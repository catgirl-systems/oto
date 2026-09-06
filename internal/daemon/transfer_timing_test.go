package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestTransferTiming(t *testing.T) {
	now := time.Unix(1000, 0)
	tr := Transfer{ID: "d-1", State: "running", Done: 100, Total: 401}
	var clock transferTiming
	check := func(at time.Time, elapsed *uint64, speed uint64, eta *uint64) {
		t.Helper()
		x := clock.snapshot(tr, at)
		equal := func(a, b *uint64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
		if !equal(x.ElapsedMS, elapsed) || x.SpeedBPS != speed || !equal(x.ETASeconds, eta) {
			t.Fatalf("timing at %v: %+v, elapsed=%v eta=%v", at.Sub(now), x, x.ElapsedMS, x.ETASeconds)
		}
	}
	ptr := func(n uint64) *uint64 { return &n }
	check(now, nil, 0, nil) // setup is not streaming
	clock.start(now, 100)
	clock.progress(now.Add(500*time.Millisecond), 150)
	tr.Done = 150
	check(now.Add(500*time.Millisecond), ptr(500), 0, nil)
	clock.progress(now.Add(time.Second), 200)
	tr.Done = 200
	check(now.Add(time.Second), ptr(1000), 100, ptr(3))
	check(now.Add(time.Second), ptr(1000), 100, ptr(3)) // readers do not sample
	check(now.Add(4*time.Second), ptr(4000), 0, nil)    // stalled stream still accumulates time
	clock.stop(now.Add(4 * time.Second))
	tr.State = "paused"
	check(now.Add(time.Hour), ptr(4000), 0, nil)
	clock.start(now.Add(time.Hour), 200)
	tr.State, tr.Done = "running", 300
	clock.progress(now.Add(time.Hour+time.Second), 300)
	check(now.Add(time.Hour+time.Second), ptr(5000), 100, ptr(2))
	clock.stop(now.Add(time.Hour + time.Second))
	tr.State = "finalizing"
	check(now.Add(2*time.Hour), ptr(5000), 0, nil)
	tr.State, tr.Done = "completed", 401
	check(now.Add(2*time.Hour), ptr(5000), 0, ptr(0))
	clock = transferTiming{} // a daemon restart cannot reconstruct elapsed
	check(now, nil, 0, ptr(0))
	tr.Total, tr.Done = 0, 0
	check(now, nil, 0, ptr(0))
	encoded, err := json.Marshal(clock.snapshot(tr, now))
	if err != nil || !strings.Contains(string(encoded), `"elapsed_ms":null`) || !strings.Contains(string(encoded), `"eta_seconds":0`) {
		t.Fatalf("nullable JSON: %s %v", encoded, err)
	}
}

func TestTimingLifecycleGuards(t *testing.T) {
	s := downloadService(t)
	s.uploadEpoch = 1
	s.transfers["d-1"] = Transfer{ID: "d-1", State: "queued", Direction: "download", Total: 100}
	s.startTransfer("d-1", 40) // remote queued -> actual stream
	s.updateTransferProgress("d-1", soulseek.Progress{State: "queued", Done: 0, Total: 100, Queue: 7})
	if tr := s.transfers["d-1"]; tr.State != "running" || tr.Done != 40 || tr.Queue != 0 {
		t.Fatalf("late queue progress rewound stream: %+v", tr)
	}
	event := soulseek.TransferEvent{Direction: "upload", Username: "peer", Filename: "song", Attempt: 1, State: "queued", Total: 100}
	s.uploadUpdate(1, event)
	event.State = "running"
	s.uploadUpdate(1, event)
	id := s.uploadEventIDLocked(event)
	if s.transferTiming[id].known {
		t.Fatal("upload setup started timing")
	}
	bad := event
	bad.Attempt++
	s.uploadStreamStart(1, bad)
	if s.transferTiming[id].known {
		t.Fatal("stale stream start accepted")
	}
	event.Done = 30
	s.uploadStreamStart(1, event)
	if !s.transferTiming[id].known || s.transferTiming[id].sampleDone != 30 {
		t.Fatal("upload stream offset not recorded")
	}
	event.State = "failed"
	s.uploadUpdate(1, event)
	prior := s.transferTiming[id].elapsed
	event.Attempt++
	event.State = "queued"
	s.uploadUpdate(1, event)
	if s.transferTiming[id].elapsed != prior || !s.transferTiming[id].known {
		t.Fatal("retry lost timing")
	}
	event.State = "completed"
	s.uploadUpdate(1, event)
	event.Attempt++
	event.State = "queued"
	s.uploadUpdate(1, event)
	if next := s.uploadEventIDLocked(event); next == id || s.transferTiming[next].known {
		t.Fatal("new successful-file request reused old timing")
	}
}

func TestTransferRetryCountdownIsReadOnly(t *testing.T) {
	now := time.Unix(1000, 0)
	original := Transfer{ID: "d-1", Direction: "download", State: "retrying", Done: 100, Total: 200, Error: "remote upload failed"}
	download := Download{ID: original.ID, State: original.State, Offset: original.Done, RetryAt: now.Add(65*time.Second + time.Nanosecond), Error: original.Error}
	s := &Service{transfers: map[string]Transfer{original.ID: original}, journal: Journal{Downloads: []Download{download}}}
	for _, tc := range []struct {
		at   time.Time
		want uint64
	}{
		{now, 66}, {now.Add(time.Nanosecond), 65},
		{download.RetryAt.Add(-time.Nanosecond), 1}, {download.RetryAt, 0},
		{download.RetryAt.Add(time.Hour), 0},
	} {
		got := s.transferValuesLocked(tc.at)[0]
		if got.RetryInSeconds == nil || *got.RetryInSeconds != tc.want || got.WaitingForPeerSeconds != nil || got.State != original.State || got.Error != original.Error || got.Done != original.Done {
			t.Fatalf("countdown at %v: %+v", tc.at, got)
		}
		if s.journal.Downloads[0] != download || s.transfers[original.ID] != original {
			t.Fatal("reading countdown changed retry policy/state")
		}
	}
	// Both IPC views use daemon time; an overdue attempt is pending, not running.
	for _, got := range []Transfer{s.Transfers()[0], s.Snapshot().Transfers[0]} {
		if got.RetryInSeconds == nil || *got.RetryInSeconds != 0 || got.State != "retrying" || got.Error != original.Error {
			t.Fatalf("public snapshot: %+v", got)
		}
	}
	for _, state := range []string{"queued", "running", "paused", "cancelled", "failed", "completed", "finalizing"} {
		x := original
		x.State = state
		s.transfers[x.ID] = x
		if got := s.transferValuesLocked(now)[0]; got.RetryInSeconds != nil {
			t.Fatalf("%s retained retry countdown", state)
		}
	}
	s.transfers[original.ID] = original
	s.journal.Downloads[0].RetryAt = time.Time{}
	if got := s.transferValuesLocked(now)[0]; got.RetryInSeconds != nil {
		t.Fatal("unknown deadline became a known countdown")
	}
	var old Transfer
	if err := json.Unmarshal([]byte(`{"id":"old","state":"retrying","error":"observed error"}`), &old); err != nil || old.RetryInSeconds != nil || old.WaitingForPeerSeconds != nil {
		t.Fatalf("old IPC payload: %+v, %v", old, err)
	}
}
