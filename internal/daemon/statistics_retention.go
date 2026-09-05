package daemon

import "time"

func (s *Service) automaticStatsPrune(now time.Time) error {
	s.mu.RLock()
	policy := s.cfg.Statistics
	s.mu.RUnlock()
	for _, item := range []struct {
		days int
		logs bool
	}{{policy.LogRetentionDays, true}, {policy.DailyRetentionDays, false}} {
		if item.days == 0 {
			continue
		}
		cutoff := now.UTC().AddDate(0, 0, -item.days)
		if cutoff.Year() < 1970 {
			continue
		}
		if _, err := s.PruneStatistics(cutoff, item.logs, !item.logs, true); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) statsOnlineLocked(now time.Time) {
	t := s.telemetry
	if t == nil {
		return
	}
	if t.online == nil {
		t.online = map[string]time.Duration{}
		t.connections = map[string]uint64{}
	}
	if t.connected != "" && !t.lastSample.IsZero() {
		t.online[t.connected] += max(0, now.Sub(t.lastSample))
	}
	account := ""
	if s.status == StatusConnected {
		account = s.uploadAccountLocked(s.uploadEpoch)
	}
	if account != "" && account != t.connected {
		t.connections[account]++
	}
	t.connected, t.lastSample = account, now
}
