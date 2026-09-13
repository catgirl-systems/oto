package daemon

import (
	"context"
	"time"
)

type communityAwayState struct {
	replies      *communityAwayReplies
	lastActivity time.Time
	automatic    bool
	wake         chan struct{}
}
type CommunityActivity struct {
	CommunityIdentity
	AutomaticAway bool      `json:"automatic_away"`
	LastActivity  time.Time `json:"last_activity"`
}

func (s *Service) noteCommunityActivityLocked(now time.Time) {
	s.community.away.lastActivity = now
	if s.community.away.wake != nil {
		select {
		case s.community.away.wake <- struct{}{}:
		default:
		}
	}
}
func (s *Service) CommunityActivity(ctx context.Context, id CommunityIdentity) (CommunityActivity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, id); err != nil {
		return CommunityActivity{}, err
	}
	s.noteCommunityActivityLocked(time.Now())
	return CommunityActivity{CommunityIdentity: id, AutomaticAway: s.community.away.automatic, LastActivity: s.community.away.lastActivity}, nil
}
func (s *Service) autoAwayTargetLocked(now time.Time) (Presence, bool) {
	a := &s.community.away
	if a.lastActivity.IsZero() {
		a.lastActivity = now
	}
	seconds := s.cfg.CommunityAway[accountKey(s.cfg)].AutoAwaySeconds
	idle := seconds > 0 && now.Sub(a.lastActivity) >= time.Duration(seconds)*time.Second
	if s.presence == PresenceOnline && idle {
		return PresenceAway, true
	}
	if s.presence == PresenceAway && a.automatic && !idle {
		return PresenceOnline, true
	}
	return s.presence, false
}
func (s *Service) applyAutoAway(ctx context.Context, now time.Time) {
	// Shutdown waits for workers while holding lifecycleMu. Never block this
	// worker on that mutex; another tick/activity wake can retry a busy lifecycle.
	if !s.lifecycleMu.TryLock() {
		return
	}
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if ctx.Err() != nil || s.closed || s.shuttingDown {
		s.mu.Unlock()
		return
	}
	target, change := s.autoAwayTargetLocked(now)
	old := s.community.away.automatic
	if change {
		s.community.away.automatic = target == PresenceAway
	}
	s.mu.Unlock()
	if change {
		if err := s.setPresenceLocked(target); err != nil {
			s.mu.Lock()
			s.community.away.automatic = old
			s.mu.Unlock()
		}
	}
}
func (s *Service) awayLoop(ctx context.Context) {
	defer s.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		s.mu.RLock()
		wake := s.community.away.wake
		s.mu.RUnlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-wake:
		}
		s.applyAutoAway(ctx, time.Now())
	}
}
