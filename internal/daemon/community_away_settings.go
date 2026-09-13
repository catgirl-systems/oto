package daemon

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
)

type CommunityAwaySettings struct {
	CommunityIdentity
	Settings      config.CommunityAway `json:"settings"`
	AutomaticAway bool                 `json:"automatic_away"`
	LastActivity  time.Time            `json:"last_activity"`
}
type CommunityAwaySettingsRequest struct {
	CommunityIdentity
	Expected config.CommunityAway `json:"expected"`
	Settings config.CommunityAway `json:"settings"`
}

func (s *Service) communityAwaySettingsLocked() CommunityAwaySettings {
	return CommunityAwaySettings{CommunityIdentity: s.community.identity, Settings: s.cfg.CommunityAway[s.community.identity.Account], AutomaticAway: s.community.away.automatic, LastActivity: s.community.away.lastActivity}
}
func (s *Service) CommunityAwaySettings(ctx context.Context, id CommunityIdentity) (CommunityAwaySettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, id); err != nil {
		return CommunityAwaySettings{}, err
	}
	return s.communityAwaySettingsLocked(), nil
}
func (s *Service) SetCommunityAwaySettings(ctx context.Context, req CommunityAwaySettingsRequest) (CommunityAwaySettings, error) {
	if req.Settings.AutoAwaySeconds < 0 || req.Settings.AutoAwaySeconds > 86400 {
		return CommunityAwaySettings{}, errors.New("auto-away seconds must be 0 (off) through 86400")
	}
	if err := validateAwayReply(req.Settings.AutoReply); err != nil {
		return CommunityAwaySettings{}, err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityAwaySettings{}, err
	}
	current := s.communityAwaySettingsLocked()
	if current.Settings == req.Settings {
		return current, nil
	}
	if current.Settings != req.Expected {
		return current, ErrCommunityMessageState
	}
	next := s.cfg
	next.CommunityAway = maps.Clone(next.CommunityAway)
	if next.CommunityAway == nil {
		next.CommunityAway = map[string]config.CommunityAway{}
	}
	next.CommunityAway[req.Account] = req.Settings
	if err := next.Save(s.configPath); err != nil {
		return current, err
	}
	s.cfg = next
	s.noteCommunityActivityLocked(time.Now())
	s.community.revision++
	return s.communityAwaySettingsLocked(), nil
}
