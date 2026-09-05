package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/catgirl-systems/oto/internal/stats"
	"github.com/catgirl-systems/oto/internal/storage"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func downloadParams(d Download) db.UpsertDownloadParams {
	var retry *int64
	if !d.RetryAt.IsZero() {
		n := d.RetryAt.UnixNano()
		retry = &n
	}
	return db.UpsertDownloadParams{ID: d.ID, StatsAccount: d.StatsAccount, FilterBypass: boolInt(d.FilterBypass), Username: d.Username, Filename: d.Filename, Size: storage.Uint64Bytes(d.Size), Offset: storage.Uint64Bytes(d.Offset), DownloadDir: d.DownloadDir, Destination: d.Destination, State: d.State, RetryAt: retry, Error: d.Error, CreatedAt: d.CreatedAt.UnixNano(), UpdatedAt: d.UpdatedAt.UnixNano()}
}

func uploadParams(u Upload) db.UpsertUploadParams {
	return db.UpsertUploadParams{ID: u.ID, Account: u.Account, Username: u.Username, Filename: u.Filename, Direction: u.Direction, State: u.State, Done: storage.Uint64Bytes(u.Done), Total: storage.Uint64Bytes(u.Total), ElapsedMs: optionalUintBytes(u.ElapsedMS), SpeedBps: storage.Uint64Bytes(u.SpeedBPS), EtaSeconds: optionalUintBytes(u.ETASeconds), Queue: int64(u.Queue), Error: u.Error, QueueOrder: storage.Uint64Bytes(u.QueueOrder), Fingerprint: u.Fingerprint, CreatedAt: u.CreatedAt.UnixNano(), QueuedAt: u.QueuedAt.UnixNano(), UpdatedAt: u.UpdatedAt.UnixNano(), Recoverable: boolInt(u.Recoverable)}
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
func optionalUintBytes(v *uint64) []byte {
	if v == nil {
		return nil
	}
	return storage.Uint64Bytes(*v)
}
func unixTime(v int64) time.Time {
	return time.Unix(0, v).UTC()
}
func optionalTime(v *int64) time.Time {
	if v == nil {
		return time.Time{}
	}
	return unixTime(*v)
}

func fromDownload(row db.Download) (Download, error) {
	size, err := storage.Uint64FromBytes(row.Size)
	if err != nil {
		return Download{}, err
	}
	offset, err := storage.Uint64FromBytes(row.Offset)
	if err != nil {
		return Download{}, err
	}
	return Download{ID: row.ID, StatsAccount: row.StatsAccount, FilterBypass: row.FilterBypass != 0, Username: row.Username, Filename: row.Filename, Size: size, Offset: offset, DownloadDir: row.DownloadDir, Destination: row.Destination, State: row.State, RetryAt: optionalTime(row.RetryAt), Error: row.Error, CreatedAt: unixTime(row.CreatedAt), UpdatedAt: unixTime(row.UpdatedAt)}, nil
}

func fromUpload(row db.Upload) (Upload, error) {
	done, err := storage.Uint64FromBytes(row.Done)
	if err != nil {
		return Upload{}, err
	}
	total, err := storage.Uint64FromBytes(row.Total)
	if err != nil {
		return Upload{}, err
	}
	queueOrder, err := storage.Uint64FromBytes(row.QueueOrder)
	if err != nil {
		return Upload{}, err
	}
	var elapsed, eta *uint64
	if len(row.ElapsedMs) != 0 {
		v, e := storage.Uint64FromBytes(row.ElapsedMs)
		if e != nil {
			return Upload{}, e
		}
		elapsed = &v
	}
	if len(row.EtaSeconds) != 0 {
		v, e := storage.Uint64FromBytes(row.EtaSeconds)
		if e != nil {
			return Upload{}, e
		}
		eta = &v
	}
	speed, err := storage.Uint64FromBytes(row.SpeedBps)
	if err != nil {
		return Upload{}, err
	}
	return Upload{QueueOrder: queueOrder, Transfer: Transfer{ID: row.ID, Username: row.Username, Filename: row.Filename, Direction: row.Direction, State: row.State, Done: done, Total: total, ElapsedMS: elapsed, SpeedBPS: speed, ETASeconds: eta, Queue: uint32(row.Queue), Error: row.Error}, Account: row.Account, Fingerprint: row.Fingerprint, CreatedAt: unixTime(row.CreatedAt), QueuedAt: unixTime(row.QueuedAt), UpdatedAt: unixTime(row.UpdatedAt), Recoverable: row.Recoverable != 0}, nil
}

func (s *Service) loadState() error {
	q := s.stateDB.Queries()
	meta, err := q.GetStateMeta(context.Background())
	if err != nil {
		return err
	}
	s.journal.DownloadSequence, err = storage.Uint64FromBytes(meta.DownloadSequence)
	if err != nil {
		return err
	}
	s.seq = s.journal.DownloadSequence
	s.journal.UploadSequence, err = storage.Uint64FromBytes(meta.UploadSequence)
	if err != nil {
		return err
	}
	s.journal.UploadQueueSequence, err = storage.Uint64FromBytes(meta.UploadQueueSequence)
	if err != nil {
		return err
	}
	s.journal.StatsSince = optionalTime(meta.StatsSince)
	downloadRows, err := q.ListDownloads(context.Background())
	if err != nil {
		return err
	}
	for _, row := range downloadRows {
		d, e := fromDownload(row)
		if e != nil {
			return e
		}
		if d.State == "" {
			d.State = "queued"
		}
		s.journal.Downloads = append(s.journal.Downloads, d)
		s.seq = max(s.seq, parseDownloadSequence(d.ID))
		s.transfers[d.ID] = Transfer{ID: d.ID, Username: d.Username, Filename: d.Filename, Direction: "download", State: d.State, Done: d.Offset, Total: d.Size, Error: d.Error}
	}
	uploadRows, err := q.ListUploads(context.Background())
	if err != nil {
		return err
	}
	for _, row := range uploadRows {
		u, e := fromUpload(row)
		if e != nil {
			return e
		}
		s.journal.Uploads = append(s.journal.Uploads, u)
		s.transfers[u.ID] = u.Transfer
	}
	return nil
}

func parseDownloadSequence(id string) uint64 {
	var n uint64
	if len(id) > 2 && id[:2] == "d-" {
		_, _ = fmt.Sscan(id[2:], &n)
	}
	return n
}

type telemetrySnapshot struct {
	pending        []stats.Event
	activeDirty    map[string]bool
	activeDeleted  map[string]bool
	dirtyDownloads map[string]bool
	dirtyUploads   map[string]bool
	queued         map[string]time.Time
	attempts       map[string]*attemptStats
	sequence       uint64
	warning        string
}

// Roll back only the records touched by a rejected action. No queue-sized copy
// or persistence diff is needed; structural removals happen after commit.
type daemonSnapshot struct {
	ids       map[string]bool
	downloads map[int]Download
	uploads   map[int]Upload
	seq       uint64
	transfers map[string]Transfer
	timing    map[string]transferTiming
	telemetry telemetrySnapshot
}

func snapshotEntries[V any](src map[string]V, ids map[string]bool) map[string]V {
	dst := make(map[string]V)
	for id := range ids {
		if value, ok := src[id]; ok {
			dst[id] = value
		}
	}
	return dst
}

func restoreEntries[V any](dst, saved map[string]V, ids map[string]bool) {
	for id := range ids {
		delete(dst, id)
	}
	for id, value := range saved {
		dst[id] = value
	}
}

func (s *Service) snapshotLocked(selected ...string) daemonSnapshot {
	ids := make(map[string]bool, len(selected))
	for _, id := range selected {
		ids[id] = true
	}
	all := len(selected) == 0
	x := daemonSnapshot{ids: ids, downloads: map[int]Download{}, uploads: map[int]Upload{}, seq: s.seq}
	for i, d := range s.journal.Downloads {
		if all || ids[d.ID] {
			x.downloads[i] = d
			ids[d.ID] = true
		}
	}
	for i, u := range s.journal.Uploads {
		if all || ids[u.ID] {
			x.uploads[i] = u
			ids[u.ID] = true
		}
	}
	if all {
		for id := range s.transfers {
			ids[id] = true
		}
	}
	x.transfers = snapshotEntries(s.transfers, ids)
	x.timing = snapshotEntries(s.transferTiming, ids)
	if t := s.telemetry; t != nil {
		x.telemetry = telemetrySnapshot{
			pending: slices.Clone(t.pending), sequence: t.sequence, warning: t.warning,
			activeDirty: snapshotEntries(t.activeDirty, ids), activeDeleted: snapshotEntries(t.activeDeleted, ids),
			dirtyDownloads: snapshotEntries(t.dirtyDownloads, ids), dirtyUploads: snapshotEntries(t.dirtyUploads, ids),
			queued: snapshotEntries(t.queued, ids), attempts: snapshotEntries(t.attempts, ids),
		}
		for id, a := range x.telemetry.attempts {
			copyA := *a
			copyA.days = make(map[string]stats.Event, len(a.days))
			for day, event := range a.days {
				copyA.days[day] = event
			}
			x.telemetry.attempts[id] = &copyA
		}
	}
	return x
}

func (s *Service) restoreLocked(x daemonSnapshot) {
	for i, d := range x.downloads {
		s.journal.Downloads[i] = d
	}
	for i, u := range x.uploads {
		s.journal.Uploads[i] = u
	}
	s.seq = x.seq
	restoreEntries(s.transfers, x.transfers, x.ids)
	restoreEntries(s.transferTiming, x.timing, x.ids)
	if t := s.telemetry; t != nil {
		old := x.telemetry
		t.pending, t.sequence, t.warning = old.pending, old.sequence, old.warning
		restoreEntries(t.activeDirty, old.activeDirty, x.ids)
		restoreEntries(t.activeDeleted, old.activeDeleted, x.ids)
		restoreEntries(t.dirtyDownloads, old.dirtyDownloads, x.ids)
		restoreEntries(t.dirtyUploads, old.dirtyUploads, x.ids)
		restoreEntries(t.queued, old.queued, x.ids)
		restoreEntries(t.attempts, old.attempts, x.ids)
	}
}

// commitLockedFor persists only explicitly named transfer rows, events, and
// active markers. Unrelated retry work remains in memory for its own commit.
func (s *Service) commitLockedFor(ctx context.Context, ids map[string]bool, write func(*db.Queries) error) error {
	if s.stateDB == nil {
		return errors.New("daemon: state database unavailable")
	}
	t := s.telemetry
	var pending []stats.Event
	if t != nil {
		for _, e := range t.pending {
			if ids[e.TransferID] {
				pending = append(pending, e)
			}
		}
	}
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		q := db.New(tx)
		if write != nil {
			if err = write(q); err != nil {
				return err
			}
		}
		if err = q.SetDownloadSequence(ctx, storage.Uint64Bytes(s.seq)); err != nil {
			return err
		}
		if err = q.SetUploadSequence(ctx, storage.Uint64Bytes(s.journal.UploadSequence)); err != nil {
			return err
		}
		if err = q.SetUploadQueueSequence(ctx, storage.Uint64Bytes(s.journal.UploadQueueSequence)); err != nil {
			return err
		}
		if t != nil {
			if !t.statsSince.IsZero() {
				since := t.statsSince.UnixNano()
				if err = q.SetStatsSince(ctx, &since); err != nil {
					return err
				}
			}
			if t.store == nil {
				return errors.New("statistics unavailable")
			}
			if err = t.store.RecordBatchTx(ctx, tx, pending); err != nil {
				return err
			}
			for id := range t.activeDirty {
				if !ids[id] {
					continue
				}
				a := t.attempts[id]
				if a == nil || a.terminal {
					continue
				}
				e := a.event
				e.ID = fmt.Sprintf("%s:interrupted:%d", e.Session, e.Attempt)
				e.Kind = stats.KindInterrupted
				e.At = time.Now().UTC()
				e.Error = "Daemon stopped before terminal accounting (detected on restart)"
				payload, e2 := json.Marshal(e)
				if e2 != nil {
					return e2
				}
				if err = q.UpsertActiveAttempt(ctx, db.UpsertActiveAttemptParams{ID: id, TransferID: id, Attempt: storage.Uint64Bytes(a.event.Attempt), Direction: e.Direction, Username: e.Peer, Filename: e.Filename, EventID: e.ID, Payload: payload}); err != nil {
					return err
				}
			}
			for id := range t.activeDeleted {
				if !ids[id] {
					continue
				}
				if err = q.DeleteActiveAttempt(ctx, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if t != nil {
		if len(pending) > 0 {
			kept := t.pending[:0]
			for _, e := range t.pending {
				if !ids[e.TransferID] {
					kept = append(kept, e)
				}
			}
			t.pending = kept
		}
		for id := range ids {
			delete(t.activeDirty, id)
			delete(t.activeDeleted, id)
			delete(t.dirtyDownloads, id)
			delete(t.dirtyUploads, id)
		}
	}
	return nil
}

func (s *Service) persistDownloadLocked(d Download) error {
	if s.telemetry != nil {
		s.telemetry.dirtyDownloads[d.ID] = true
	}
	return s.commitLockedFor(context.Background(), map[string]bool{d.ID: true}, func(q *db.Queries) error { return q.UpsertDownload(context.Background(), downloadParams(d)) })
}
func (s *Service) deleteDownloadLocked(id string) error {
	if s.telemetry != nil {
		s.telemetry.dirtyDownloads[id] = true
	}
	return s.commitLockedFor(context.Background(), map[string]bool{id: true}, func(q *db.Queries) error { return q.DeleteDownload(context.Background(), id) })
}
func (s *Service) persistUploadRowLocked(u Upload) error {
	if s.telemetry != nil {
		s.telemetry.dirtyUploads[u.ID] = true
	}
	return s.commitLockedFor(context.Background(), map[string]bool{u.ID: true}, func(q *db.Queries) error { return q.UpsertUpload(context.Background(), uploadParams(u)) })
}
func (s *Service) deleteUploadRowLocked(id string) error {
	if s.telemetry != nil {
		s.telemetry.dirtyUploads[id] = true
	}
	return s.commitLockedFor(context.Background(), map[string]bool{id: true}, func(q *db.Queries) error { return q.DeleteUpload(context.Background(), id) })
}

func (s *Service) ensureStatsSince() error {
	if s.stateDB == nil || s.telemetry == nil {
		return errors.New("daemon: state database unavailable")
	}
	since := s.telemetry.statsSince.UnixNano()
	return s.stateDB.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return s.stateDB.Queries().WithTx(tx).SetStatsSince(context.Background(), &since)
	})
}

func (s *Service) recoverActiveAttempts() error {
	if s.telemetry == nil || s.telemetry.store == nil {
		return errors.New("daemon: statistics unavailable")
	}
	rows, err := s.stateDB.Queries().ListActiveAttempts(context.Background())
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return s.stateDB.WriteTx(context.Background(), func(tx *sql.Tx) error {
		q := db.New(tx)
		events := make([]stats.Event, 0, len(rows))
		for _, row := range rows {
			var e stats.Event
			if err := json.Unmarshal(row.Payload, &e); err != nil {
				return err
			}
			events = append(events, e)
			if err := q.DeleteActiveAttempt(context.Background(), row.ID); err != nil {
				return err
			}
		}
		return s.telemetry.store.RecordBatchTx(context.Background(), tx, events)
	})
}
