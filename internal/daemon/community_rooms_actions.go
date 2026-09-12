package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

type CommunityRoomActionRequest struct {
	CommunityIdentity
	Room      string `json:"room"`
	Action    string `json:"action"`   // join, leave, remember, forget; close/clear use conversation operations.
	Remember  bool   `json:"remember"` // Joining can also opt in to remembering; false does not forget an existing preference.
	Private   bool   `json:"private"`  // Explicit creation of a new private room.
	RequestID string `json:"request_id"`
	Revision  uint64 `json:"revision"` // Required for preference-only edits.
}
type CommunityRoomActionResult struct {
	CommunityIdentity
	Room      CommunityRoom `json:"room"`
	Duplicate bool          `json:"duplicate"`
}

func (s *Service) CommunityRoomAction(ctx context.Context, req CommunityRoomActionRequest) (CommunityRoomActionResult, error) {
	if err := soulseek.ValidateRoomName(req.Room); err != nil {
		return CommunityRoomActionResult{}, err
	}
	if err := validateCommunityRequestID(req.RequestID); err != nil {
		return CommunityRoomActionResult{}, err
	}
	if req.Action != "join" && req.Action != "leave" && req.Action != "remember" && req.Action != "forget" {
		return CommunityRoomActionResult{}, errors.New("community: invalid room action")
	}
	if req.Private && req.Action != "join" {
		return CommunityRoomActionResult{}, errors.New("community: private creation is a join action")
	}
	fingerprint := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s:%t", req.Room, req.Action, req.Remember))
	if req.Private {
		fingerprint = sha256.Sum256(fmt.Appendf(nil, "%s\x00%s:%t:private", req.Room, req.Action, req.Remember))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityRoomActionResult{}, err
	}
	out := CommunityRoomActionResult{CommunityIdentity: req.CommunityIdentity}
	var preference db.CommunityRoom
	var conversationID int64
	creating := false
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		previous, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID})
		if err == nil {
			if previous.Kind != "room-action" || !bytes.Equal(previous.Fingerprint, fingerprint[:]) {
				return errors.New("community: request_id was already used for a different submission")
			}
			out.Duplicate = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if req.Action == "join" && !s.community.online {
			return soulseek.ErrNotConnected
		}
		account, err := q.GetCommunityAccount(ctx, req.Account)
		if err != nil {
			return err
		}
		if (req.Action == "remember" || req.Action == "forget") && req.Revision != s.community.revision+uint64(account.Revision) {
			return ErrCommunityMessageState
		}
		preference, err = q.GetCommunityRoom(ctx, db.GetCommunityRoomParams{Account: req.Account, Room: req.Room})
		if errors.Is(err, sql.ErrNoRows) {
			preference = db.CommunityRoom{Account: req.Account, Room: req.Room}
		} else if err != nil {
			return err
		}
		r := s.community.rooms[req.Room]
		knownPrivate := preference.PrivateRoom != 0 || s.community.directory[req.Room].private || r != nil && r.private
		if req.Action == "join" && knownPrivate && (r == nil || !r.roleFresh || r.role == "none" || r.role == "") {
			return errors.New("community: private-room membership is not confirmed; refresh the directory or request an invitation")
		}
		creating = req.Action == "join" && req.Private && !knownPrivate
		if req.Private || knownPrivate {
			preference.PrivateRoom = 1
		}
		if req.Action == "remember" || req.Action == "join" && req.Remember {
			preference.Autojoin = 1
		}
		if req.Action == "forget" {
			preference.Autojoin = 0
		}
		if err = q.PutCommunityRoom(ctx, db.PutCommunityRoomParams{Account: req.Account, Room: req.Room, Autojoin: preference.Autojoin, PrivateRoom: preference.PrivateRoom, OwnWall: preference.OwnWall}); err != nil {
			return err
		}
		if req.Action == "join" {
			conversation, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{Account: req.Account, Kind: "room", Target: req.Room})
			if err != nil {
				return err
			}
			conversationID = conversation.ID
			if _, err = q.SetCommunityConversationClosed(ctx, db.SetCommunityConversationClosedParams{Account: req.Account, ID: conversation.ID}); err != nil {
				return err
			}
		}
		if _, err = q.InsertCommunitySubmission(ctx, db.InsertCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID, Kind: "room-action", Fingerprint: fingerprint[:], Result: `{}`, CreatedAt: time.Now().UnixMilli()}); err != nil {
			return err
		}
		_, err = q.BumpCommunityRevision(ctx, req.Account)
		return err
	})
	if err != nil {
		return CommunityRoomActionResult{}, err
	}
	if !out.Duplicate {
		r := s.community.rooms[req.Room]
		if r == nil {
			r = &communityRoomState{}
			s.community.rooms[req.Room] = r
		}
		r.autojoin = preference.Autojoin != 0
		r.private = preference.PrivateRoom != 0
		if req.Action == "join" {
			r.creating = creating
		}
		if conversationID != 0 {
			r.conversationID = conversationID
		}
		if req.Action == "join" || req.Action == "leave" {
			r.wanted = req.Action == "join"
			r.intent++
			r.err = ""
		}
		s.community.revision++
		s.wakeCommunityOutboxLocked()
	}
	out.Room = s.communityRoomLocked(req.Room)
	return out, nil
}

type CommunityRoomSendRequest struct {
	CommunityIdentity
	Room      string `json:"room"`
	Text      string `json:"text"`
	RequestID string `json:"request_id"`
}

var errCommunityRoomDuplicate = errors.New("community: room send already submitted")

func communityRoomSendSubmission(ctx context.Context, q *db.Queries, req CommunityRoomSendRequest, fingerprint [32]byte, out *CommunitySendResult) (bool, error) {
	previous, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if previous.Kind != "room-message" || !bytes.Equal(previous.Fingerprint, fingerprint[:]) {
		return false, errors.New("community: request_id was already used for a different submission")
	}
	if err = json.Unmarshal([]byte(previous.Result), out); err != nil {
		return false, err
	}
	out.CommunityIdentity, out.Duplicate = req.CommunityIdentity, true
	return true, nil
}

// Room sends never enter the offline PM outbox. Only a server echo enters the
// transcript; durable request receipts prevent duplicate writes on HTTP retries.
func (s *Service) SendCommunityRoom(ctx context.Context, req CommunityRoomSendRequest) (CommunitySendResult, error) {
	if err := soulseek.ValidateRoomName(req.Room); err != nil {
		return CommunitySendResult{}, err
	}
	if err := validateCommunityRequestID(req.RequestID); err != nil {
		return CommunitySendResult{}, err
	}
	var err error
	if req.Text, err = communityOutgoingText(req.Text); err != nil {
		return CommunitySendResult{}, err
	}
	fingerprint := sha256.Sum256([]byte(req.Room + "\x00" + req.Text))
	out := CommunitySendResult{CommunityIdentity: req.CommunityIdentity}
	s.mu.Lock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		s.mu.Unlock()
		return out, err
	}
	if found, err := communityRoomSendSubmission(ctx, s.stateDB.Queries(), req, fingerprint, &out); found || err != nil {
		s.mu.Unlock()
		return out, err
	}
	r := s.community.rooms[req.Room]
	client, sessionCtx := s.client, s.ctx
	if !s.community.online || client == nil {
		s.mu.Unlock()
		return out, soulseek.ErrNotConnected
	}
	if r == nil || !r.joined || !r.wanted || r.pending != "" {
		s.mu.Unlock()
		return out, errors.New("community: room send requires confirmed membership")
	}
	intent := r.intent
	out.ConversationID = r.conversationID
	s.mu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if sessionCtx != nil {
		stop := context.AfterFunc(sessionCtx, cancel)
		defer stop()
	}
	reserved := false
	attempted, sendErr := client.SendRoomMessage(writeCtx, req.Room, req.Text, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := s.checkCommunityIdentityLocked(writeCtx, req.CommunityIdentity); err != nil {
			return err
		}
		if !s.communityCurrentLocked(req.CommunityIdentity) || s.client != client {
			return ErrCommunitySession
		}
		err := s.stateDB.WriteTx(writeCtx, func(tx *sql.Tx) error {
			q := db.New(tx)
			if found, err := communityRoomSendSubmission(writeCtx, q, req, fingerprint, &out); err != nil {
				return err
			} else if found {
				return errCommunityRoomDuplicate
			}
			if r != s.community.rooms[req.Room] || r.intent != intent || !r.joined || !r.wanted || r.pending != "" {
				return ErrCommunityMessageState
			}
			// Unknown is the crash-safe reservation, not permission to auto-retry.
			out.State = "unknown"
			result, err := json.Marshal(out)
			if err != nil {
				return err
			}
			_, err = q.InsertCommunitySubmission(writeCtx, db.InsertCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID, Kind: "room-message", Fingerprint: fingerprint[:], Result: string(result), CreatedAt: time.Now().UnixMilli()})
			return err
		})
		reserved = err == nil
		return err
	})
	if errors.Is(sendErr, errCommunityRoomDuplicate) {
		return out, nil
	}
	if !reserved {
		return out, sendErr
	}
	if sendErr == nil {
		out.State = "sent"
	} else if !attempted {
		out.State = "failed"
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	result, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	err = s.stateDB.WriteTx(finishCtx, func(tx *sql.Tx) error {
		n, err := db.New(tx).SetCommunitySubmissionResult(finishCtx, db.SetCommunitySubmissionResultParams{Account: req.Account, RequestID: req.RequestID, Result: string(result)})
		if err == nil && n != 1 {
			return ErrCommunityMessageState
		}
		return err
	})
	if err != nil {
		return out, fmt.Errorf("community: save room write outcome: %w", err)
	}
	return out, sendErr
}
