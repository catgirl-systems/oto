package daemon

import (
	"errors"
	"os"
	"slices"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
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
	next := s.journal
	next.Downloads = slices.Clone(next.Downloads)
	next.StatsPending = slices.Clone(next.StatsPending)
	starts := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		found := false
		for i := range next.Downloads {
			d := &next.Downloads[i]
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
				event.Kind = "forced"
				next.StatsPending = append(next.StatsPending, s.statsPrepareEventLocked(event))
			}
			break
		}
		if !found {
			result.Errors = append(result.Errors, UploadActionError{id, os.ErrNotExist.Error()})
		}
	}
	if err := config.SaveJSON(s.journalPath, next); err != nil {
		s.mu.Unlock()
		return UploadActionResult{}, err
	}
	s.journal = next
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
