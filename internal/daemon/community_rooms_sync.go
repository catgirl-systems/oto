package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

// The existing connection worker reconciles room intentions. No reader callback
// waits for a write or a reply; each intent is written at most once per session.
func (s *Service) syncCommunityRooms(ctx context.Context, client *soulseek.Client, identity CommunityIdentity) error {
	s.mu.Lock()
	if !s.communityCurrentLocked(identity) || s.client != client {
		s.mu.Unlock()
		return ErrCommunitySession
	}
	if s.shuttingDown {
		s.mu.Unlock()
		return nil
	}
	now := time.Now()
	if s.community.directoryPending && !now.Before(s.community.directoryDeadline) {
		s.community.directoryPending = false
		s.community.directoryFresh = false
		s.community.revision++
	}
	refresh := s.community.directoryRefresh && !s.community.directoryPending
	if refresh {
		s.community.directoryRefresh = false
		s.community.directoryPending = true
		s.community.directoryDeadline = now.Add(15 * time.Second)
		s.community.revision++
	}
	names := s.communityRoomNamesLocked()
	s.mu.Unlock()
	if refresh {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := client.RequestRoomDirectory(writeCtx)
		cancel()
		if err != nil {
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
		if r.pending != "" && !time.Now().Before(r.deadline) {
			r.pending = ""
			r.err = "No server confirmation. Check Chats -> server and retry explicitly."
			s.community.revision++
		}
		wanted, intent, private := r.wanted, r.intent, r.private
		eligible := r.pending == "" && r.issued < intent && r.joined != wanted
		s.mu.Unlock()
		if !eligible {
			continue
		}
		before := func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.communityCurrentLocked(identity) || s.client != client {
				return ErrCommunitySession
			}
			if s.shuttingDown {
				return ErrClosed
			}
			if r != s.community.rooms[name] || r.intent != intent || r.wanted != wanted || r.pending != "" || r.joined == wanted {
				return ErrCommunityMessageState
			}
			r.pending = "leave"
			if wanted {
				r.pending = "join"
			}
			r.issued = intent
			r.deadline = time.Now().Add(15 * time.Second)
			r.err = ""
			s.community.revision++
			return nil
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var err error
		if wanted {
			_, err = client.JoinRoom(writeCtx, name, private, before)
		} else {
			_, err = client.LeaveRoom(writeCtx, name, before)
		}
		cancel()
		if errors.Is(err, ErrClosed) {
			return nil
		}
		if errors.Is(err, ErrCommunityMessageState) {
			continue
		}
		if err != nil {
			return err
		}
	}
	s.mu.RLock()
	wanted, written := s.community.feedWanted, s.community.feedWritten
	s.mu.RUnlock()
	if wanted == written {
		return nil
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := client.SetPublicFeed(writeCtx, wanted, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.communityCurrentLocked(identity) || s.client != client {
			return ErrCommunitySession
		}
		if s.shuttingDown {
			return ErrClosed
		}
		if s.community.feedWanted != wanted || s.community.feedWritten == wanted {
			return ErrCommunityMessageState
		}
		// Reserve before Write: the first feed message may precede its return.
		s.community.feedWritten = wanted
		s.community.revision++
		return nil
	})
	if errors.Is(err, ErrCommunityMessageState) || errors.Is(err, ErrClosed) {
		return nil
	}
	return err
}
