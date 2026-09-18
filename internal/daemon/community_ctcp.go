package daemon

import (
	"runtime/debug"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

const communityCTCPVersionRequest = "\x01VERSION\x01"

type communityCTCPState struct {
	busy  bool
	last  time.Time
	users map[string]time.Time
}

func (c *communityCTCPState) reserve(username string, now time.Time) bool {
	if c.busy || now.Sub(c.last) < 10*time.Second {
		return false
	}
	for user, at := range c.users {
		if now.Sub(at) >= time.Minute {
			delete(c.users, user)
		}
	}
	if _, ok := c.users[username]; ok || len(c.users) >= 200 {
		return false
	}
	if c.users == nil {
		c.users = map[string]time.Time{}
	}
	c.users[username] = now
	c.last = now
	c.busy = true
	return true
}
func communityClientVersion() string {
	version := ""
	if info, ok := debug.ReadBuildInfo(); ok && len(info.Main.Version) <= 64 {
		version = info.Main.Version
	}
	return strings.TrimSpace("VERSION: oto " + version)
}
func (s *Service) allowCTCPReplyLocked(id CommunityIdentity, m soulseek.PrivateMessage) bool {
	if s.closed || s.shuttingDown || !s.communityCurrentLocked(id) || s.community.text == nil || !s.community.text.settings.CTCPVersion || !m.New || m.Text != communityCTCPVersionRequest || strings.EqualFold(m.Username, "server") || m.Username == s.cfg.Soulseek.Username {
		return false
	}
	ignored, held := s.communityIgnoreLocked(m.Username)
	return !ignored && !held
}

// Called only after the incoming receipt commits. Replies are best-effort,
// online-only and never enter an offline outbox or automatic retry loop.
func (s *Service) queueCTCPReplyLocked(id CommunityIdentity, m soulseek.PrivateMessage) {
	if !s.allowCTCPReplyLocked(id, m) || s.client == nil || s.ctx == nil {
		return
	}
	if s.community.ctcp == nil {
		s.community.ctcp = &communityCTCPState{}
	}
	state := s.community.ctcp
	if !state.reserve(m.Username, time.Now()) {
		return
	}
	s.sendCommunityAutomaticLocked(id, m.Username, communityClientVersion(), func() bool { return s.allowCTCPReplyLocked(id, m) }, func() { state.busy = false })
}
