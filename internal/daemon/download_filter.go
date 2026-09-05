package daemon

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/stats"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func downloadMatcher(c config.Config) (*soulseek.ShareExclusions, error) {
	if !c.Downloads.FiltersEnabled {
		return nil, nil
	}
	patterns, err := config.NormalizeDownloadFilters(c.Downloads.FilterPatterns)
	if err != nil {
		return nil, err
	}
	return soulseek.NewShareExclusions(patterns)
}

// ForceDownloads authorizes only existing filtered file IDs, never entire folders.
func (s *Service) ForceDownloads(ids []string) (UploadActionResult, error) {
	result := UploadActionResult{Errors: []UploadActionError{}}
	if len(ids) == 0 || len(ids) > 10000 {
		return result, errors.New("daemon: explicit filtered file IDs required (maximum 10000)")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return result, ErrClosed
	}
	previous := s.snapshotLocked(ids...)
	starts := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		found := false
		for i := range s.journal.Downloads {
			d := &s.journal.Downloads[i]
			if d.ID != id {
				continue
			}
			found = true
			if d.State != "filtered" {
				result.Skipped++
				break
			}
			d.FilterBypass, d.State, d.Error, d.RetryAt, d.UpdatedAt = true, "queued", "", time.Time{}, time.Now().UTC()
			starts = append(starts, id)
			result.Changed++
			if s.telemetry != nil {
				event := s.statsTemplateLocked(id)
				event.Kind = stats.KindForced
				s.statsEventLocked(event)
			}
			break
		}
		if !found {
			result.Errors = append(result.Errors, UploadActionError{id, os.ErrNotExist.Error()})
		}
	}
	dirty := make(map[string]bool, len(starts))
	for _, id := range starts {
		dirty[id] = true
	}
	if err := s.commitLockedFor(context.Background(), dirty, func(q *db.Queries) error {
		for _, id := range starts {
			for _, d := range s.journal.Downloads {
				if d.ID == id {
					if err := q.UpsertDownload(context.Background(), downloadParams(d)); err != nil {
						return err
					}
					break
				}
			}
		}
		return nil
	}); err != nil {
		s.restoreLocked(previous)
		s.mu.Unlock()
		return UploadActionResult{}, err
	}
	for _, id := range starts {
		tr := s.transfers[id]
		tr.State, tr.Error = "queued", ""
		s.transfers[id] = tr

	}
	s.mu.Unlock()
	for _, id := range starts {
		s.startDownload(id)
	}
	return result, nil
}
