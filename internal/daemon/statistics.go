package daemon

import (
	"github.com/catgirl-systems/oto/internal/stats"
	"slices"
	"time"
)

type DirectionTotals struct {
	Download stats.Totals `json:"download"`
	Upload   stats.Totals `json:"upload"`
}
type StatsOverview struct {
	OnlineSeconds uint64          `json:"online_seconds"`
	Reconnects    uint64          `json:"reconnects"`
	Account       string          `json:"account"`
	Accounts      []string        `json:"accounts"`
	Session       string          `json:"session"`
	Since         time.Time       `json:"since"`
	UptimeSeconds uint64          `json:"uptime_seconds"`
	Lifetime      DirectionTotals `json:"lifetime"`
	SessionTotals DirectionTotals `json:"session_totals"`
	ActiveFiles   uint64          `json:"active_files"`
	QueuedFiles   uint64          `json:"queued_files"`
	ActiveBytes   uint64          `json:"active_bytes"`
	QueuedBytes   uint64          `json:"queued_bytes"`
	Samples       []RateSample    `json:"samples"`
	Warning       string          `json:"warning,omitempty"`
}

func (s *Service) statsFilter(f stats.Filter) stats.Filter {
	if f.Account == "" {
		s.mu.RLock()
		f.Account = accountKey(s.cfg)
		s.mu.RUnlock()
	}
	return f
}
func (s *Service) Statistics(f stats.Filter) (StatsOverview, error) {
	store, err := s.statsStore()
	if err != nil {
		return StatsOverview{}, err
	}
	f = s.statsFilter(f)
	s.mu.RLock()
	t := s.telemetry
	out := StatsOverview{Account: f.Account, Session: t.session, Since: s.journal.StatsSince, UptimeSeconds: uint64(max(0, time.Since(t.started).Seconds())), Warning: t.warning, Samples: slices.Clone(t.samples[f.Account])}
	out.OnlineSeconds = uint64(max(0, t.online[f.Account].Seconds()))
	if n := t.connections[f.Account]; n > 0 {
		out.Reconnects = n - 1
	}
	for id, tr := range s.transfers {
		account := accountKey(s.cfg)
		if a := t.attempts[id]; a != nil {
			account = a.event.Account
		} else {
			for _, u := range s.journal.Uploads {
				if u.ID == id {
					account = u.Account
					break
				}
			}
		}
		if account != f.Account {
			continue
		}
		switch tr.State {
		case "running":
			out.ActiveFiles++
			out.ActiveBytes += tr.Total
		case "queued", "retrying":
			out.QueuedFiles++
			out.QueuedBytes += tr.Total
		}
	}
	s.mu.RUnlock()
	if out.Accounts, err = store.Accounts(); err != nil {
		return out, err
	}
	if !slices.Contains(out.Accounts, f.Account) {
		out.Accounts = append(out.Accounts, f.Account)
		slices.Sort(out.Accounts)
	}
	for _, scope := range []struct {
		session string
		target  *DirectionTotals
	}{{"", &out.Lifetime}, {out.Session, &out.SessionTotals}} {
		q := f
		q.Session = scope.session
		q.Direction = "download"
		if scope.target.Download, err = store.Totals(q); err != nil {
			return out, err
		}
		q.Direction = "upload"
		if scope.target.Upload, err = store.Totals(q); err != nil {
			return out, err
		}
	}
	return out, nil
}
func (s *Service) StatsSeries(f stats.Filter) ([]stats.Daily, error) {
	store, err := s.statsStore()
	if err != nil {
		return nil, err
	}
	return store.Series(s.statsFilter(f))
}
func (s *Service) StatsPeers(f stats.Filter) (stats.PeerPage, error) {
	store, err := s.statsStore()
	if err != nil {
		return stats.PeerPage{}, err
	}
	return store.Peers(s.statsFilter(f))
}
func (s *Service) TransferLog(f stats.Filter) (stats.LogPage, error) {
	store, err := s.statsStore()
	if err != nil {
		return stats.LogPage{}, err
	}
	return store.Log(s.statsFilter(f))
}
func (s *Service) PruneStatistics(cutoff time.Time, logs, daily, apply bool) (stats.PruneResult, error) {
	store, err := s.statsStore()
	if err != nil {
		return stats.PruneResult{}, err
	}
	return store.Prune(cutoff, logs, daily, apply)
}
