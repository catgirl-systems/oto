package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestAutoAwayActivityAndManualPrecedence(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	id := s.community.identity
	now := time.Now()
	s.mu.Lock()
	s.presence = PresenceOnline
	s.community.away.lastActivity = now.Add(-time.Hour)
	if _, change := s.autoAwayTargetLocked(now); change {
		t.Fatal("auto-away enabled by default")
	}
	s.cfg.CommunityAway = map[string]config.CommunityAway{id.Account: {AutoAwaySeconds: 60}}
	if target, change := s.autoAwayTargetLocked(now); !change || target != PresenceAway {
		t.Fatal("idle transition", target, change)
	}
	s.presence = PresenceAway
	s.community.away.automatic = true
	s.mu.Unlock()
	if _, err := s.CommunityActivity(ctx, id); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if target, change := s.autoAwayTargetLocked(time.Now()); !change || target != PresenceOnline {
		t.Fatal("activity did not clear automatic away")
	}
	s.community.away.automatic = false
	if _, change := s.autoAwayTargetLocked(time.Now()); change {
		t.Fatal("activity cleared manual away")
	}
	s.presence = PresenceOffline
	s.community.away.lastActivity = now.Add(-time.Hour)
	if _, change := s.autoAwayTargetLocked(now); change {
		t.Fatal("idle connected offline account")
	}
	before := s.community.away.lastActivity
	s.mu.Unlock()
	id.Session++
	if _, err := s.CommunityActivity(ctx, id); err == nil {
		t.Fatal("stale activity")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.community.away.lastActivity != before {
		t.Fatal("stale activity changed clock")
	}
}
func TestAwayWorkerDoesNotBlockShutdownLifecycle(t *testing.T) {
	s := downloadService(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.lifecycleMu.Lock()
	s.wg.Add(1)
	go s.awayLoop(ctx)
	cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker blocked on shutdown lifecycle")
	}
	s.lifecycleMu.Unlock()
}
func TestCommunityAwayConfigurationBounds(t *testing.T) {
	cfg := testConfig(t)
	for _, seconds := range []int{-1, 86401} {
		cfg.CommunityAway = map[string]config.CommunityAway{"account": {AutoAwaySeconds: seconds}}
		if cfg.Validate() == nil {
			t.Fatal("invalid duration", seconds)
		}
	}
}
