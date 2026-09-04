package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

const (
	remoteShareCacheVersion = 1
	browseProgressRetention = 5 * time.Second
)

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
	Version  int                   `json:"version"`
	Username string                `json:"username"`
	SavedAt  time.Time             `json:"saved_at"`
	Entries  []soulseek.ShareEntry `json:"entries"`
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

func remoteShareCachePath(dir, username string) (string, error) {
	key, err := browseUsername(username)
	if err != nil {
		return "", err
	}
	name := base64.RawURLEncoding.EncodeToString([]byte(key)) + ".json"
	return filepath.Join(dir, name), nil
}

func loadRemoteShareCache(dir, username string) (remoteShareCache, error) {
	var cache remoteShareCache
	path, err := remoteShareCachePath(dir, username)
	if err != nil {
		return cache, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cache, err
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return remoteShareCache{}, err
	}
	if cache.Version != remoteShareCacheVersion {
		return remoteShareCache{}, fmt.Errorf("unsupported remote share cache version %d", cache.Version)
	}
	if !strings.EqualFold(strings.TrimSpace(cache.Username), strings.TrimSpace(username)) {
		return remoteShareCache{}, errors.New("remote share cache username mismatch")
	}
	return cache, nil
}

func saveRemoteShareCache(dir string, cache remoteShareCache) error {
	path, err := remoteShareCachePath(dir, cache.Username)
	if err != nil {
		return err
	}
	return config.SaveJSON(path, cache)
}

func listSavedBrowses(dir string) ([]SavedBrowse, error) {
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]SavedBrowse, 0, len(files))
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		encoded := strings.TrimSuffix(file.Name(), ".json")
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		username := string(decoded)
		path, err := remoteShareCachePath(dir, username)
		if err != nil || filepath.Base(path) != file.Name() {
			continue
		}
		info, err := file.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, SavedBrowse{Username: username, SavedAt: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
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
	client, browse, dir := s.client, s.fullBrowse, s.remoteSharesDir
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

	cache, cacheErr := loadRemoteShareCache(dir, username)
	if cacheErr == nil {
		return s.rememberBrowse(cache.Username, cache.Entries, true, cache.SavedAt), nil
	}
	if remoteErr != nil {
		return BrowseResult{}, remoteErr
	}
	if !errors.Is(cacheErr, os.ErrNotExist) {
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
	dir := s.remoteSharesDir
	s.mu.RUnlock()
	if !ok {
		return SavedBrowse{}, ErrBrowseNotLoaded
	}
	if revision == 0 || loaded.result.Revision != revision {
		return SavedBrowse{}, ErrBrowseRevision
	}
	savedAt := time.Now().UTC()
	cache := remoteShareCache{Version: remoteShareCacheVersion, Username: loaded.username, SavedAt: savedAt, Entries: loaded.result.Entries}
	if err := saveRemoteShareCache(dir, cache); err != nil {
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
	s.mu.RLock()
	dir := s.remoteSharesDir
	s.mu.RUnlock()
	return listSavedBrowses(dir)
}
