// Package stats keeps transfer logs and independent, durable statistical rollups.
package stats

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	KindQueued      = "queued"
	KindStarted     = "started"
	KindProgress    = "progress"
	KindCompleted   = "completed"
	KindFailed      = "failed"
	KindCancelled   = "cancelled"
	KindInterrupted = "interrupted"
	KindRetry       = "retry"
	KindResume      = "resume"
	KindFiltered    = "filtered"
	KindForced      = "forced"
	KindRejected    = "rejected"
)

var validKinds = map[string]bool{KindQueued: true, KindStarted: true, KindProgress: true, KindCompleted: true, KindFailed: true, KindCancelled: true, KindInterrupted: true, KindRetry: true, KindResume: true, KindFiltered: true, KindForced: true, KindRejected: true}

type Event struct {
	ID             string    `json:"id"`
	Account        string    `json:"account"`
	Session        string    `json:"session"`
	TransferID     string    `json:"transfer_id,omitempty"`
	Attempt        uint64    `json:"attempt,omitempty"`
	Peer           string    `json:"peer"`
	Direction      string    `json:"direction"`
	Kind           string    `json:"kind"`
	Filename       string    `json:"filename,omitempty"`
	Destination    string    `json:"destination,omitempty"`
	Error          string    `json:"error,omitempty"`
	At             time.Time `json:"at"`
	Bytes          uint64    `json:"bytes,omitempty"`
	CompletedBytes uint64    `json:"completed_bytes,omitempty"`
	FileComplete   bool      `json:"file_complete,omitempty"`
	ActiveMillis   uint64    `json:"active_millis,omitempty"`
	WaitMillis     uint64    `json:"wait_millis,omitempty"`
	Peak           uint64    `json:"peak,omitempty"`
}
type Filter struct {
	Sort      string    `json:"sort,omitempty"`
	Account   string    `json:"account,omitempty"`
	Peer      string    `json:"peer,omitempty"`
	Direction string    `json:"direction,omitempty"`
	Session   string    `json:"session,omitempty"`
	From      time.Time `json:"from,omitempty"`
	To        time.Time `json:"to,omitempty"`
	Kinds     []string  `json:"kinds,omitempty"`
	Cursor    string    `json:"cursor,omitempty"`
	Limit     int       `json:"limit,omitempty"`
	Bins      int       `json:"bins,omitempty"`
}
type Totals struct {
	Bytes               uint64    `json:"bytes"`
	CompletedFiles      uint64    `json:"completed_files"`
	CompletedBytes      uint64    `json:"completed_bytes"`
	AttemptsStarted     uint64    `json:"attempts_started"`
	AttemptsCompleted   uint64    `json:"attempts_completed"`
	AttemptsFailed      uint64    `json:"attempts_failed"`
	AttemptsCancelled   uint64    `json:"attempts_cancelled"`
	AttemptsInterrupted uint64    `json:"attempts_interrupted"`
	Retries             uint64    `json:"retries"`
	Resumes             uint64    `json:"resumes"`
	Filtered            uint64    `json:"filtered"`
	Forced              uint64    `json:"forced"`
	Rejected            uint64    `json:"rejected"`
	ActiveMillis        uint64    `json:"active_millis"`
	WaitMillis          uint64    `json:"wait_millis"`
	Peak                uint64    `json:"peak"`
	UniquePeers         uint64    `json:"unique_peers"`
	First               time.Time `json:"first,omitempty"`
	Last                time.Time `json:"last,omitempty"`
}
type PeerStats struct {
	Peer string `json:"peer"`
	Totals
}
type Daily struct {
	Day string `json:"day"`
	Totals
}
type LogPage struct {
	Entries    []Event `json:"entries"`
	NextCursor string  `json:"next_cursor,omitempty"`
}
type PeerPage struct {
	Peers      []PeerStats `json:"peers"`
	NextCursor string      `json:"next_cursor,omitempty"`
}
type PruneResult struct {
	Logs  int64 `json:"logs"`
	Daily int64 `json:"daily"`
}
type Store struct{ db *sql.DB }

var ErrInvalidCursor = errors.New("stats: invalid or expired cursor")

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("stats: file path required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = file.Chmod(0600)
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }
	for _, query := range []string{
		"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL",
		`CREATE TABLE IF NOT EXISTS seen(id TEXT PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS events(id TEXT PRIMARY KEY,account TEXT,peer TEXT,direction TEXT,session TEXT,kind TEXT,at INTEGER,data TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS event_account ON events(account,at DESC,id DESC)`,
		`CREATE INDEX IF NOT EXISTS event_peer ON events(account,peer,at DESC,id DESC)`,
		`CREATE TABLE IF NOT EXISTS totals(account TEXT,peer TEXT,direction TEXT,session TEXT,day TEXT,data TEXT NOT NULL,PRIMARY KEY(account,peer,direction,session,day))`,
		`CREATE INDEX IF NOT EXISTS totals_day ON totals(account,day)`,
	} {
		if _, err = db.Exec(query); err != nil {
			return fail(err)
		}
	}
	for _, name := range []string{absolute, absolute + "-wal", absolute + "-shm"} {
		if err = os.Chmod(name, 0600); err != nil && !os.IsNotExist(err) {
			return fail(err)
		}
	}
	return &Store{db}, nil
}
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

func metric(e Event) Totals {
	t := Totals{Bytes: e.Bytes, CompletedBytes: e.CompletedBytes, ActiveMillis: e.ActiveMillis, WaitMillis: e.WaitMillis, Peak: e.Peak, First: e.At, Last: e.At}
	switch e.Kind {
	case KindStarted:
		t.AttemptsStarted = 1
	case KindCompleted:
		if e.FileComplete {
			t.CompletedFiles = 1
		}
		if e.Attempt != 0 {
			t.AttemptsCompleted = 1
		}
	case KindFailed:
		if e.Attempt != 0 {
			t.AttemptsFailed = 1
		}
	case KindCancelled:
		if e.Attempt != 0 {
			t.AttemptsCancelled = 1
		}
	case KindInterrupted:
		if e.Attempt != 0 {
			t.AttemptsInterrupted = 1
		}
	case KindRetry:
		t.Retries = 1
	case KindResume:
		t.Resumes = 1
	case KindFiltered:
		t.Filtered = 1
	case KindForced:
		t.Forced = 1
	case KindRejected:
		t.Rejected = 1
	}
	return t
}
func merge(dst *Totals, src Totals) error {
	targets := []*uint64{&dst.Bytes, &dst.CompletedFiles, &dst.CompletedBytes, &dst.AttemptsStarted, &dst.AttemptsCompleted, &dst.AttemptsFailed, &dst.AttemptsCancelled, &dst.AttemptsInterrupted, &dst.Retries, &dst.Resumes, &dst.Filtered, &dst.Forced, &dst.Rejected, &dst.ActiveMillis, &dst.WaitMillis}
	values := []uint64{src.Bytes, src.CompletedFiles, src.CompletedBytes, src.AttemptsStarted, src.AttemptsCompleted, src.AttemptsFailed, src.AttemptsCancelled, src.AttemptsInterrupted, src.Retries, src.Resumes, src.Filtered, src.Forced, src.Rejected, src.ActiveMillis, src.WaitMillis}
	for i, p := range targets {
		if ^uint64(0)-*p < values[i] {
			return errors.New("stats: counter overflow")
		}
		*p += values[i]
	}
	dst.Peak = max(dst.Peak, src.Peak)
	if !src.First.IsZero() && (dst.First.IsZero() || src.First.Before(dst.First)) {
		dst.First = src.First
	}
	if src.Last.After(dst.Last) {
		dst.Last = src.Last
	}
	return nil
}

func (s *Store) Record(e Event) error { return s.RecordBatch([]Event{e}) }
func (s *Store) RecordBatch(events []Event) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		if e.ID == "" || e.Account == "" || e.Session == "" || !validKinds[e.Kind] || (e.Direction != "upload" && e.Direction != "download") || e.At.IsZero() || e.At.Year() < 1970 || e.At.Year() > 2261 {
			return errors.New("stats: invalid event")
		}
		e.At = e.At.UTC()
		result, err := tx.Exec(`INSERT OR IGNORE INTO seen VALUES(?)`, e.ID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			continue
		}
		data, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO events VALUES(?,?,?,?,?,?,?,?)`, e.ID, e.Account, e.Peer, e.Direction, e.Session, e.Kind, e.At.UnixNano(), string(data)); err != nil {
			return err
		}
		for _, day := range []string{"", e.At.Format("2006-01-02")} {
			var prior string
			t := Totals{}
			err = tx.QueryRow(`SELECT data FROM totals WHERE account=? AND peer=? AND direction=? AND session=? AND day=?`, e.Account, e.Peer, e.Direction, e.Session, day).Scan(&prior)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				if err = json.Unmarshal([]byte(prior), &t); err != nil {
					return err
				}
			}
			if err = merge(&t, metric(e)); err != nil {
				return err
			}
			data, err = json.Marshal(t)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(`INSERT INTO totals VALUES(?,?,?,?,?,?) ON CONFLICT(account,peer,direction,session,day) DO UPDATE SET data=excluded.data`, e.Account, e.Peer, e.Direction, e.Session, day, string(data)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func ValidateFilter(f Filter) error {
	if len(f.Cursor) > 4096 {
		return ErrInvalidCursor
	}
	if f.Sort != "" && f.Sort != "peer" && f.Sort != "bytes" {
		return errors.New("stats: invalid peer sort")
	}
	for _, at := range []time.Time{f.From, f.To} {
		if !at.IsZero() && (at.Year() < 1970 || at.Year() > 2261) {
			return errors.New("stats: date outside supported range")
		}
	}
	if f.Limit < 0 || f.Limit > 1000 || f.Bins < 0 || f.Bins > 400 {
		return errors.New("stats: query limit out of range")
	}
	if f.Direction != "" && f.Direction != "upload" && f.Direction != "download" {
		return errors.New("stats: invalid direction")
	}
	if !f.To.IsZero() && !f.From.IsZero() && !f.From.Before(f.To) {
		return errors.New("stats: invalid date range")
	}
	for _, kind := range f.Kinds {
		if !validKinds[kind] {
			return errors.New("stats: invalid outcome")
		}
	}
	return nil
}
func where(f Filter) (string, []any) {
	clauses := []string{"1=1"}
	args := []any{}
	for _, pair := range [][2]string{{"account", f.Account}, {"peer", f.Peer}, {"direction", f.Direction}, {"session", f.Session}} {
		if pair[1] != "" {
			clauses = append(clauses, pair[0]+"=?")
			args = append(args, pair[1])
		}
	}
	return strings.Join(clauses, " AND "), args
}

type row struct {
	peer, day string
	totals    Totals
}

func (s *Store) rows(f Filter, daily bool) iter.Seq2[row, error] {
	return func(yield func(row, error) bool) {
		if err := ValidateFilter(f); err != nil {
			yield(row{}, err)
			return
		}
		condition, args := where(f)
		if daily {
			condition += " AND day<>''"
			if !f.From.IsZero() {
				condition += " AND day>=?"
				args = append(args, f.From.UTC().Format(time.DateOnly))
			}
			if !f.To.IsZero() {
				condition += " AND day<?"
				args = append(args, f.To.UTC().Format(time.DateOnly))
			}
		} else {
			condition += " AND day=''"
		}
		rs, err := s.db.Query(`SELECT peer,day,data FROM totals WHERE `+condition, args...)
		if err != nil {
			yield(row{}, err)
			return
		}
		defer rs.Close()
		for rs.Next() {
			r := row{}
			var data string
			if err = rs.Scan(&r.peer, &r.day, &data); err != nil {
				yield(row{}, err)
				return
			}
			if err = json.Unmarshal([]byte(data), &r.totals); err != nil {
				yield(row{}, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err = rs.Err(); err != nil {
			yield(row{}, err)
		}
	}
}
func (s *Store) Totals(f Filter) (Totals, error) {
	out := Totals{}
	peers := map[string]bool{}
	for r, err := range s.rows(f, !f.From.IsZero() || !f.To.IsZero()) {
		if err != nil {
			return Totals{}, err
		}
		if err = merge(&out, r.totals); err != nil {
			return Totals{}, err
		}
		if r.peer != "" {
			peers[r.peer] = true
		}
	}
	out.UniquePeers = uint64(len(peers))
	return out, nil
}

// ponytail: merge compact per-peer/session rollups in Go; use numeric SQL columns
// if millions of distinct peer/session combinations make aggregate queries costly.
func (s *Store) Peers(f Filter) (PeerPage, error) {
	grouped := map[string]Totals{}
	for r, err := range s.rows(f, !f.From.IsZero() || !f.To.IsZero()) {
		if err != nil {
			return PeerPage{}, err
		}
		t := grouped[r.peer]
		if err = merge(&t, r.totals); err != nil {
			return PeerPage{}, err
		}
		grouped[r.peer] = t
	}
	keys := []string{}
	for name := range grouped {
		if name != "" {
			keys = append(keys, name)
		}
	}
	slices.SortFunc(keys, func(a, b string) int {
		if f.Sort == "bytes" {
			if grouped[a].Bytes > grouped[b].Bytes {
				return -1
			}
			if grouped[a].Bytes < grouped[b].Bytes {
				return 1
			}
		}
		return strings.Compare(a, b)
	})
	if f.Cursor != "" {
		start := slices.Index(keys, f.Cursor)
		if start < 0 {
			return PeerPage{}, ErrInvalidCursor
		}
		keys = keys[start+1:]
	}
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	out := PeerPage{Peers: []PeerStats{}}
	if len(keys) > limit {
		out.NextCursor = keys[limit-1]
		keys = keys[:limit]
	}
	for _, key := range keys {
		out.Peers = append(out.Peers, PeerStats{key, grouped[key]})
	}
	return out, nil
}
func (s *Store) Series(f Filter) ([]Daily, error) {
	out := []Daily{}
	daily := map[string]Totals{}
	first, last := "", ""
	for r, err := range s.rows(f, true) {
		if err != nil {
			return nil, err
		}
		t := daily[r.day]
		if err = merge(&t, r.totals); err != nil {
			return nil, err
		}
		daily[r.day] = t
		if first == "" || r.day < first {
			first = r.day
		}
		last = max(last, r.day)
	}
	if len(daily) == 0 {
		return out, nil
	}
	start, _ := time.Parse(time.DateOnly, first)
	end, _ := time.Parse(time.DateOnly, last)
	if !f.From.IsZero() && f.From.After(start) {
		start = f.From.UTC().Truncate(24 * time.Hour)
	}
	if !f.To.IsZero() {
		end = f.To.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	}
	bins := f.Bins
	if bins == 0 {
		bins = 100
	}
	days := int(end.Sub(start)/(24*time.Hour)) + 1
	if days < 1 {
		return out, nil
	}
	width := (days + bins - 1) / bins
	grouped := map[int]Totals{}
	for day, totals := range daily {
		at, _ := time.Parse(time.DateOnly, day)
		n := int(at.Sub(start)/(24*time.Hour)) / width
		t := grouped[n]
		if err := merge(&t, totals); err != nil {
			return nil, err
		}
		grouped[n] = t
	}
	for day := 0; day < days; day += width {
		out = append(out, Daily{start.AddDate(0, 0, day).Format(time.DateOnly), grouped[day/width]})
	}
	return out, nil
}
func (s *Store) Accounts() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT account FROM totals ORDER BY account`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
func (s *Store) Log(f Filter) (LogPage, error) {
	if err := ValidateFilter(f); err != nil {
		return LogPage{}, err
	}
	condition, args := where(f)
	if !f.From.IsZero() {
		condition += " AND at>=?"
		args = append(args, f.From.UnixNano())
	}
	if !f.To.IsZero() {
		condition += " AND at<?"
		args = append(args, f.To.UnixNano())
	}
	if len(f.Kinds) > 0 {
		condition += " AND kind IN (" + strings.TrimSuffix(strings.Repeat("?,", len(f.Kinds)), ",") + ")"
		for _, kind := range f.Kinds {
			args = append(args, kind)
		}
	}
	if f.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		if err != nil {
			return LogPage{}, ErrInvalidCursor
		}
		parts := strings.SplitN(string(raw), "\x00", 2)
		if len(parts) != 2 {
			return LogPage{}, ErrInvalidCursor
		}
		at, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || at < 0 || time.Unix(0, at).UTC().Year() > 2261 || parts[1] == "" {
			return LogPage{}, ErrInvalidCursor
		}
		condition += " AND (at<? OR (at=? AND id<?))"
		args = append(args, at, at, parts[1])
	}
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(`SELECT data FROM events WHERE `+condition+` ORDER BY at DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		return LogPage{}, err
	}
	defer rows.Close()
	out := LogPage{Entries: []Event{}}
	for rows.Next() {
		var data string
		var e Event
		if err = rows.Scan(&data); err != nil {
			return LogPage{}, err
		}
		if err = json.Unmarshal([]byte(data), &e); err != nil {
			return LogPage{}, err
		}
		out.Entries = append(out.Entries, e)
	}
	if err = rows.Err(); err != nil {
		return LogPage{}, err
	}
	if len(out.Entries) > limit {
		out.Entries = out.Entries[:limit]
		last := out.Entries[limit-1]
		out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d\x00%s", last.At.UnixNano(), last.ID)))
	}
	return out, nil
}
func (s *Store) Prune(cutoff time.Time, logs, daily, apply bool) (PruneResult, error) {
	if cutoff.Year() < 1970 || cutoff.Year() > 2261 {
		return PruneResult{}, errors.New("stats: cutoff outside supported range")
	}
	if cutoff.IsZero() || cutoff.After(time.Now()) || (!logs && !daily) {
		return PruneResult{}, errors.New("stats: select data and a past cutoff")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return PruneResult{}, err
	}
	defer tx.Rollback()
	out := PruneResult{}
	if logs {
		if err = tx.QueryRow(`SELECT count(*) FROM events WHERE at<?`, cutoff.UnixNano()).Scan(&out.Logs); err != nil {
			return out, err
		}
		if apply {
			if _, err = tx.Exec(`DELETE FROM events WHERE at<?`, cutoff.UnixNano()); err != nil {
				return out, err
			}
		}
	}
	if daily {
		if err = tx.QueryRow(`SELECT count(*) FROM totals WHERE day<>'' AND day<?`, cutoff.UTC().Format(time.DateOnly)).Scan(&out.Daily); err != nil {
			return out, err
		}
		if apply {
			if _, err = tx.Exec(`DELETE FROM totals WHERE day<>'' AND day<?`, cutoff.UTC().Format(time.DateOnly)); err != nil {
				return out, err
			}
		}
	}
	return out, tx.Commit()
}
