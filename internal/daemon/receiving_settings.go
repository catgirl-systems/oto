package daemon

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"

	"github.com/catgirl-systems/oto/internal/config"
)

type ReceivingSettings struct {
	CommunityIdentity
	Settings           config.Receiving `json:"settings"`
	EffectiveDirectory string           `json:"effective_directory"`
}
type ReceivingSettingsRequest struct {
	CommunityIdentity
	Expected config.Receiving `json:"expected"`
	Settings config.Receiving `json:"settings"`
	Confirm  bool             `json:"confirm"`
}

func equalReceiving(a, b config.Receiving) bool {
	return a.Mode == b.Mode && a.Directory == b.Directory && a.CompletionHooks == b.CompletionHooks && slices.Equal(a.Users, b.Users)
}
func (s *Service) receivingSettingsLocked() ReceivingSettings {
	policy := s.cfg.Receiving[s.community.identity.Account]
	policy.Users = slices.Clone(policy.Users)
	directory := policy.Directory
	if directory == "" {
		directory = filepath.Join(s.cfg.DownloadDir, "received")
	}
	return ReceivingSettings{CommunityIdentity: s.community.identity, Settings: policy, EffectiveDirectory: directory}
}
func (s *Service) ReceivingSettings(ctx context.Context, id CommunityIdentity) (ReceivingSettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, id); err != nil {
		return ReceivingSettings{}, err
	}
	return s.receivingSettingsLocked(), nil
}
func (s *Service) SetReceivingSettings(ctx context.Context, req ReceivingSettingsRequest) (ReceivingSettings, error) {
	if err := req.Settings.Validate(); err != nil {
		return ReceivingSettings{}, err
	}
	req.Settings.Users = slices.Clone(req.Settings.Users)
	slices.Sort(req.Settings.Users)
	defer s.revalidateReceivedDownloads()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return ReceivingSettings{}, err
	}
	current := s.receivingSettingsLocked()
	if equalReceiving(current.Settings, req.Settings) {
		return current, nil
	}
	if !req.Confirm {
		return current, errors.New("explicit receiving-policy confirmation required")
	}
	if !equalReceiving(current.Settings, req.Expected) {
		return current, ErrCommunityMessageState
	}
	next := s.cfg
	next.Receiving = maps.Clone(next.Receiving)
	if next.Receiving == nil {
		next.Receiving = map[string]config.Receiving{}
	}
	next.Receiving[req.Account] = req.Settings
	if err := next.Save(s.configPath); err != nil {
		return current, err
	}
	s.cfg = next
	s.community.revision++
	return s.receivingSettingsLocked(), nil
}
