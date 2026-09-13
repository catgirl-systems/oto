package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

type SharedSendAction struct {
	CommunityIdentity
	RequestID string `json:"request_id"`
	Token     string `json:"token"`
	Action    string `json:"action"`
	Confirm   bool   `json:"confirm"`
}
type sharedSendRun struct {
	requestID string
	cancel    context.CancelFunc
}

func saveSharedSend(ctx context.Context, q *db.Queries, b sharedSend) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	n, err := q.SetCommunitySubmissionResult(ctx, db.SetCommunitySubmissionResultParams{Account: b.Identity.Account, RequestID: b.RequestID, Result: string(data)})
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("shared-send receipt missing")
	}
	return nil
}
func (s *Service) ActSharedSend(ctx context.Context, req SharedSendAction) (SharedSendPage, error) {
	if err := validateCommunityRequestID(req.RequestID); err != nil {
		return SharedSendPage{}, err
	}
	if req.Action != "send" && req.Action != "stop" {
		return SharedSendPage{}, errors.New("choose send or stop")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return SharedSendPage{}, err
	}
	b, err := loadSharedSend(ctx, s.stateDB.Queries(), req.Account, req.RequestID)
	if err != nil {
		return SharedSendPage{}, err
	}
	if b.State == "running" && (b.Identity != req.CommunityIdentity || s.community.sharedSend == nil || s.community.sharedSend.requestID != req.RequestID) {
		b.State = "interrupted"
	}
	if req.Token == "" || req.Token != b.Token {
		return SharedSendPage{}, errors.New("shared-send preview changed; reload")
	}
	if !req.Confirm {
		return sharedSendPage(req.CommunityIdentity, b, 0)
	}
	if req.Action == "stop" {
		if run := s.community.sharedSend; run != nil && run.requestID == req.RequestID {
			run.cancel()
		}
		if b.State == "preview" {
			b.State = "stopped"
			for i := range b.Files {
				if b.Files[i].File.State == "preview" {
					b.Files[i].File.State = "not-submitted"
				}
			}
			if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error { return saveSharedSend(ctx, db.New(tx), b) }); err != nil {
				return SharedSendPage{}, err
			}
		}
		return sharedSendPage(req.CommunityIdentity, b, 0)
	}
	if b.State != "preview" {
		return sharedSendPage(req.CommunityIdentity, b, 0)
	}
	if b.Identity != req.CommunityIdentity || time.Since(b.CreatedAt) > 5*time.Minute {
		return SharedSendPage{}, errors.New("shared-send preview expired; create a new preview")
	}
	if b.Eligible == 0 {
		return SharedSendPage{}, errors.New("no permitted files in this preview")
	}
	if s.client == nil || s.ctx == nil || !s.community.online {
		return SharedSendPage{}, errors.New("connect before sending files")
	}
	if s.community.sharedSend != nil {
		return SharedSendPage{}, errors.New("another shared-send submission is running")
	}
	b.State = "running"
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error { return saveSharedSend(ctx, db.New(tx), b) }); err != nil {
		return SharedSendPage{}, err
	}
	out, err := sharedSendPage(req.CommunityIdentity, b, 0)
	if err != nil {
		return out, err
	}
	work, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
	run := &sharedSendRun{requestID: req.RequestID, cancel: cancel}
	s.community.sharedSend = run
	s.wg.Add(1)
	go s.runSharedSend(work, run, s.client, s.uploadEpoch, b)
	return out, nil
}
func (s *Service) runSharedSend(ctx context.Context, run *sharedSendRun, client *soulseek.Client, epoch uint64, b sharedSend) {
	defer s.wg.Done()
	defer run.cancel()
	defer func() {
		s.mu.Lock()
		if s.community.sharedSend == run {
			s.community.sharedSend = nil
		}
		s.mu.Unlock()
	}()
	stopped := false
	for i := range b.Files {
		item := &b.Files[i]
		if item.File.State != "preview" {
			continue
		}
		if ctx.Err() != nil {
			stopped = true
			break
		}
		s.mu.RLock()
		current := s.communityCurrentLocked(b.Identity) && s.client == client && s.uploadEpoch == epoch && !s.shuttingDown
		s.mu.RUnlock()
		if !current {
			stopped = true
			break
		}
		// Admission itself has a durable upload journal. Persist uncertainty first:
		// a crash must never cause this batch to enqueue the same file again.
		item.File.State = "unknown"
		if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error { return saveSharedSendFile(ctx, db.New(tx), b, *item) }); err != nil {
			stopped = true
			break
		}
		target, _, err := client.QueueUploadSnapshot(b.Username, soulseek.UploadSnapshot{Filename: item.File.Filename, Size: item.File.Size, Fingerprint: item.Fingerprint})
		if err != nil {
			item.File.State = "failed"
			item.File.Error = "Admission rejected: file changed, permission revoked, queue limited, or connection unavailable"
		} else {
			s.mu.RLock()
			id := s.uploadKeys[uploadKey(s.uploadAccountLocked(epoch), b.Username, target.Filename)]
			owner := s.uploadOwners[id]
			if owner.session == epoch && owner.target == target {
				item.File.UploadID = id
				item.File.State = "queued"
			}
			s.mu.RUnlock()
		}
		if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error { return saveSharedSendFile(ctx, db.New(tx), b, *item) }); err != nil {
			stopped = true
			break
		}
	}
	b.State = "completed"
	if stopped {
		b.State = "stopped"
	}
	for i := range b.Files {
		if b.Files[i].File.State == "preview" {
			b.Files[i].File.State = "not-submitted"
		}
	}
	finish, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.stateDB.WriteTx(finish, func(tx *sql.Tx) error { return saveSharedSend(finish, db.New(tx), b) }); err != nil {
		s.mu.Lock()
		if s.community.identity.Account == b.Identity.Account {
			s.lastErr = "Shared-send progress could not be saved; inspect upload history before retrying"
		}
		s.mu.Unlock()
	}
}
