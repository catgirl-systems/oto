package daemon

import (
	"errors"
	"strings"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

type communityAwayReplies struct {
	users  map[string]bool
	active int
}

func validateAwayReply(text string) error {
	if len(text) > 1024 {
		return errors.New("away reply exceeds 1024 bytes")
	}
	if text != "" {
		_, err := communityOutgoingText("[Automatic Message] " + text)
		return err
	}
	return nil
}
func (s *Service) allowAwayReplyLocked(id CommunityIdentity, m soulseek.PrivateMessage) bool {
	if s.closed || s.shuttingDown || !s.communityCurrentLocked(id) || s.presence != PresenceAway || !m.New || strings.EqualFold(m.Username, "server") || m.Username == s.cfg.Soulseek.Username || strings.Contains(m.Text, "\x01") {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(m.Text))
	if strings.HasPrefix(text, "[automatic message]") || strings.HasPrefix(text, "[auto-message]") || strings.HasPrefix(text, "version:") || strings.HasPrefix(text, "errmsg ") {
		return false
	}
	ignored, held := s.communityIgnoreLocked(m.Username)
	return !ignored && !held
}
func (s *Service) queueAwayReplyLocked(id CommunityIdentity, m soulseek.PrivateMessage) {
	if !s.allowAwayReplyLocked(id, m) || s.client == nil || s.ctx == nil {
		return
	}
	raw := s.cfg.CommunityAway[id.Account].AutoReply
	if strings.TrimSpace(raw) == "" {
		return
	}
	policy := s.community.text
	text, err := policy.outgoing(raw)
	if err != nil {
		return
	}
	text, err = communityOutgoingText("[Automatic Message] " + text)
	if err != nil {
		return
	}
	if s.community.away.replies == nil {
		s.community.away.replies = &communityAwayReplies{users: map[string]bool{}}
	}
	period := s.community.away.replies
	// Never evict within an away period: eviction could send a second response.
	if period.users[m.Username] || len(period.users) >= 4096 || period.active >= 4 {
		return
	}
	period.users[m.Username] = true
	period.active++
	s.sendCommunityAutomaticLocked(id, m.Username, text, func() bool {
		return s.community.away.replies == period && s.community.text == policy && s.cfg.CommunityAway[id.Account].AutoReply == raw && s.allowAwayReplyLocked(id, m)
	}, func() { period.active-- })
}
