package daemon

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
)

type ShareAccessRequest struct {
	CommunityIdentity
	Expected config.Share `json:"expected"`
	Revision uint64       `json:"revision"`
	Access   string       `json:"access"`
	Reveal   bool         `json:"reveal"`
	Confirm  bool         `json:"confirm"`
}

type ShareAccessResult struct {
	CommunityIdentity
	Share    config.Share `json:"share"`
	Revision uint64       `json:"revision"`
	State    string       `json:"state"`
}

func (s *Service) SetShareAccess(ctx context.Context, req ShareAccessRequest) (ShareAccessResult, error) {
	out := ShareAccessResult{CommunityIdentity: req.CommunityIdentity}
	if !req.Confirm {
		return out, errors.New("community: confirm changing access to this share")
	}
	if req.Access != "public" && req.Access != "buddy" && req.Access != "trusted" {
		return out, errors.New("community: choose public, buddy or trusted access")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		s.mu.Unlock()
		return out, err
	}
	i := slices.IndexFunc(s.cfg.Shares, func(root config.Share) bool { return root.Name == req.Expected.Name })
	if i < 0 {
		s.mu.Unlock()
		return out, errors.New("community: share no longer exists; reload")
	}
	current := s.cfg.Shares[i]
	if current.Path == req.Expected.Path && current.Access == req.Access && current.Reveal == req.Reveal {
		out.Share, out.Revision, out.State = current, s.shareIndexRevision, "saved"
		s.mu.Unlock()
		return out, nil
	}
	if req.Revision != s.shareIndexRevision || current != req.Expected {
		s.mu.Unlock()
		return out, ErrCommunityMessageState
	}
	next := s.cfg
	next.Shares = slices.Clone(next.Shares)
	next.Shares[i].Access, next.Shares[i].Reveal = req.Access, req.Reveal
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return out, err
	}
	if err := next.Save(s.configPath); err != nil {
		s.mu.Unlock()
		return out, err
	}
	s.cfg = next
	s.shareIndexRevision++
	s.community.revision++
	out.Share, out.Revision, out.State = next.Shares[i], s.shareIndexRevision, "saved"
	client := s.client
	s.mu.Unlock()
	if client != nil {
		client.RevalidateSharePolicy()
		publishCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := client.AnnounceShareCounts(publishCtx); err != nil {
			out.State = "saved; public counts refresh on reconnect"
		}
	}
	return out, nil
}
