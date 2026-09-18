package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

const communityRoomWallBytes = 4 << 20

func (s *Service) updateCommunityRoomDirectoryRolesLocked(ctx context.Context) error {
	s.pruneDirectoryRoomsLocked()
	for name, entry := range s.community.directory {
		if !entry.private {
			continue
		}
		r := s.community.rooms[name]
		if r == nil {
			r = &communityRoomState{private: true}
			s.community.rooms[name] = r
		}
	}
	for name, r := range s.community.rooms {
		if !r.private {
			continue
		}
		role := s.community.directory[name].role
		if role == "" {
			role = "none"
		}
		if r.roleMutation != nil && (r.roleMutation.Action == soulseek.RoomCancelOwnership && role != "owner" || r.roleMutation.Action == soulseek.RoomCancelMembership && role == "none") {
			if err := s.finishCommunityRoomRoleLocked(ctx, r, "confirmed"); err != nil {
				return err
			}
		}
		if role == "none" && !r.creating && (r.role != "" && r.role != "none" || r.autojoin || r.joined) {
			if err := s.revokeCommunityRoomLocked(ctx, name, r); err != nil {
				return err
			}
		}
		if role != "owner" && r.owner == s.cfg.Soulseek.Username {
			r.owner = ""
		}
		r.role, r.roleFresh = role, true
		if role == "owner" {
			r.owner = s.cfg.Soulseek.Username
		}
	}
	return nil
}

// Revoke preferences before publishing the new state. A restart cannot turn a
// revoked invitation back into an automatic join, and existing history stays.
func (s *Service) revokeCommunityRoomLocked(ctx context.Context, name string, r *communityRoomState) error {
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		if r.conversationID == 0 && !r.autojoin && r.ownWall == "" {
			return nil
		}
		q := db.New(tx)
		if err := q.PutCommunityRoom(ctx, db.PutCommunityRoomParams{Account: s.community.identity.Account, Room: name, PrivateRoom: 1, OwnWall: r.ownWall}); err != nil {
			return err
		}
		if r.conversationID != 0 {
			if _, err := q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: s.community.identity.Account, ConversationID: r.conversationID, Direction: "system", State: "received", Body: "Private-room membership revoked. Autojoin stopped; local history is retained.", CreatedAt: time.Now().UnixMilli()}); err != nil {
				return err
			}
		}
		_, err := q.BumpCommunityRevision(ctx, s.community.identity.Account)
		return err
	})
	if err != nil {
		return err
	}
	r.role, r.roleFresh, r.private = "none", true, true
	r.joined, r.wanted, r.autojoin, r.creating = false, false, false, false
	r.intent++
	r.pending = ""
	r.err = "Membership revoked; history retained and autojoin disabled."
	s.clearRoomCachesLocked(r)
	s.watchRoomLocked(name, r)
	return nil
}
func (s *Service) updateCommunityRoomRolesLocked(ctx context.Context, message soulseek.SocialMessage) error {
	switch m := message.(type) {
	case soulseek.RoomInvitations:
		value := m.Enabled
		s.community.invitationsConfirmed = &value
	case soulseek.RoomRoleList:
		r := s.community.rooms[m.Room]
		if r == nil || !r.private || !r.joined && r.pending != "join" {
			return nil
		}
		if m.Operators {
			if !s.roomCacheFitsLocked(len(r.operators), len(m.Users)) {
				return fmt.Errorf("community: room cache entry limit")
			}
			// XXX: skip undecodable roster names; nicotine+ renders whatever it receives.
			operators := make([]string, 0, len(m.Users))
			for _, username := range m.Users {
				if soulseek.ValidateUsername(username) == nil {
					operators = append(operators, username)
				}
			}
			r.operators = operators
			r.operatorsFresh = true
			if r.roleFresh && r.role != "owner" && r.role != "none" {
				r.role = "member"
				if slices.Contains(r.operators, s.cfg.Soulseek.Username) {
					r.role = "operator"
				}
			}
		} else {
			if !s.roomCacheFitsLocked(len(r.privateMembers), len(m.Users)) {
				return fmt.Errorf("community: room cache entry limit")
			}
			r.privateMembers = make(map[string]bool, len(m.Users))
			for _, username := range m.Users {
				// XXX: skip undecodable roster names; nicotine+ renders whatever it receives.
				if soulseek.ValidateUsername(username) != nil {
					continue
				}
				r.privateMembers[username] = true
			}
			r.privateMembersFresh = true
		}
	case soulseek.RoomRoleUpdate:
		// XXX: skip undecodable role targets; nicotine+ renders whatever it receives.
		if m.Username != "" && soulseek.ValidateUsername(m.Username) != nil {
			return nil
		}
		r := s.community.rooms[m.Room]
		if m.Action == soulseek.RoomMembershipGranted {
			if r == nil {
				r = &communityRoomState{}
			}
			s.community.rooms[m.Room] = r
			if !r.roleFresh || r.role != "owner" && r.role != "operator" {
				r.role = "member"
			}
			r.private, r.roleFresh = true, true
			r.err = "Private-room membership granted. Join explicitly to participate."
			entry := s.community.directory[m.Room]
			entry.private, entry.role = true, r.role
			s.community.directory[m.Room] = entry
		} else if r == nil {
			return nil
		}
		if r.roleMutation != nil && r.roleMutation.Action == m.Action && r.roleMutation.Username == m.Username {
			if err := s.finishCommunityRoomRoleLocked(ctx, r, "confirmed"); err != nil {
				return err
			}
		}
		switch m.Action {
		case soulseek.RoomCreationRejected:
			r.pending, r.creating = "", false
			r.err = "Server rejected room creation; an inaccessible private room may already use this name."
		case soulseek.RoomMembershipRevoked:
			if r.roleMutation != nil && (r.roleMutation.Action == soulseek.RoomCancelMembership || r.roleMutation.Action == soulseek.RoomCancelOwnership) {
				if err := s.finishCommunityRoomRoleLocked(ctx, r, "confirmed"); err != nil {
					return err
				}
			}
			if err := s.revokeCommunityRoomLocked(ctx, m.Room, r); err != nil {
				return err
			}
			entry := s.community.directory[m.Room]
			entry.private, entry.role = true, "none"
			s.community.directory[m.Room] = entry
		case soulseek.RoomAddMember:
			if !r.privateMembersFresh {
				break
			}
			if r.privateMembers == nil {
				r.privateMembers = map[string]bool{}
			}
			if !r.privateMembers[m.Username] && (len(r.privateMembers) >= soulseek.MaxRoomUsers || !s.roomCacheFitsLocked(0, 1)) {
				return fmt.Errorf("community: private member limit")
			}
			r.privateMembers[m.Username] = true
		case soulseek.RoomRemoveMember:
			delete(r.privateMembers, m.Username)
			r.operators = slices.DeleteFunc(r.operators, func(user string) bool { return user == m.Username })
			if m.Username == s.cfg.Soulseek.Username {
				if err := s.revokeCommunityRoomLocked(ctx, m.Room, r); err != nil {
					return err
				}
			}
		case soulseek.RoomAddOperator, soulseek.RoomOperatorshipGranted:
			username := m.Username
			if m.Action == soulseek.RoomOperatorshipGranted {
				username = s.cfg.Soulseek.Username
			}
			if r.operatorsFresh && !slices.Contains(r.operators, username) {
				if len(r.operators) >= soulseek.MaxRoomUsers || !s.roomCacheFitsLocked(0, 1) {
					return fmt.Errorf("community: operator limit")
				}
				r.operators = append(r.operators, username)
			}
			if username == s.cfg.Soulseek.Username && r.role != "owner" {
				r.role, r.roleFresh = "operator", true
			}
		case soulseek.RoomRemoveOperator, soulseek.RoomOperatorshipRevoked:
			username := m.Username
			if m.Action == soulseek.RoomOperatorshipRevoked {
				username = s.cfg.Soulseek.Username
			}
			r.operators = slices.DeleteFunc(r.operators, func(user string) bool { return user == username })
			if username == s.cfg.Soulseek.Username && r.role != "owner" && r.role != "none" {
				r.role, r.roleFresh = "member", true
			}
		}
	case soulseek.RoomWallSnapshot:
		r := s.community.rooms[m.Room]
		if r == nil || !r.joined {
			return nil
		}
		next := make(map[string]string, len(m.Entries))
		for _, entry := range m.Entries {
			// XXX: skip undecodable wall authors; nicotine+ renders whatever it receives.
			if soulseek.ValidateUsername(entry.Username) != nil {
				continue
			}
			next[entry.Username], _ = s.community.text.incoming(entry.Text, "")
		}
		bytes := 0
		for username, text := range next {
			bytes += len(username) + len(text)
		}
		if bytes > communityRoomWallBytes || s.community.wallBytes-r.wallBytes+bytes > communityRoomWallsBytes {
			return fmt.Errorf("community: wall byte limit")
		}
		s.community.wallBytes += bytes - r.wallBytes
		r.wall, r.wallFresh, r.wallBytes = next, true, bytes
		if r.wall[s.cfg.Soulseek.Username] == r.ownWall {
			r.wallState = "confirmed"
		}
	case soulseek.RoomWallUpdate:
		// XXX: skip undecodable wall authors; nicotine+ renders whatever it receives.
		if soulseek.ValidateUsername(m.Username) != nil {
			return nil
		}
		r := s.community.rooms[m.Room]
		if r == nil || !r.joined {
			return nil
		}
		if r.wall == nil {
			r.wall = map[string]string{}
		}
		bytes := r.wallBytes
		if old, ok := r.wall[m.Username]; ok {
			bytes -= len(m.Username) + len(old)
		}
		if m.Remove {
			delete(r.wall, m.Username)
		} else {
			if len(r.wall) >= soulseek.MaxRoomUsers {
				if _, ok := r.wall[m.Username]; !ok {
					return fmt.Errorf("community: wall entry limit")
				}
			}
			text, _ := s.community.text.incoming(m.Text, "")
			bytes += len(m.Username) + len(text)
			if bytes > communityRoomWallBytes || s.community.wallBytes-r.wallBytes+bytes > communityRoomWallsBytes {
				return fmt.Errorf("community: wall byte limit")
			}
			r.wall[m.Username] = text
		}
		s.community.wallBytes += bytes - r.wallBytes
		r.wallBytes = bytes
		if m.Username == s.cfg.Soulseek.Username && r.wall[m.Username] == r.ownWall {
			r.wallState = "confirmed"
		}
	}
	s.community.revision++
	s.wakeCommunityOutboxLocked()
	return nil
}
