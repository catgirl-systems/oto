package daemon

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

var ErrCommunityMessageState = errors.New("community: message or conversation changed; refresh and try again")

const communityPageBytes = 512 << 10 // Leaves room for metadata within IPC's 1 MiB budget.

type CommunityConversation struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	Target      string `json:"target"`
	Closed      bool   `json:"closed"`
	ReadThrough int64  `json:"read_through"`
	Unread      int64  `json:"unread"`
	Mentions    int64  `json:"mentions"`
	LatestID    int64  `json:"latest_id"`
}

type CommunityMessage struct {
	ID             int64      `json:"id"`
	ConversationID int64      `json:"conversation_id"`
	Sender         string     `json:"sender"`
	Direction      string     `json:"direction"`
	Text           string     `json:"text"`
	CreatedAt      time.Time  `json:"created_at"`
	ServerTime     *time.Time `json:"server_time,omitempty"`
	State          string     `json:"state"`
	Mention        bool       `json:"mention"`
	Error          string     `json:"error,omitempty"`
}

func communityMessage(row db.CommunityMessage) CommunityMessage {
	out := CommunityMessage{ID: row.ID, ConversationID: row.ConversationID, Sender: row.Sender, Direction: row.Direction,
		Text: row.Body, CreatedAt: time.UnixMilli(row.CreatedAt).UTC(), State: row.State, Mention: row.Mention != 0, Error: row.Error}
	if row.Body == communityCTCPVersionRequest {
		out.Text = "[CTCP] VERSION request"
	}
	if row.ServerTime != nil {
		ts := time.Unix(*row.ServerTime, 0).UTC()
		out.ServerTime = &ts
	}
	return out
}

func (s *Service) checkCommunityIdentityLocked(ctx context.Context, identity CommunityIdentity) error {
	if s.closed || s.shuttingDown {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if identity != s.community.identity || identity.Account != accountKey(s.cfg) {
		return ErrCommunitySession
	}
	return nil
}

func communityPageLimit(limit int) (int64, error) {
	if limit < 0 {
		return 0, errors.New("community: invalid page limit")
	}
	if limit == 0 {
		limit = 200
	}
	return int64(min(limit, 200)), nil
}

type CommunityConversationsRequest struct {
	CommunityIdentity
	Cursor        int64  `json:"cursor"`
	Limit         int    `json:"limit"`
	Kind          string `json:"kind"`
	Query         string `json:"query"` // Literal, case-sensitive history/name search.
	IncludeClosed bool   `json:"include_closed"`
}

type CommunityConversationsPage struct {
	CommunityIdentity
	Conversations []CommunityConversation `json:"conversations"`
	NextCursor    int64                   `json:"next_cursor"`
	Revision      uint64                  `json:"revision"`
}

func (s *Service) CommunityConversations(ctx context.Context, req CommunityConversationsRequest) (CommunityConversationsPage, error) {
	limit, err := communityPageLimit(req.Limit)
	if err != nil || req.Cursor < 0 || len(req.Query) > 1024 || !utf8.ValidString(req.Query) || req.Kind != "" && req.Kind != "private" && req.Kind != "room" {
		return CommunityConversationsPage{}, errors.New("community: invalid conversation page")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityConversationsPage{}, err
	}
	out := CommunityConversationsPage{CommunityIdentity: req.CommunityIdentity, Conversations: []CommunityConversation{}}
	err = s.stateDB.ReadSnapshot(ctx, func(tx *storage.ReadTx) error {
		q := tx.Queries()
		account, err := q.GetCommunityAccount(ctx, req.Account)
		if err != nil {
			return err
		}
		out.Revision = s.community.revision + uint64(account.Revision)
		var includeClosed int64
		if req.IncludeClosed {
			includeClosed = 1
		}
		rows, err := q.PageCommunityConversations(ctx, db.PageCommunityConversationsParams{Account: req.Account, AfterID: req.Cursor,
			PageSize: limit, Kind: req.Kind, SearchText: req.Query, IncludeClosed: includeClosed})
		if err != nil {
			return err
		}
		conversations, more, err := takePage(func(yield func(CommunityConversation) bool) {
			for _, row := range rows {
				if !yield(communityConversation(row)) {
					return
				}
			}
		}, int(limit), communityPageBytes)
		if err != nil {
			return err
		}
		out.Conversations = conversations
		if more || int64(len(rows)) == limit {
			out.NextCursor = conversations[len(conversations)-1].ID
		}
		return nil
	})
	return out, err
}

type CommunityOpenConversationRequest struct {
	CommunityIdentity
	Username string `json:"username"`
	Room     string `json:"room,omitempty"` // Mutually exclusive with Username; opening history never joins.
}

func (s *Service) OpenCommunityConversation(ctx context.Context, req CommunityOpenConversationRequest) (CommunityConversation, error) {
	kind, target := "private", req.Username
	if req.Room != "" {
		if req.Username != "" {
			return CommunityConversation{}, errors.New("community: select either username or room")
		}
		if err := soulseek.ValidateRoomName(req.Room); err != nil {
			return CommunityConversation{}, err
		}
		kind, target = "room", req.Room
	} else if err := soulseek.ValidateUsername(req.Username); err != nil {
		return CommunityConversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityConversation{}, err
	}
	var out CommunityConversation
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		row, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{Account: req.Account, Kind: kind, Target: target})
		if err != nil {
			return err
		}
		if _, err = q.SetCommunityConversationClosed(ctx, db.SetCommunityConversationClosedParams{Account: req.Account, ID: row.ID}); err != nil {
			return err
		}
		if _, err = q.BumpCommunityRevision(ctx, req.Account); err != nil {
			return err
		}
		out, err = getCommunityConversationSummary(ctx, q, req.Account, row.ID)
		return err
	})
	if err == nil {
		if kind == "private" {
			s.watchConversationLocked(target, true)
		} else {
			r := s.community.rooms[target]
			if r == nil {
				r = &communityRoomState{}
				s.community.rooms[target] = r
			}
			r.conversationID = out.ID
		}
	}
	return out, err
}

func communityConversation(row db.PageCommunityConversationsRow) CommunityConversation {
	return CommunityConversation{ID: row.ID, Kind: row.Kind, Target: row.Target, Closed: row.Closed != 0,
		ReadThrough: row.ReadThrough, Unread: row.Unread, Mentions: row.Mentions, LatestID: row.LatestID}
}

func getCommunityConversationSummary(ctx context.Context, q *db.Queries, account string, id int64) (CommunityConversation, error) {
	rows, err := q.PageCommunityConversations(ctx, db.PageCommunityConversationsParams{Account: account, AfterID: id - 1, PageSize: 1, IncludeClosed: 1})
	if err != nil {
		return CommunityConversation{}, err
	}
	if len(rows) == 0 || rows[0].ID != id {
		return CommunityConversation{}, sql.ErrNoRows
	}
	return communityConversation(rows[0]), nil
}

type CommunityMessagesRequest struct {
	CommunityIdentity
	ConversationID int64  `json:"conversation_id"`
	Cursor         int64  `json:"cursor"` // Read older local IDs, in descending order; zero starts at newest.
	Limit          int    `json:"limit"`
	Query          string `json:"query"`
	NewerThan      int64  `json:"newer_than"` // The frontend's newest loaded ID; counts arrivals without moving its anchor.
}

type CommunityMessagesPage struct {
	CommunityIdentity
	Conversation CommunityConversation `json:"conversation"`
	Messages     []CommunityMessage    `json:"messages"`
	NextCursor   int64                 `json:"next_cursor"`
	NewerCount   int64                 `json:"newer_count"`
	Revision     uint64                `json:"revision"`
}

func (s *Service) CommunityMessages(ctx context.Context, req CommunityMessagesRequest) (CommunityMessagesPage, error) {
	limit, err := communityPageLimit(req.Limit)
	if err != nil || req.ConversationID <= 0 || req.Cursor < 0 || req.NewerThan < 0 || len(req.Query) > 1024 || !utf8.ValidString(req.Query) {
		return CommunityMessagesPage{}, errors.New("community: invalid message page")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityMessagesPage{}, err
	}
	out := CommunityMessagesPage{CommunityIdentity: req.CommunityIdentity, Messages: []CommunityMessage{}}
	err = s.stateDB.ReadSnapshot(ctx, func(tx *storage.ReadTx) error {
		q := tx.Queries()
		account, err := q.GetCommunityAccount(ctx, req.Account)
		if err != nil {
			return err
		}
		out.Revision = s.community.revision + uint64(account.Revision)
		conversation, err := getCommunityConversationSummary(ctx, q, req.Account, req.ConversationID)
		if err != nil {
			return err
		}
		info, err := q.CommunityHistoryInfo(ctx, db.CommunityHistoryInfoParams{Account: req.Account, ConversationID: req.ConversationID, NewerThan: req.NewerThan})
		if err != nil {
			return err
		}
		out.Conversation = conversation
		out.NewerCount = info.NewerCount
		rows, err := q.ListCommunityMessages(ctx, db.ListCommunityMessagesParams{Account: req.Account, ConversationID: req.ConversationID,
			BeforeID: req.Cursor, PageSize: limit, SearchText: req.Query})
		if err != nil {
			return err
		}
		messages, more, err := takePage(func(yield func(CommunityMessage) bool) {
			for _, row := range rows {
				if !yield(communityMessage(row)) {
					return
				}
			}
		}, int(limit), communityPageBytes)
		if err != nil {
			return err
		}
		out.Messages = messages
		if more || int64(len(rows)) == limit {
			out.NextCursor = messages[len(messages)-1].ID
		}
		return nil
	})
	return out, err
}

type CommunityConversationActionRequest struct {
	CommunityIdentity
	ConversationID int64  `json:"conversation_id"`
	Action         string `json:"action"` // read, close, or clear through ThroughID; repeated clears cannot erase later arrivals.
	ThroughID      int64  `json:"through_id"`
	Confirm        bool   `json:"confirm"`
}

func (s *Service) CommunityConversationAction(ctx context.Context, req CommunityConversationActionRequest) error {
	if req.ConversationID <= 0 || req.ThroughID < 0 || req.Action != "read" && req.Action != "close" && req.Action != "clear" {
		return errors.New("community: invalid conversation action")
	}
	if req.Action == "clear" && !req.Confirm {
		return errors.New("community: clearing history requires confirmation; unresolved outgoing messages are preserved")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return err
	}
	var conversation db.CommunityConversation
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		var err error
		conversation, err = q.GetCommunityConversation(ctx, db.GetCommunityConversationParams{Account: req.Account, ID: req.ConversationID})
		if err != nil {
			return err
		}
		var changed int64
		switch req.Action {
		case "read":
			changed, err = q.MarkCommunityRead(ctx, db.MarkCommunityReadParams{Account: req.Account, ConversationID: req.ConversationID, ThroughID: req.ThroughID})
			if err == nil && changed == 0 {
				return ErrCommunityMessageState
			}
		case "close":
			_, err = q.SetCommunityConversationClosed(ctx, db.SetCommunityConversationClosedParams{Account: req.Account, ID: req.ConversationID, Closed: 1})
		case "clear":
			_, err = q.ClearCommunityHistoryThrough(ctx, db.ClearCommunityHistoryThroughParams{Account: req.Account, ConversationID: req.ConversationID, ThroughID: req.ThroughID})
		}
		if err != nil {
			return err
		}
		_, err = q.BumpCommunityRevision(ctx, req.Account)
		return err
	})
	if err == nil && req.Action == "close" && conversation.Kind == "private" {
		s.watchConversationLocked(conversation.Target, false)
	}
	return err
}
