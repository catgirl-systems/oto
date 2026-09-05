package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage"
)

const browseProgressRetention = 5 * time.Second

var (
	ErrBrowseNotLoaded = errors.New("daemon: share list is not loaded")
	ErrBrowseRevision  = errors.New("daemon: share list changed")
)

type BrowseResult struct {
	Entries  []soulseek.ShareEntry `json:"entries"`
	Cached   bool                  `json:"cached"`
	SavedAt  time.Time             `json:"saved_at,omitempty"`
	Revision uint64                `json:"revision"`
}

type SavedBrowse struct {
	Username string    `json:"username"`
	SavedAt  time.Time `json:"saved_at"`
}

type BrowseProgress struct {
	Username string `json:"username"`
	Received uint64 `json:"received"`
	Total    uint64 `json:"total"`
}

type trackedBrowse struct {
	generation uint64
	progress   BrowseProgress
}

type remoteShareCache struct {
	Username string
	SavedAt  time.Time
	Entries  []soulseek.ShareEntry
}

type loadedBrowse struct {
	username string
	result   BrowseResult
}

type fullBrowseFunc func(context.Context, *soulseek.Client, string, func(received, total uint64)) ([]soulseek.ShareEntry, error)

func browseUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", errors.New("daemon: browse username is required")
	}
	return strings.ToLower(username), nil
}

func (s *Service) beginBrowseProgress(username string) (string, uint64, func(received, total uint64)) {
	key, _ := browseUsername(username)
	s.mu.Lock()
	s.browseProgressSeq++
	generation := s.browseProgressSeq
	s.browseProgress[key] = trackedBrowse{generation: generation, progress: BrowseProgress{Username: strings.TrimSpace(username)}}
	s.mu.Unlock()
	return key, generation, func(received, total uint64) {
		s.mu.Lock()
		tracked, ok := s.browseProgress[key]
		if ok && tracked.generation == generation {
			tracked.progress.Received, tracked.progress.Total = received, total
			s.browseProgress[key] = tracked
		}
		s.mu.Unlock()
	}
}

func (s *Service) finishBrowseProgress(key string, generation uint64, keep bool) {
	remove := func() {
		s.mu.Lock()
		if tracked, ok := s.browseProgress[key]; ok && tracked.generation == generation {
			delete(s.browseProgress, key)
		}
		s.mu.Unlock()
	}
	if keep {
		time.AfterFunc(browseProgressRetention, remove)
		return
	}
	remove()
}

func (s *Service) BrowseProgress(username string) *BrowseProgress {
	key, err := browseUsername(username)
	if err != nil {
		return nil
	}
	s.mu.RLock()
	tracked, ok := s.browseProgress[key]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	progress := tracked.progress
	return &progress
}

func (s *Service) rememberBrowse(username string, entries []soulseek.ShareEntry, cached bool, savedAt time.Time) BrowseResult {
	key, _ := browseUsername(username)
	s.mu.Lock()
	s.browseSeq++
	result := BrowseResult{Entries: entries, Cached: cached, SavedAt: savedAt, Revision: s.browseSeq}
	s.browses[key] = loadedBrowse{username: strings.TrimSpace(username), result: result}
	s.mu.Unlock()
	return result
}

// BrowseComplete fetches a complete remote list, falling back to an explicitly saved copy.
func (s *Service) BrowseComplete(ctx context.Context, username string) (BrowseResult, error) {
	if _, err := browseUsername(username); err != nil {
		return BrowseResult{}, err
	}
	s.mu.RLock()
	client, browse := s.client, s.fullBrowse
	s.mu.RUnlock()

	var remoteErr error
	if client != nil {
		key, generation, progress := s.beginBrowseProgress(username)
		entries, err := browse(ctx, client, strings.TrimSpace(username), progress)
		s.finishBrowseProgress(key, generation, err == nil)
		if err == nil {
			return s.rememberBrowse(username, entries, false, time.Time{}), nil
		}
		remoteErr = err
	}

	cache, cacheErr := s.loadRemoteShareCache(username)
	if cacheErr == nil {
		return s.rememberBrowse(cache.Username, cache.Entries, true, cache.SavedAt), nil
	}
	if remoteErr != nil {
		return BrowseResult{}, remoteErr
	}
	if !errors.Is(cacheErr, sql.ErrNoRows) {
		return BrowseResult{}, fmt.Errorf("daemon: load saved share list: %w", cacheErr)
	}
	return BrowseResult{}, ErrNotStarted
}

func (s *Service) SaveBrowse(username string, revision uint64) (SavedBrowse, error) {
	key, err := browseUsername(username)
	if err != nil {
		return SavedBrowse{}, err
	}
	s.mu.RLock()
	loaded, ok := s.browses[key]
	s.mu.RUnlock()
	if !ok {
		return SavedBrowse{}, ErrBrowseNotLoaded
	}
	if revision == 0 || loaded.result.Revision != revision {
		return SavedBrowse{}, ErrBrowseRevision
	}
	savedAt := time.Now().UTC()
	cache := remoteShareCache{Username: loaded.username, SavedAt: savedAt, Entries: loaded.result.Entries}
	if err := s.saveRemoteShareCache(cache, revision); err != nil {
		return SavedBrowse{}, err
	}
	s.mu.Lock()
	if current, ok := s.browses[key]; ok && current.result.Revision == revision {
		current.result.SavedAt = savedAt
		s.browses[key] = current
	}
	s.mu.Unlock()
	return SavedBrowse{Username: key, SavedAt: savedAt}, nil
}

func (s *Service) SavedBrowses() ([]SavedBrowse, error) {
	if s.stateDB == nil {
		return nil, errors.New("daemon: state database is not open")
	}
	var out []SavedBrowse
	err := s.stateDB.ReadSnapshot(context.Background(), func(snapshot *storage.ReadTx) error {
		headRows, err := snapshot.Queries().ListShareHeads(context.Background())
		if err != nil {
			return err
		}
		for _, head := range headRows {
			if head.Source != "remote" {
				continue
			}
			row, err := snapshot.Queries().GetShareSnapshot(context.Background(), head.SnapshotID)
			if err != nil {
				return err
			}
			name := row.Username
			if name == "" {
				name = head.NormalizedUsername
			}
			savedAt := row.SavedAt
			if savedAt == 0 {
				savedAt = row.CreatedAt
			}
			out = append(out, SavedBrowse{Username: name, SavedAt: time.Unix(0, savedAt).UTC()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username) })
	return out, nil
}
