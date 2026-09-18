package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
)

type CommunityTextSettings struct {
	CommunityIdentity
	Revision string             `json:"revision"`
	Settings CommunityTextTools `json:"settings"`
}

func communityTextSettings(id CommunityIdentity, p *communityTextPolicy) CommunityTextSettings {
	out := CommunityTextSettings{CommunityIdentity: id}
	if p != nil {
		out.Settings = p.settings
	}
	out.Settings.Keywords = slices.Clone(out.Settings.Keywords)
	out.Settings.Censorship = slices.Clone(out.Settings.Censorship)
	out.Settings.Substitutions = slices.Clone(out.Settings.Substitutions)
	encoded, _ := json.Marshal(out.Settings)
	hash := sha256.Sum256(encoded)
	out.Revision = hex.EncodeToString(hash[:])
	return out
}
func (s *Service) CommunityTextSettings(ctx context.Context, id CommunityIdentity) (CommunityTextSettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, id); err != nil {
		return CommunityTextSettings{}, err
	}
	return communityTextSettings(id, s.community.text), nil
}
func (s *Service) SetCommunityTextSettings(ctx context.Context, req CommunityTextSettings) (CommunityTextSettings, error) {
	p, err := compileCommunityTextTools(req.Settings)
	if err != nil {
		return CommunityTextSettings{}, err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityTextSettings{}, err
	}
	current := communityTextSettings(req.CommunityIdentity, s.community.text)
	out := communityTextSettings(req.CommunityIdentity, p)
	if current.Revision == out.Revision {
		return out, nil
	}
	if req.Revision != current.Revision {
		return current, ErrCommunityMessageState
	}
	next := s.cfg
	next.CommunityText = maps.Clone(next.CommunityText)
	if next.CommunityText == nil {
		next.CommunityText = map[string]CommunityTextTools{}
	}
	next.CommunityText[req.Account] = p.settings
	if err := next.Save(s.configPath); err != nil {
		return current, err
	}
	s.cfg = next
	s.community.text = p
	s.community.revision++
	return out, nil
}
