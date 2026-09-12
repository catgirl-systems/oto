package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

type CommunityRoomRoleRequest struct {
	CommunityIdentity
	Room      string                  `json:"room"`
	Action    soulseek.RoomRoleAction `json:"action"`
	Username  string                  `json:"username"`
	RequestID string                  `json:"request_id"`
	Revision  uint64                  `json:"revision"`
	Confirm   bool                    `json:"confirm"`
}
type CommunityRoomRoleResult struct {
	CommunityIdentity
	Room      string                  `json:"room"`
	Action    soulseek.RoomRoleAction `json:"action"`
	Username  string                  `json:"username"`
	RequestID string                  `json:"request_id"`
	State     string                  `json:"state"`
	Duplicate bool                    `json:"duplicate"`
}
type communityRoomRoleMutation struct {
	CommunityRoomRoleResult
	deadline time.Time
}

func (s *Service) checkRoomRoleActionLocked(req CommunityRoomRoleRequest) error {
	r := s.community.rooms[req.Room]
	if s.shuttingDown {
		return ErrClosed
	}
	if !s.community.online {
		return soulseek.ErrNotConnected
	}
	if r == nil || !r.private || !r.roleFresh || r.role == "none" || r.role == "" {
		return errors.New("community: private-room role is not confirmed")
	}
	if r.roleMutation != nil && r.roleMutation.State == "pending" {
		return errors.New("community: previous room role action awaits confirmation")
	}
	owner := r.role == "owner"
	switch req.Action {
	case soulseek.RoomCancelOwnership:
		if owner {
			return nil
		}
	case soulseek.RoomCancelMembership:
		if !owner {
			return nil
		}
	case soulseek.RoomAddMember, soulseek.RoomRemoveMember, soulseek.RoomAddOperator, soulseek.RoomRemoveOperator:
		if req.Username == s.cfg.Soulseek.Username || req.Username == r.owner {
			return errors.New("community: use the explicit relinquish action for yourself; the owner cannot be removed")
		}
		if !owner && r.role != "operator" {
			break
		}
		if req.Action == soulseek.RoomAddOperator || req.Action == soulseek.RoomRemoveOperator {
			if !owner {
				break
			}
			if req.Action == soulseek.RoomAddOperator && (!r.privateMembersFresh || !r.privateMembers[req.Username]) {
				return errors.New("community: select a confirmed private-room member before granting operator privileges")
			}
		}
		if req.Action == soulseek.RoomRemoveMember && !owner && (!r.operatorsFresh || slices.Contains(r.operators, req.Username)) {
			return errors.New("community: operators can remove only confirmed regular members")
		}
		return nil
	}
	return errors.New("community: your confirmed room role does not permit this action")
}

func communityRoomRoleSubmission(ctx context.Context, q *db.Queries, req CommunityRoomRoleRequest, fingerprint [32]byte, out *CommunityRoomRoleResult) (bool, error) {
	previous, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if previous.Kind != "room-role" || !bytes.Equal(previous.Fingerprint, fingerprint[:]) {
		return false, errors.New("community: request_id already used for another action")
	}
	if err = json.Unmarshal([]byte(previous.Result), out); err != nil {
		return false, err
	}
	out.CommunityIdentity, out.Duplicate = req.CommunityIdentity, true
	return true, nil
}

func (s *Service) ChangeCommunityRoomRole(ctx context.Context, req CommunityRoomRoleRequest) (CommunityRoomRoleResult, error) {
	out := CommunityRoomRoleResult{CommunityIdentity: req.CommunityIdentity, Room: req.Room, Action: req.Action, Username: req.Username, RequestID: req.RequestID}
	wire := soulseek.RoomRoleRequest{Room: req.Room, Action: req.Action, Username: req.Username}
	if _, err := soulseek.EncodeMessage(wire); err != nil {
		return out, err
	}
	if err := validateCommunityRequestID(req.RequestID); err != nil {
		return out, err
	}
	if !req.Confirm {
		return out, errors.New("community: room role changes require explicit confirmation")
	}
	fingerprint := sha256.Sum256([]byte(req.Room + "\x00" + string(req.Action) + "\x00" + req.Username))
	s.mu.Lock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		s.mu.Unlock()
		return out, err
	}
	if found, err := communityRoomRoleSubmission(ctx, s.stateDB.Queries(), req, fingerprint, &out); found || err != nil {
		if found {
			if r := s.community.rooms[req.Room]; r != nil && r.roleMutation != nil && r.roleMutation.RequestID == req.RequestID {
				out = r.roleMutation.CommunityRoomRoleResult
				out.CommunityIdentity = req.CommunityIdentity
				out.Duplicate = true
			}
		}
		s.mu.Unlock()
		return out, err
	}
	account, err := s.stateDB.Queries().GetCommunityAccount(ctx, req.Account)
	if err == nil && req.Revision != s.community.revision+uint64(account.Revision) {
		err = ErrCommunityMessageState
	}
	if err == nil {
		err = s.checkRoomRoleActionLocked(req)
	}
	client, sessionCtx := s.client, s.ctx
	s.mu.Unlock()
	if err != nil {
		return out, err
	}
	if client == nil {
		return out, soulseek.ErrNotConnected
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if sessionCtx != nil {
		stop := context.AfterFunc(sessionCtx, cancel)
		defer stop()
	}
	reserved := false
	attempted, sendErr := client.ChangeRoomRole(writeCtx, wire, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.communityCurrentLocked(req.CommunityIdentity) || s.client != client {
			return ErrCommunitySession
		}
		err := s.stateDB.WriteTx(writeCtx, func(tx *sql.Tx) error {
			q := db.New(tx)
			if found, err := communityRoomRoleSubmission(writeCtx, q, req, fingerprint, &out); err != nil {
				return err
			} else if found {
				return errCommunityRoomDuplicate
			}
			account, err := q.GetCommunityAccount(writeCtx, req.Account)
			if err != nil {
				return err
			}
			if req.Revision != s.community.revision+uint64(account.Revision) {
				return ErrCommunityMessageState
			}
			if err := s.checkRoomRoleActionLocked(req); err != nil {
				return err
			}
			out.State = "unknown" // A crash after reservation never automatically repeats a role change.
			encoded, err := json.Marshal(out)
			if err != nil {
				return err
			}
			_, err = q.InsertCommunitySubmission(writeCtx, db.InsertCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID, Kind: "room-role", Fingerprint: fingerprint[:], Result: string(encoded), CreatedAt: time.Now().UnixMilli()})
			return err
		})
		if err != nil {
			return err
		}
		reserved = true
		pending := out
		pending.State = "pending"
		s.community.rooms[req.Room].roleMutation = &communityRoomRoleMutation{CommunityRoomRoleResult: pending, deadline: time.Now().Add(15 * time.Second)}
		s.community.revision++
		return nil
	})
	if errors.Is(sendErr, errCommunityRoomDuplicate) {
		return out, nil
	}
	if !reserved {
		return out, sendErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.communityCurrentLocked(req.CommunityIdentity) || s.client != client {
		out.State = "unknown"
		return out, nil
	}
	mutation := s.community.rooms[req.Room].roleMutation
	if mutation == nil || mutation.RequestID != req.RequestID {
		out.State = "unknown"
		return out, nil
	}
	if mutation.State == "pending" && sendErr != nil {
		mutation.State = "unknown"
		if !attempted {
			mutation.State = "failed"
		}
		s.community.revision++
	}
	if sendErr == nil && (req.Action == soulseek.RoomCancelOwnership || req.Action == soulseek.RoomCancelMembership) {
		s.community.directoryRefresh = true
		s.wakeCommunityOutboxLocked()
	}
	return mutation.CommunityRoomRoleResult, nil
}

func (s *Service) finishCommunityRoomRoleLocked(ctx context.Context, r *communityRoomState, state string) error {
	if r.roleMutation == nil || r.roleMutation.State != "pending" && r.roleMutation.State != "unknown" {
		return nil
	}
	next := r.roleMutation.CommunityRoomRoleResult
	next.State = state
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	err = s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		n, err := q.SetCommunitySubmissionResult(ctx, db.SetCommunitySubmissionResultParams{Account: next.Account, RequestID: next.RequestID, Result: string(encoded)})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("community: role submission missing")
		}
		_, err = q.BumpCommunityRevision(ctx, next.Account)
		return err
	})
	if err == nil {
		r.roleMutation.CommunityRoomRoleResult = next
	}
	return err
}
