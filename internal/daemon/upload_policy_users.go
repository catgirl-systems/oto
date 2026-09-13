package daemon

import (
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

// Called under s.mu: the manager only consumes copied values, never daemon callbacks.
func (s *Service) applyUploadUserPoliciesLocked() {
	if s.client == nil {
		return
	}
	users := make(map[string]soulseek.UploadUserPolicy, len(s.community.buddies))
	if s.cfg.Uploads.PrioritizePrivileged {
		for username := range s.community.privileged {
			users[username] = soulseek.UploadUserPolicy{Preferred: true}
		}
	}
	for username, buddy := range s.community.buddies {
		policy := users[username]
		policy.Preferred = policy.Preferred || buddy.Priority || s.cfg.Uploads.PrioritizeBuddies
		policy.ExemptLimits = s.cfg.Uploads.ExemptBuddiesFromQueueLimits
		users[username] = policy
	}
	s.client.SetUploadUserPolicies(users)
}

func (s *Service) updateUploadPrivilegesLocked(message soulseek.SocialMessage) (bool, error) {
	updates := map[string]bool{}
	handled := true
	switch m := message.(type) {
	case soulseek.PrivilegedUsers:
		for _, username := range m.Users {
			updates[username] = true
		}
	case soulseek.ConnectPeerInstruction:
		updates[m.Username] = m.Privileged
	case soulseek.UserPresence:
		updates[m.Username] = m.Privileged
		handled = false
	default:
		return false, nil
	}
	added := 0
	for username, privileged := range updates {
		if err := soulseek.ValidateUsername(username); err != nil {
			return handled, err
		}
		if _, known := s.community.privileged[username]; privileged && !known {
			added++
		}
	}
	if len(s.community.privileged)+added > soulseek.MaxPrivilegedUsers {
		return handled, soulseek.ErrTooLarge
	}
	if s.community.privileged == nil {
		s.community.privileged = make(map[string]time.Time)
	}
	now := time.Now().UTC()
	changed := false
	for username, privileged := range updates {
		_, known := s.community.privileged[username]
		changed = changed || known != privileged
		if privileged {
			s.community.privileged[username] = now
		} else {
			delete(s.community.privileged, username)
		}
		if user, ok := s.community.users[username]; ok {
			user.Privileged, user.PrivilegeFresh, user.PrivilegeUpdatedAt = privileged, true, now
			s.community.users[username] = user
			s.community.revision++
		}
	}
	if changed && s.cfg.Uploads.PrioritizePrivileged {
		s.applyUploadUserPoliciesLocked()
	}
	return handled, nil
}
