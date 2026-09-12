package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

// Preferences are reconciled on the existing connection worker. A socket write
// is not a server acknowledgement, and an unconfirmed role action is never replayed.
func (s *Service) syncCommunityRoomExtras(ctx context.Context, client *soulseek.Client, identity CommunityIdentity, names []string) error {
	s.mu.Lock()
	if !s.communityCurrentLocked(identity) || s.client != client {
		s.mu.Unlock()
		return ErrCommunitySession
	}
	wanted := s.community.invitationsWanted
	writeInvitations := s.community.invitationsWritten == nil || *s.community.invitationsWritten != wanted
	if s.community.invitationsWritten != nil && s.community.invitationsConfirmed == nil && !s.community.invitationsDeadline.IsZero() && !time.Now().Before(s.community.invitationsDeadline) {
		s.community.invitationsDeadline = time.Time{}
		s.community.revision++
	}
	s.mu.Unlock()
	if writeInvitations {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := client.SetRoomInvitations(writeCtx, wanted, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.communityCurrentLocked(identity) || s.client != client {
				return ErrCommunitySession
			}
			if s.shuttingDown {
				return ErrClosed
			}
			if s.community.invitationsWanted != wanted || s.community.invitationsWritten != nil && *s.community.invitationsWritten == wanted {
				return ErrCommunityMessageState
			}
			s.community.invitationsWritten = &wanted
			s.community.invitationsDeadline = time.Now().Add(15 * time.Second)
			s.community.revision++
			return nil
		})
		cancel()
		if errors.Is(err, ErrClosed) {
			return nil
		}
		if err != nil && !errors.Is(err, ErrCommunityMessageState) {
			return err
		}
	}
	for _, name := range names {
		s.mu.Lock()
		if !s.communityCurrentLocked(identity) || s.client != client {
			s.mu.Unlock()
			return ErrCommunitySession
		}
		if s.shuttingDown {
			s.mu.Unlock()
			return nil
		}
		r := s.community.rooms[name]
		if r == nil {
			s.mu.Unlock()
			continue
		}
		if r.wallState == "pending" && !time.Now().Before(r.wallDeadline) {
			r.wallState = "unknown"
			s.community.revision++
		}
		eligible := r.joined && r.wanted && r.wallIntent > r.wallIssued
		text, intent := r.ownWall, r.wallIntent
		s.mu.Unlock()
		if !eligible {
			continue
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := client.SetRoomWall(writeCtx, name, text, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.communityCurrentLocked(identity) || s.client != client {
				return ErrCommunitySession
			}
			if s.shuttingDown {
				return ErrClosed
			}
			if s.community.rooms[name] != r || !r.joined || !r.wanted || r.wallIntent != intent || r.wallIssued >= intent {
				return ErrCommunityMessageState
			}
			r.wallIssued, r.wallState, r.wallDeadline = intent, "pending", time.Now().Add(15*time.Second)
			s.community.revision++
			return nil
		})
		cancel()
		if errors.Is(err, ErrClosed) {
			return nil
		}
		if err != nil && !errors.Is(err, ErrCommunityMessageState) {
			return err
		}
	}
	return nil
}
