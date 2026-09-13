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

type AccountPrivilegeGiftRequest struct {
	CommunityIdentity
	RequestID string `json:"request_id"`
	Username  string `json:"username"`
	Days      uint32 `json:"days"`
	Revision  uint64 `json:"revision"`
	Confirm   bool   `json:"confirm"`
}
type AccountPrivilegeGiftResult struct {
	CommunityIdentity
	RequestID string            `json:"request_id"`
	Username  string            `json:"username"`
	Days      uint32            `json:"days"`
	State     string            `json:"state"`
	Message   string            `json:"message"`
	Duplicate bool              `json:"duplicate"`
	Balance   AccountPrivileges `json:"balance"`
}

var errPrivilegeGiftDuplicate = errors.New("privilege gift already submitted")

func privilegeGiftSubmission(ctx context.Context, q *db.Queries, req AccountPrivilegeGiftRequest, fingerprint [32]byte, out *AccountPrivilegeGiftResult) (bool, error) {
	old, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if old.Kind != "privilege-gift" || !bytes.Equal(old.Fingerprint, fingerprint[:]) {
		return false, errors.New("community: request_id already used for another action")
	}
	if err := json.Unmarshal([]byte(old.Result), out); err != nil {
		return false, err
	}
	out.CommunityIdentity = req.CommunityIdentity
	out.Duplicate = true
	return true, nil
}
func (s *Service) checkPrivilegeGiftLocked(req AccountPrivilegeGiftRequest) error {
	if req.Username == s.cfg.Soulseek.Username {
		return errors.New("cannot gift privileges to yourself")
	}
	balance := s.accountPrivilegesLocked()
	if !balance.Fresh || balance.Pending || s.client == nil {
		return fmt.Errorf("refresh privilege balance before gifting: %w", ErrCommunityMessageState)
	}
	if req.Confirm && req.Revision != balance.Revision {
		return fmt.Errorf("privilege balance changed; preview again: %w", ErrCommunityMessageState)
	}
	if uint64(req.Days)*86400 > uint64(balance.Seconds) {
		return errors.New("insufficient whole days of supporter privileges")
	}
	return nil
}

func (s *Service) GiftAccountPrivileges(ctx context.Context, req AccountPrivilegeGiftRequest) (AccountPrivilegeGiftResult, error) {
	out := AccountPrivilegeGiftResult{CommunityIdentity: req.CommunityIdentity, RequestID: req.RequestID, Username: req.Username, Days: req.Days}
	if err := validateCommunityRequestID(req.RequestID); err != nil {
		return out, err
	}
	if _, err := soulseek.EncodeMessage(soulseek.GivePrivilegesRequest{Username: req.Username, Days: req.Days}); err != nil {
		return out, err
	}
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", req.Username, req.Days)))
	s.mu.Lock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		s.mu.Unlock()
		return out, err
	}
	if found, err := privilegeGiftSubmission(ctx, s.stateDB.Queries(), req, fingerprint, &out); found || err != nil {
		s.mu.Unlock()
		return out, err
	}
	if err := s.checkPrivilegeGiftLocked(req); err != nil {
		s.mu.Unlock()
		return out, err
	}
	out.Balance = s.accountPrivilegesLocked()
	if !req.Confirm {
		out.State = "preview"
		out.Message = "Confirm the exact recipient and whole days. Gifts have no server acknowledgement."
		s.mu.Unlock()
		return out, nil
	}
	client, sessionCtx := s.client, s.scanCtx
	s.mu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(sessionCtx, cancel)
	defer stop()
	reserved := false
	attempted, sendErr := client.GivePrivileges(writeCtx, req.Username, req.Days, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.communityCurrentLocked(req.CommunityIdentity) || s.client != client {
			return ErrCommunitySession
		}
		if found, err := privilegeGiftSubmission(writeCtx, s.stateDB.Queries(), req, fingerprint, &out); err != nil {
			return err
		} else if found {
			return errPrivilegeGiftDuplicate
		}
		if err := s.checkPrivilegeGiftLocked(req); err != nil {
			return err
		}
		out.Balance = s.accountPrivilegesLocked()
		out.State = "unknown"
		out.Balance.Fresh = false
		out.Message = "Gift outcome unknown: the server provides no acknowledgement. Refresh balance and check server notices in Chats. Never retry automatically."
		encoded, err := json.Marshal(out)
		if err != nil {
			return err
		}
		err = s.stateDB.WriteTx(writeCtx, func(tx *sql.Tx) error {
			rows, err := db.New(tx).InsertCommunitySubmission(writeCtx, db.InsertCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID, Kind: "privilege-gift", Fingerprint: fingerprint[:], Result: string(encoded), CreatedAt: time.Now().UnixMilli()})
			if err == nil && rows != 1 {
				return errPrivilegeGiftDuplicate
			}
			return err
		})
		if err != nil {
			return err
		}
		reserved = true
		p := &s.community.privileges
		p.giftActive = true
		p.fresh = false
		p.revision++
		return nil
	})
	if errors.Is(sendErr, errPrivilegeGiftDuplicate) {
		return out, nil
	}
	if !reserved {
		return out, sendErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.communityCurrentLocked(req.CommunityIdentity) && s.client == client {
		p := &s.community.privileges
		p.giftActive = false
		p.fresh = false
		p.revision++
	}
	if !attempted {
		failed := out
		failed.State = "failed"
		failed.Message = "Gift was not written. Refresh balance and explicitly start a new preview to try again."
		encoded, _ := json.Marshal(failed)
		saveCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		err := s.stateDB.WriteTx(saveCtx, func(tx *sql.Tx) error {
			_, err := db.New(tx).SetCommunitySubmissionResult(saveCtx, db.SetCommunitySubmissionResultParams{Account: req.Account, RequestID: req.RequestID, Result: string(encoded)})
			return err
		})
		if err == nil {
			out = failed
		}
	}
	return out, nil
}
