package daemon

import (
	"context"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

type AccountPrivilegesRequest struct {
	CommunityIdentity
	Refresh bool `json:"refresh"`
}
type AccountPrivileges struct {
	CommunityIdentity
	Revision  uint64    `json:"revision"`
	Seconds   uint32    `json:"seconds"`
	UpdatedAt time.Time `json:"updated_at"`
	Known     bool      `json:"known"`
	Fresh     bool      `json:"fresh"`
	Pending   bool      `json:"pending"`
	Error     string    `json:"error,omitempty"`
}
type communityPrivilegeState struct {
	seconds                              uint32
	updated                              time.Time
	revision                             uint64
	fresh, pending, poisoned, giftActive bool
	err                                  string
	queryDone                            chan struct{}
	queryCancel                          context.CancelFunc
}

func (s *Service) accountPrivilegesLocked() AccountPrivileges {
	p := &s.community.privileges
	out := AccountPrivileges{CommunityIdentity: s.community.identity, Revision: p.revision, Seconds: p.seconds, UpdatedAt: p.updated, Known: !p.updated.IsZero(), Pending: p.pending || p.giftActive, Error: p.err}
	age := time.Since(p.updated)
	out.Fresh = p.fresh && s.community.online && !out.Pending && age < time.Minute
	if out.Known && age > 0 {
		elapsed := uint64(age / time.Second)
		if elapsed >= uint64(out.Seconds) {
			out.Seconds = 0
		} else {
			out.Seconds -= uint32(elapsed)
		}
	}
	if !s.community.online {
		out.Error = "Offline; reconnect to refresh privileges"
	}
	return out
}

// Balance responses have no token. A timed-out query fences further queries until
// reconnect, rather than allowing its late response to authorize a later gift.
func (s *Service) AccountPrivileges(ctx context.Context, req AccountPrivilegesRequest) (AccountPrivileges, error) {
	s.mu.Lock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		s.mu.Unlock()
		return AccountPrivileges{}, err
	}
	p := &s.community.privileges
	out := s.accountPrivilegesLocked()
	if !s.community.online || s.client == nil || p.poisoned || p.giftActive || (!req.Refresh && out.Fresh) {
		s.mu.Unlock()
		return out, nil
	}
	if !p.pending {
		p.pending = true
		p.fresh = false
		p.err = ""
		p.revision++
		done := make(chan struct{})
		p.queryDone = done
		queryCtx, cancel := context.WithTimeout(s.scanCtx, 5*time.Second)
		p.queryCancel = cancel
		client, identity := s.client, s.community.identity
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer cancel()
			err := client.RequestPrivilegeBalance(queryCtx)
			if err == nil {
				select {
				case <-done:
					return
				case <-queryCtx.Done():
					err = queryCtx.Err()
				}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			p := &s.community.privileges
			if p.queryDone != done {
				return
			}
			p.queryDone = nil
			p.queryCancel = nil
			p.pending = false
			close(done)
			if s.communityCurrentLocked(identity) && s.client == client {
				p.poisoned = true
				p.fresh = false
				p.err = "Privilege query failed; reconnect before retrying: " + err.Error()
				p.revision++
			}
		}()
	}
	done := p.queryDone
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return AccountPrivileges{}, ctx.Err()
	case <-done:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return AccountPrivileges{}, err
	}
	return s.accountPrivilegesLocked(), nil
}

func (s *Service) acceptCommunityPrivilegeBalanceLocked(identity CommunityIdentity, balance soulseek.PrivilegeBalance) {
	p := &s.community.privileges
	if !s.communityCurrentLocked(identity) || !p.pending || p.poisoned || p.giftActive {
		return
	}
	p.seconds = balance.Seconds
	p.updated = time.Now().UTC()
	p.fresh = true
	p.pending = false
	p.err = ""
	p.revision++
	close(p.queryDone)
	p.queryDone = nil
	p.queryCancel = nil
}
