package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

type CommunityRoom struct {
	Name            string                   `json:"name"`
	Population      uint32                   `json:"population"`
	PopulationKnown bool                     `json:"population_known"`
	Private         bool                     `json:"private"`
	Role            string                   `json:"role"`
	DirectoryFresh  bool                     `json:"directory_fresh"`
	Remembered      bool                     `json:"remembered"`
	Joined          bool                     `json:"joined"`
	RosterFresh     bool                     `json:"roster_fresh"`
	State           string                   `json:"state"`
	ConversationID  int64                    `json:"conversation_id"`
	Error           string                   `json:"error,omitempty"`
	RoleFresh       bool                     `json:"role_fresh"`
	Owner           string                   `json:"owner"`
	LastRoleAction  *CommunityRoomRoleResult `json:"last_role_action,omitempty"`
}
type CommunityRoomsRequest struct {
	CommunityIdentity
	Room   string `json:"room"`
	Cursor string `json:"cursor"`
	Query  string `json:"query"`
	Mode   string `json:"mode"` // all, remembered, joined, history
	Limit  int    `json:"limit"`
}
type CommunityRoomsPage struct {
	CommunityIdentity
	Rooms              []CommunityRoom `json:"rooms"`
	NextCursor         string          `json:"next_cursor"`
	Revision           uint64          `json:"revision"`
	DirectoryFresh     bool            `json:"directory_fresh"`
	Refreshing         bool            `json:"refreshing"`
	InvitationsEnabled bool            `json:"invitations_enabled"`
	InvitationsState   string          `json:"invitations_state"`
}

func (s *Service) communityRoomLocked(name string) CommunityRoom {
	entry := s.community.directory[name]
	r := s.community.rooms[name]
	out := CommunityRoom{Name: name, Population: entry.population, PopulationKnown: entry.populationKnown, Private: entry.private, Role: entry.role, DirectoryFresh: s.community.directoryFresh, State: communityRoomStateName(s.community.online, r)}
	if r != nil {
		out.Remembered, out.Joined, out.RosterFresh, out.ConversationID, out.Error = r.autojoin, r.joined, r.joined && s.community.online, r.conversationID, r.err
		out.Private = out.Private || r.private
		out.Role, out.RoleFresh, out.Owner = r.role, s.community.online && r.roleFresh, r.owner
		if r.roleMutation != nil {
			action := r.roleMutation.CommunityRoomRoleResult
			out.LastRoleAction = &action
		}
		if out.RosterFresh {
			out.Population, out.PopulationKnown = uint32(len(r.members)), true
		}
	}
	return out
}
func (s *Service) CommunityRooms(ctx context.Context, req CommunityRoomsRequest) (CommunityRoomsPage, error) {
	limit, err := communityPageLimit(req.Limit)
	if err != nil || validateCommunityRoomPage(req.Cursor, req.Query) != nil || !utf8.ValidString(req.Query) || req.Room != "" && (req.Cursor != "" || req.Query != "" || req.Mode != "") {
		return CommunityRoomsPage{}, errors.New("community: invalid room page")
	}
	if req.Room != "" {
		if err := soulseek.ValidateRoomName(req.Room); err != nil {
			return CommunityRoomsPage{}, err
		}
	}
	if req.Mode != "" && req.Mode != "all" && req.Mode != "remembered" && req.Mode != "joined" && req.Mode != "history" && req.Mode != "invitations" {
		return CommunityRoomsPage{}, errors.New("community: invalid room list mode")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityRoomsPage{}, err
	}
	account, err := s.stateDB.Queries().GetCommunityAccount(ctx, req.Account)
	if err != nil {
		return CommunityRoomsPage{}, err
	}
	out := CommunityRoomsPage{CommunityIdentity: req.CommunityIdentity, Rooms: []CommunityRoom{}, Revision: s.community.revision + uint64(account.Revision), DirectoryFresh: s.community.directoryFresh, Refreshing: s.community.directoryPending || s.community.directoryRefresh}
	out.InvitationsEnabled, out.InvitationsState = s.community.invitationsWanted, "saved; reconnect pending"
	if s.community.online {
		out.InvitationsState = "queued"
		if s.community.invitationsWritten != nil {
			out.InvitationsState = "pending"
			if s.community.invitationsDeadline.IsZero() {
				out.InvitationsState = "unknown"
			}
		}
		if s.community.invitationsConfirmed != nil {
			out.InvitationsState = "server differs"
			if *s.community.invitationsConfirmed == s.community.invitationsWanted {
				out.InvitationsState = "confirmed"
			}
		}
	}
	if req.Room != "" {
		out.Rooms = append(out.Rooms, s.communityRoomLocked(req.Room))
		return out, nil
	}
	set := map[string]bool{}
	for name := range s.community.directory {
		set[name] = true
	}
	for name := range s.community.rooms {
		set[name] = true
	}
	var names []string
	for name := range set {
		if name <= req.Cursor || !strings.Contains(strings.ToLower(name), strings.ToLower(req.Query)) {
			continue
		}
		r := s.community.rooms[name]
		if req.Mode == "invitations" && (r == nil || !r.private || !r.roleFresh || r.role == "none" || r.role == "" || r.joined) {
			continue
		}
		if req.Mode == "remembered" && (r == nil || !r.autojoin) || req.Mode == "joined" && (r == nil || !r.joined) || req.Mode == "history" && (r == nil || r.conversationID == 0) {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	budget := 0
	for _, name := range names {
		room := s.communityRoomLocked(name)
		encoded, err := json.Marshal(room)
		if err != nil {
			return CommunityRoomsPage{}, err
		}
		if len(out.Rooms) == int(limit) || budget+len(encoded)+1 > communityPageBytes {
			out.NextCursor = out.Rooms[len(out.Rooms)-1].Name
			break
		}
		budget += len(encoded) + 1
		out.Rooms = append(out.Rooms, room)
	}
	return out, nil
}

func (s *Service) RefreshCommunityRooms(ctx context.Context, identity CommunityIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, identity); err != nil {
		return err
	}
	if !s.community.online {
		return soulseek.ErrNotConnected
	}
	if !s.community.directoryPending {
		s.community.directoryRefresh = true
		s.wakeCommunityOutboxLocked()
	}
	return nil
}

type CommunityRoomMembersRequest struct {
	CommunityIdentity
	Room    string `json:"room"`
	Cursor  string `json:"cursor"`
	Query   string `json:"query"`
	Limit   int    `json:"limit"`
	Private bool   `json:"private"` // Granted membership, including users not currently present.
}
type CommunityRoomMember struct {
	CommunityUser
	SlotsFull  uint32 `json:"slots_full"`
	SlotsKnown bool   `json:"slots_known"`
	Role       string `json:"role"`
}
type CommunityRoomMembersPage struct {
	CommunityIdentity
	Room         CommunityRoom         `json:"room"`
	Members      []CommunityRoomMember `json:"members"`
	NextCursor   string                `json:"next_cursor"`
	Revision     uint64                `json:"revision"`
	MembersFresh bool                  `json:"members_fresh"`
}

func (s *Service) CommunityRoomMembers(ctx context.Context, req CommunityRoomMembersRequest) (CommunityRoomMembersPage, error) {
	if err := soulseek.ValidateRoomName(req.Room); err != nil {
		return CommunityRoomMembersPage{}, err
	}
	limit, err := communityPageLimit(req.Limit)
	if err != nil || validateCommunityRoomPage(req.Cursor, req.Query) != nil || !utf8.ValidString(req.Query) {
		return CommunityRoomMembersPage{}, errors.New("community: invalid roster page")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityRoomMembersPage{}, err
	}
	account, err := s.stateDB.Queries().GetCommunityAccount(ctx, req.Account)
	if err != nil {
		return CommunityRoomMembersPage{}, err
	}
	out := CommunityRoomMembersPage{CommunityIdentity: req.CommunityIdentity, Room: s.communityRoomLocked(req.Room), Members: []CommunityRoomMember{}, Revision: s.community.revision + uint64(account.Revision)}
	r := s.community.rooms[req.Room]
	if r == nil {
		return out, nil
	}
	out.MembersFresh = out.Room.RosterFresh
	set := map[string]bool{}
	for name := range r.members {
		set[name] = true
	}
	if req.Private {
		out.MembersFresh = s.community.online && r.roleFresh && r.privateMembersFresh
		set = map[string]bool{}
		for name := range r.privateMembers {
			set[name] = true
		}
		for _, name := range r.operators {
			set[name] = true
		}
		if r.owner != "" {
			set[r.owner] = true
		}
	}
	var names []string
	for name := range set {
		if name > req.Cursor && strings.Contains(strings.ToLower(name), strings.ToLower(req.Query)) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	// Reserve the worst-case JSON-escaped username cursor as well as the rows.
	budget := 6 * 1024
	for _, name := range names {
		member := r.members[name]
		user, ok := s.community.users[name]
		if !ok {
			user = CommunityUser{Username: name, Exists: true, Status: member.Status, Stats: member.Stats, Country: member.Country}
		}
		row := CommunityRoomMember{CommunityUser: user, SlotsFull: member.SlotsFull, SlotsKnown: member.SlotsKnown && out.Room.RosterFresh}
		if r.private {
			row.Role = "member"
			if slices.Contains(r.operators, name) {
				row.Role = "operator"
			}
			if name == r.owner {
				row.Role = "owner"
			}
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return CommunityRoomMembersPage{}, err
		}
		if len(out.Members) == int(limit) || budget+len(encoded)+1 > communityPageBytes {
			out.NextCursor = out.Members[len(out.Members)-1].Username
			break
		}
		budget += len(encoded) + 1
		out.Members = append(out.Members, row)
	}
	return out, nil
}

type CommunityFeedRequest struct {
	CommunityIdentity
	Cursor int64 `json:"cursor"` // Older entries, newest first; not a durable history cursor.
	Limit  int   `json:"limit"`
}
type CommunityFeedPage struct {
	CommunityIdentity
	Messages       []CommunityFeedMessage `json:"messages"`
	NextCursor     int64                  `json:"next_cursor"`
	Revision       uint64                 `json:"revision"`
	Requested      bool                   `json:"requested"`
	RequestWritten bool                   `json:"request_written"`
	Connected      bool                   `json:"connected"`
}
type CommunityFeedSubscription struct {
	CommunityIdentity
	Enabled bool `json:"enabled"`
}

func (s *Service) SetCommunityFeed(ctx context.Context, req CommunityFeedSubscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return err
	}
	if s.community.feedWanted != req.Enabled {
		s.community.feedWanted = req.Enabled
		s.community.revision++
		s.wakeCommunityOutboxLocked()
	}
	return nil
}
func (s *Service) CommunityFeed(ctx context.Context, req CommunityFeedRequest) (CommunityFeedPage, error) {
	limit, err := communityPageLimit(req.Limit)
	if err != nil || req.Cursor < 0 {
		return CommunityFeedPage{}, errors.New("community: invalid feed page")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityFeedPage{}, err
	}
	out := CommunityFeedPage{CommunityIdentity: req.CommunityIdentity, Messages: []CommunityFeedMessage{}, Revision: s.community.revision, Requested: s.community.feedWanted, RequestWritten: s.community.feedWritten, Connected: s.community.online}
	budget := 0
	for i := len(s.community.feed) - 1; i >= 0; i-- {
		row := s.community.feed[i]
		if req.Cursor != 0 && row.ID >= req.Cursor {
			continue
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return CommunityFeedPage{}, err
		}
		if len(out.Messages) == int(limit) || budget+len(encoded)+1 > communityPageBytes {
			out.NextCursor = out.Messages[len(out.Messages)-1].ID
			break
		}
		budget += len(encoded) + 1
		out.Messages = append(out.Messages, row)
	}
	return out, nil
}
