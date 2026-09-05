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
	s := &Service{transfers: map[string]Transfer{"d-1": {ID: "d-1", State: "queued", Direction: "download", Total: 100}}, uploadEpoch: 1}
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
