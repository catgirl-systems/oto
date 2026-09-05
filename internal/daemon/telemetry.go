package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/catgirl-systems/oto/internal/stats"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

type attemptStats struct {
	completed, streamBegan, interrupted bool
	event                               stats.Event
	queuedAt                            time.Time
	activeAt                            time.Time
	done                                uint64
	terminal                            bool
	days                                map[string]stats.Event
}
type RateSample struct {
	At       time.Time `json:"at"`
	Download uint64    `json:"download"`
	Upload   uint64    `json:"upload"`
}
type telemetryState struct {
	queued         map[string]time.Time
	online         map[string]time.Duration
	connections    map[string]uint64
	connected      string
	lastSample     time.Time
	store          *stats.Store
	flushMu        sync.Mutex
	pending        []stats.Event
	activeDirty    map[string]bool
	activeDeleted  map[string]bool
	dirtyDownloads map[string]bool
	dirtyUploads   map[string]bool
	session        string
	sequence       uint64
	started        time.Time
	warning        string
	attempts       map[string]*attemptStats
	samples        map[string][]RateSample
	wake           chan struct{}
	statsSince     time.Time
}

func (s *Service) initTelemetry() {
	now := time.Now().UTC()
	since := s.journal.StatsSince
	if since.IsZero() {
		since = now
	}
	t := &telemetryState{session: rand.Text(), started: now, attempts: map[string]*attemptStats{}, samples: map[string][]RateSample{}, wake: make(chan struct{}, 1), activeDirty: map[string]bool{}, activeDeleted: map[string]bool{}, dirtyDownloads: map[string]bool{}, dirtyUploads: map[string]bool{}, statsSince: since}
	t.store = stats.New(s.stateDB)
	if t.store == nil {
		t.warning = "Statistics unavailable: shared database is unavailable"
	}
	s.telemetry = t
}
func (s *Service) statsPrepareEventLocked(e stats.Event) stats.Event {
	e.Error = statsError(e.Error)
	t := s.telemetry
	t.sequence++
	e.ID = fmt.Sprintf("%s:%d", t.session, t.sequence)
	if e.Session == "" {
		e.Session = t.session
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	return e
}
func (s *Service) statsEventLocked(e stats.Event) {
	if s.telemetry != nil {
		s.telemetry.pending = append(s.telemetry.pending, s.statsPrepareEventLocked(e))
	}
}
func (s *Service) statsTemplateLocked(id string) stats.Event {
	tr := s.transfers[id]
	e := stats.Event{Account: accountKey(s.cfg), TransferID: id, Peer: tr.Username, Direction: tr.Direction, Filename: tr.Filename}
	for _, u := range s.journal.Uploads {
		if u.ID == id {
			e.Account = u.Account
			break
		}
	}
	for _, d := range s.journal.Downloads {
		if d.ID == id {
			root := d.DownloadDir
			if root == "" {
				root = s.cfg.DownloadDir
			}
			e.Destination = filepath.Join(root, d.Destination)
			if d.StatsAccount != "" {
				e.Account = d.StatsAccount
			}
			break
		}
	}
	return e
}
func (s *Service) statsBeginLocked(id, account string) {
	t := s.telemetry
	if t == nil {
		return
	}
	if a := t.attempts[id]; a != nil && !a.terminal {
		return
	}
	e := s.statsTemplateLocked(id)
	if account != "" {
		e.Account = account
	}
	if e.Direction == "" {
		return
	}
	t.sequence++
	e.Attempt = t.sequence
	e.Session = t.session
	e.Kind = stats.KindStarted
	now := time.Now().UTC()
	a := &attemptStats{event: e, queuedAt: now, days: map[string]stats.Event{}}
	for _, d := range s.journal.Downloads {
		if d.ID == id {
			a.queuedAt = d.UpdatedAt
			break
		}
	}
	for _, u := range s.journal.Uploads {
		if u.ID == id {
			a.queuedAt = u.QueuedAt
			if a.queuedAt.IsZero() {
				a.queuedAt = u.UpdatedAt
			}
			break
		}
	}
	if queued := t.queued[id]; !queued.IsZero() {
		a.queuedAt = queued
	}
	if prior := t.attempts[id]; prior != nil {
		retry := e
		retry.Kind = stats.KindRetry
		s.statsEventLocked(retry)
	}
	t.attempts[id] = a
	interrupted := e
	interrupted.ID = fmt.Sprintf("%s:interrupted:%d", e.Session, e.Attempt)
	interrupted.Kind = stats.KindInterrupted
	interrupted.At = now
	interrupted.Error = "Daemon stopped before terminal accounting (detected on restart)"
	t.activeDirty[id] = true
	s.statsEventLocked(e)
	select {
	case t.wake <- struct{}{}:
	default:
	}
}
func (s *Service) statsStreamLocked(id string, offset uint64) {
	if s.telemetry == nil {
		return
	}
	s.statsBeginLocked(id, "")
	a := s.telemetry.attempts[id]
	if a == nil || !a.activeAt.IsZero() {
		return
	}
	now := time.Now().UTC()
	a.streamBegan = true
	a.activeAt = now
	a.done = offset
	e := a.event
	e.Kind = stats.KindProgress
	e.WaitMillis = uint64(max(0, now.Sub(a.queuedAt).Milliseconds()))
	s.statsEventLocked(e)
	if offset > 0 {
		e.WaitMillis = 0
		e.Kind = stats.KindResume
		s.statsEventLocked(e)
	}
}
func (a *attemptStats) day(at time.Time) stats.Event {
	key := at.UTC().Format(time.DateOnly)
	if e, ok := a.days[key]; ok {
		return e
	}
	e := a.event
	e.Kind = stats.KindProgress
	e.At = at.UTC()
	return e
}
func (s *Service) statsProgressLocked(id string, done uint64) {
	if s.telemetry == nil {
		return
	}
	a := s.telemetry.attempts[id]
	if a == nil || !a.streamBegan || a.terminal || done <= a.done {
		return
	}
	now := time.Now().UTC()
	e := a.day(now)
	delta := done - a.done
	if ^uint64(0)-e.Bytes < delta {
		s.telemetry.warning = "Statistics payload counter overflow"
		return
	}
	e.Bytes += delta
	e.Peak = max(e.Peak, s.transferTiming[id].speed)
	a.days[now.Format(time.DateOnly)] = e
	a.done = done
}
func (a *attemptStats) checkpoint(now time.Time) {
	// Split cumulative active time at UTC boundaries, independently of payload arrival.
	for !a.activeAt.IsZero() && a.activeAt.Before(now) {
		at := a.activeAt.UTC()
		end := at.Truncate(24 * time.Hour).Add(24 * time.Hour)
		if end.After(now) {
			end = now
		}
		e := a.day(at)
		e.ActiveMillis += uint64(max(0, end.Sub(at).Milliseconds()))
		a.days[at.Format(time.DateOnly)] = e
		a.activeAt = end
	}
}
func (s *Service) statsStopLocked(id string) {
	if s.telemetry == nil {
		return
	}
	if a := s.telemetry.attempts[id]; a != nil {
		a.checkpoint(time.Now().UTC())
		a.activeAt = time.Time{}
	}
}
func (s *Service) statsDrainLocked(a *attemptStats) {
	keys := make([]string, 0, len(a.days))
	for day := range a.days {
		keys = append(keys, day)
	}
	slices.Sort(keys)
	for _, day := range keys {
		s.statsEventLocked(a.days[day])
		delete(a.days, day)
	}
}
func (s *Service) statsStateLocked(id, state string) {
	t := s.telemetry
	if t == nil {
		return
	}
	kind := state
	switch state {
	case "running":
		if s.transfers[id].Direction == "upload" {
			s.statsBeginLocked(id, "")
		}
		return
	case "retrying":
		kind = stats.KindFailed
	case "paused", "queued":
		kind = stats.KindInterrupted
	case "completed", "failed", "cancelled", "interrupted":
	default:
		return
	}
	a := t.attempts[id]
	if a == nil {
		if state == "queued" {
			return
		}
		a = &attemptStats{event: s.statsTemplateLocked(id), days: map[string]stats.Event{}}
		t.attempts[id] = a
	}
	if a.completed || (a.terminal && kind != stats.KindCompleted) {
		return
	}
	a.checkpoint(time.Now().UTC())
	a.activeAt = time.Time{}
	s.statsDrainLocked(a)
	e := a.event
	if a.terminal {
		e.Attempt = 0
	}
	e.Kind, e.Error = kind, s.transfers[id].Error
	e.Destination = s.statsTemplateLocked(id).Destination
	if kind == stats.KindCompleted {
		e.CompletedBytes = s.transfers[id].Total
		e.FileComplete = true
		a.completed = true
	}
	s.statsEventLocked(e)
	a.terminal = true
	t.activeDeleted[id] = true
	delete(t.activeDirty, id)
	a.interrupted = kind == stats.KindInterrupted
	select {
	case t.wake <- struct{}{}:
	default:
	}
}
func (s *Service) flushStats() error { return s.flushStatsContext(context.Background()) }

func (s *Service) flushStatsContext(ctx context.Context) error {
	t := s.telemetry
	if t == nil {
		return nil
	}
	t.flushMu.Lock()
	defer t.flushMu.Unlock()
	s.mu.Lock()
	for id, a := range t.attempts {
		a.checkpoint(time.Now().UTC())
		s.statsDrainLocked(a)
		if !a.terminal {
			t.activeDirty[id] = true
		}
	}
	// The transaction includes only explicitly dirty rows, their events, and
	// active-attempt checkpoints. Unchanged history is never rewritten.
	ids := map[string]bool{}
	for id := range t.dirtyDownloads {
		ids[id] = true
	}
	var clearUploads []string
	for id := range t.dirtyUploads {
		ids[id] = true
		if s.cfg.Uploads.AutoClearCompleted && s.transfers[id].State == "completed" {
			clearUploads = append(clearUploads, id)
		}
	}
	for id := range t.activeDirty {
		ids[id] = true
	}
	for id := range t.activeDeleted {
		ids[id] = true
	}
	for _, e := range t.pending {
		ids[e.TransferID] = true
	}
	err := error(nil)
	if len(ids) > 0 {
		err = s.commitLockedFor(ctx, ids, func(q *db.Queries) error {
			for id := range t.dirtyDownloads {
				for _, d := range s.journal.Downloads {
					if d.ID == id {
						if err := q.UpsertDownload(ctx, downloadParams(d)); err != nil {
							return err
						}
						break
					}
				}
			}
			for id := range t.dirtyUploads {
				for _, u := range s.journal.Uploads {
					if u.ID == id {
						if tr, ok := s.transfers[id]; ok {
							u.Transfer = tr
						}
						if err := q.UpsertUpload(ctx, uploadParams(u)); err != nil {
							return err
						}
						break
					}
				}
			}
			return nil
		})
	}
	if err != nil {
		t.warning = "Statistics persistence: " + err.Error()
		s.mu.Unlock()
		return err
	}
	t.warning = ""
	var retries []completionRetry
	for id, retry := range s.completionRetries {
		retries = append(retries, retry)
		delete(s.completionRetries, id)
	}
	s.mu.Unlock()
	for _, retry := range retries {
		s.runCompletionEffects(retry)
	}
	for _, id := range clearUploads {
		s.mu.Lock()
		if tr, ok := s.transfers[id]; ok && tr.State == "completed" {
			delete(s.transfers, id)
			if err := s.persistUploadLocked(id); err != nil {
				s.transfers[id] = tr
				t.warning = err.Error()
			} else {
				s.forgetTransferLocked(id)
			}
		}
		s.mu.Unlock()
	}
	return nil
}
func (s *Service) telemetryLoop(ctx context.Context) {
	defer s.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	checkpoints := 0
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.telemetry.wake:
			_ = s.flushStats()
		case now := <-tick.C:
			now = time.Now()
			s.mu.Lock()
			s.statsOnlineLocked(now)
			rates := map[string]RateSample{accountKey(s.cfg): {At: now.UTC()}}
			for account := range s.telemetry.samples {
				rates[account] = RateSample{At: now.UTC()}
			}
			for id, a := range s.telemetry.attempts {
				if a.terminal {
					continue
				}
				sample := rates[a.event.Account]
				sample.At = now.UTC()
				tr := s.transferTiming[id].snapshot(s.transfers[id], now)
				if a.event.Direction == "upload" {
					sample.Upload += tr.SpeedBPS
				} else {
					sample.Download += tr.SpeedBPS
				}
				rates[a.event.Account] = sample
			}
			for account, sample := range rates {
				samples := append(s.telemetry.samples[account], sample)
				for len(samples) > 0 && samples[0].At.Before(now.Add(-5*time.Minute)) {
					samples = samples[1:]
				}
				if len(samples) > 300 {
					samples = samples[len(samples)-300:]
				}
				s.telemetry.samples[account] = samples
			}
			s.mu.Unlock()
			checkpoints++
			if checkpoints%5 == 0 {
				if err := s.flushStats(); err == nil && now.Sub(lastPrune) >= time.Hour {
					if err = s.automaticStatsPrune(now); err != nil {
						s.mu.Lock()
						s.telemetry.warning = err.Error()
						s.mu.Unlock()
					} else {
						lastPrune = now
					}
				}
			}
		}
	}
}
func (s *Service) statsStore() (*stats.Store, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.telemetry == nil {
		return nil, errors.New("statistics not initialized")
	}
	if s.telemetry.store == nil {
		return nil, errors.New(s.telemetry.warning)
	}
	return s.telemetry.store, nil
}
