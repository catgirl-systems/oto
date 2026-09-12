package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

// Only preferences/history survive restart. Membership and pending network
// operations belong to one connection, protected by Service.mu.
type communityRoomState struct {
	conversationID                                           int64
	autojoin, private, wanted, joined                        bool
	intent, issued                                           uint64
	pending                                                  string
	deadline                                                 time.Time
	err                                                      string
	members                                                  map[string]soulseek.RoomUser
	owner                                                    string
	operators                                                []string
	role                                                     string
	roleFresh, operatorsFresh, privateMembersFresh, creating bool
	privateMembers                                           map[string]bool
	roleMutation                                             *communityRoomRoleMutation
	ownWall                                                  string
	wallIntent, wallIssued                                   uint64
	wallFresh                                                bool
	wallState                                                string
	wallDeadline                                             time.Time
	wall                                                     map[string]string
	wallBytes                                                int
	rejectJoin                                               bool
}
type communityRoomListing struct {
	population               uint32
	populationKnown, private bool
	role                     string
}
type CommunityFeedMessage struct {
	ID        int64     `json:"id"`
	Room      string    `json:"room"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

func loadCommunityRooms(ctx context.Context, q *db.Queries, account string, next *communityState) error {
	next.rooms = map[string]*communityRoomState{}
	next.directory = map[string]communityRoomListing{}
	for after := ""; ; {
		rows, err := q.ListCommunityRooms(ctx, db.ListCommunityRoomsParams{Account: account, AfterRoom: after, PageSize: 200})
		if err != nil {
			return err
		}
		for _, row := range rows {
			next.rooms[row.Room] = &communityRoomState{autojoin: row.Autojoin != 0, private: row.PrivateRoom != 0, wanted: row.Autojoin != 0, intent: 1}
			r := next.rooms[row.Room]
			r.ownWall = row.OwnWall
			if r.ownWall != "" {
				r.wallIntent = 1
				r.wallState = "saved; awaiting confirmed join"
			}
			after = row.Room
		}
		if len(rows) < 200 {
			return nil
		}
	}
}

func (s *Service) retireCommunityRoomsLocked() {
	s.community.directoryFresh = false
	s.community.directoryPending = false
	s.community.feedWritten = false
	s.community.invitationsWritten, s.community.invitationsConfirmed = nil, nil
	for name, r := range s.community.rooms {
		r.wanted, r.joined, r.pending = r.autojoin, false, ""
		r.intent++
		r.err = "Disconnected; room messages missed while away cannot be recovered."
		r.roleFresh, r.operatorsFresh, r.privateMembersFresh, r.creating, r.wallFresh = false, false, false, false, false
		if r.wallState == "pending" {
			r.wallState = "unknown"
		}
		if r.roleMutation != nil && r.roleMutation.State == "pending" {
			r.roleMutation.State = "unknown"
		}
		s.clearRoomCachesLocked(r)
		r.rejectJoin = false
		delete(s.community.watches, "room:"+name)
	}
}

func (s *Service) updateCommunityRoomLocked(ctx context.Context, message soulseek.SocialMessage) error {
	switch m := message.(type) {
	case soulseek.RoomDirectory:
		next := make(map[string]communityRoomListing)
		for _, group := range []struct {
			entries []soulseek.RoomPopulation
			role    string
		}{{m.Public, ""}, {m.Member, "member"}, {m.Owned, "owner"}} {
			for _, entry := range group.entries {
				next[entry.Room] = communityRoomListing{population: entry.Users, populationKnown: entry.UsersKnown, private: group.role != "", role: group.role}
			}
		}
		for _, name := range m.Operated {
			entry := next[name]
			entry.private = true
			if entry.role != "owner" {
				entry.role = "operator"
			}
			next[name] = entry
		}
		s.community.directory = next
		s.community.directoryFresh, s.community.directoryPending = true, false
		if err := s.updateCommunityRoomDirectoryRolesLocked(ctx); err != nil {
			return err
		}
	case soulseek.RoomJoined:
		r := s.community.rooms[m.Room]
		if r == nil {
			r = &communityRoomState{}
			s.community.rooms[m.Room] = r
		}
		if !r.wanted || !r.joined && r.issued != r.intent || r.private && !r.creating && (!r.roleFresh || r.role == "none" || r.role == "") {
			r.wanted, r.rejectJoin = false, true
			r.intent++
			r.err = "Unsolicited or unauthorized join ignored; leaving the server room."
			s.community.revision++
			s.wakeCommunityOutboxLocked()
			return nil
		}
		if !s.roomCacheFitsLocked(len(r.members)+len(r.operators), len(m.Users)+len(m.Operators)) {
			return fmt.Errorf("community: room cache entry limit")
		}
		if !r.joined {
			if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
				q := db.New(tx)
				c, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{Account: s.community.identity.Account, Kind: "room", Target: m.Room})
				if err != nil {
					return err
				}
				if err := q.PutCommunityRoom(ctx, db.PutCommunityRoomParams{Account: s.community.identity.Account, Room: m.Room, Autojoin: boolInt(r.autojoin), PrivateRoom: boolInt(m.Private), OwnWall: r.ownWall}); err != nil {
					return err
				}
				text := "Joined room. Earlier room messages cannot be recovered."
				info, err := q.CommunityHistoryInfo(ctx, db.CommunityHistoryInfoParams{Account: c.Account, ConversationID: c.ID})
				if err != nil {
					return err
				}
				if info.LatestID != 0 {
					text = "History gap: messages sent while disconnected or outside the room cannot be recovered. Rejoined room."
				}
				if _, err = q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: c.Account, ConversationID: c.ID, Direction: "system", Body: text, State: "received", CreatedAt: time.Now().UnixMilli()}); err != nil {
					return err
				}
				if _, err = q.BumpCommunityRevision(ctx, c.Account); err != nil {
					return err
				}
				return nil
			}); err != nil {
				return err
			}
		}
		// Publish nothing until the durable history boundary above succeeds.
		conversation, err := s.stateDB.Queries().FindCommunityConversation(ctx, db.FindCommunityConversationParams{Account: s.community.identity.Account, Kind: "room", Target: m.Room})
		if err != nil {
			return err
		}
		r.conversationID, r.joined, r.private = conversation.ID, true, m.Private
		r.owner, r.operators = m.Owner, slices.Clone(m.Operators)
		r.role, r.roleFresh, r.operatorsFresh, r.creating = "", true, true, false
		if m.Private {
			r.role = "member"
			if slices.Contains(r.operators, s.cfg.Soulseek.Username) {
				r.role = "operator"
			}
			if r.owner == s.cfg.Soulseek.Username {
				r.role = "owner"
			}
		}
		s.clearRoomWallLocked(r)
		r.wallIssued = 0
		r.wallState = "waiting for wall snapshot"
		if r.ownWall != "" {
			r.wallIntent++
		}
		r.err = ""
		if r.pending == "join" {
			r.pending = ""
		}
		r.members = make(map[string]soulseek.RoomUser, len(m.Users))
		for _, user := range m.Users {
			r.members[user.Username] = user
		}
		s.watchRoomLocked(m.Room, r)
		for _, user := range m.Users {
			s.hydrateRoomUserLocked(user)
		}
	case soulseek.RoomLeft:
		if r := s.community.rooms[m.Room]; r != nil {
			r.joined = false
			s.clearRoomCachesLocked(r)
			r.rejectJoin = false
			r.wallFresh = false
			if r.wallState == "pending" {
				r.wallState = "unknown"
			}
			r.members = nil
			if r.pending == "leave" {
				r.pending = ""
			}
			r.err = ""
			s.watchRoomLocked(m.Room, r)
		}
	case soulseek.RoomUserJoined:
		if r := s.community.rooms[m.Room]; r != nil && r.joined {
			if len(r.members) >= soulseek.MaxRoomUsers {
				if _, ok := r.members[m.User.Username]; !ok {
					return fmt.Errorf("community: room roster exceeds local limit")
				}
			}
			if _, exists := r.members[m.User.Username]; !exists && !s.roomCacheFitsLocked(0, 1) {
				return fmt.Errorf("community: room cache entry limit")
			}
			r.members[m.User.Username] = m.User
			s.watchRoomLocked(m.Room, r)
			s.hydrateRoomUserLocked(m.User)
		}
	case soulseek.RoomUserLeft:
		if r := s.community.rooms[m.Room]; r != nil && r.joined {
			delete(r.members, m.Username)
			s.watchRoomLocked(m.Room, r)
		}
	case soulseek.RoomMessage:
		return s.receiveCommunityRoomMessageLocked(ctx, m)
	}
	s.community.revision++
	s.wakeCommunityOutboxLocked()
	return nil
}

func (s *Service) watchRoomLocked(name string, r *communityRoomState) {
	var users []string
	if r.joined {
		for user := range r.members {
			users = append(users, user)
		}
	}
	s.setUserWatchesLocked("room:"+name, users, time.Time{})
}
func (s *Service) hydrateRoomUserLocked(member soulseek.RoomUser) {
	user, ok := s.community.users[member.Username]
	if !ok {
		return
	}
	now := time.Now().UTC()
	user.Exists = true
	if member.StatusKnown {
		user.Status, user.StatusFresh, user.StatusUpdatedAt = member.Status, true, now
	}
	if member.StatsKnown {
		user.Stats, user.StatsFresh, user.StatsUpdatedAt = member.Stats, true, now
	}
	if member.CountryKnown {
		user.Country = member.Country
	}
	// Initial roster hydration is not an observed online->offline transition.
	s.community.users[member.Username] = user
}

func (s *Service) receiveCommunityRoomMessageLocked(ctx context.Context, m soulseek.RoomMessage) error {
	if m.PublicFeed {
		if !s.community.feedWanted || !s.community.feedWritten {
			return nil
		}
		// The feed is explicitly requested, read-only, and never a durable log by
		// default. Bound retained text as well as the number of entries.
		s.community.feedID++
		entry := CommunityFeedMessage{ID: s.community.feedID, Room: m.Room, Sender: m.Username, Text: communityDisplayText(m.Text), CreatedAt: time.Now().UTC()}
		s.community.feed = append(s.community.feed, entry)
		s.community.feedBytes += len(entry.Text) + len(entry.Sender) + len(entry.Room)
		for len(s.community.feed) > 200 || s.community.feedBytes > 1<<20 {
			old := s.community.feed[0]
			s.community.feedBytes -= len(old.Text) + len(old.Sender) + len(old.Room)
			s.community.feed = slices.Delete(s.community.feed, 0, 1)
		}
		s.community.revision++
		return nil
	}
	r := s.community.rooms[m.Room]
	if r == nil || !r.joined {
		return nil
	}
	direction, state := "incoming", "received"
	if m.Username == s.cfg.Soulseek.Username {
		direction, state = "outgoing", "sent"
	}
	return s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		if _, err := q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: s.community.identity.Account, ConversationID: r.conversationID, Sender: m.Username, Direction: direction, State: state, Body: communityDisplayText(m.Text), CreatedAt: time.Now().UnixMilli()}); err != nil {
			return err
		}
		_, err := q.BumpCommunityRevision(ctx, s.community.identity.Account)
		return err
	})
}

// Server PMs remain in Chats -> server; only their association with pending room
// joins lives here. Never put their bodies in diagnostic errors or invent a
// confirmation from them. Replayed notices cannot mutate current membership.
func (s *Service) communityRoomNoticeLocked(message soulseek.PrivateMessage) {
	if message.Username != "server" || !message.New {
		return
	}
	for _, r := range s.community.rooms {
		if r.pending == "join" {
			r.err = "Server notice received; open Chats -> server for the reason. Membership is not confirmed."
		}
	}
	s.community.revision++
}

func (s *Service) beginCommunityRoomsLocked() {
	// A freshly connected socket needs a fresh full directory, not the limited
	// unsolicited startup list. Never restore server-authoritative membership.
	s.community.directoryRefresh = true
	s.community.invitationsWritten, s.community.invitationsConfirmed = nil, nil
	for _, r := range s.community.rooms {
		r.joined, r.pending = false, ""
		r.intent++
		if r.autojoin {
			r.wanted = true
		}
	}
}

func communityRoomStateName(online bool, r *communityRoomState) string {
	if !online {
		return "offline"
	}
	if r == nil {
		return "not-joined"
	}
	if r.pending == "join" {
		return "joining"
	}
	if r.pending == "leave" {
		return "leaving"
	}
	if r.issued < r.intent && r.wanted != r.joined {
		if r.wanted {
			return "join-pending"
		}
		return "leave-pending"
	}
	if r.joined {
		return "joined"
	}
	if r.err != "" {
		return "unknown"
	}
	return "not-joined"
}

func (s *Service) communityRoomNamesLocked() []string {
	names := make([]string, 0, len(s.community.rooms))
	for name := range s.community.rooms {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func validateCommunityRoomPage(cursor, query string) error {
	if len(cursor) > soulseek.MaxUsernameBytes || len(query) > 1024 || strings.ContainsAny(query, "\x00\r\n") {
		return fmt.Errorf("community: invalid room page")
	}
	return nil
}
