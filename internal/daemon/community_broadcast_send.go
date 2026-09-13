package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/catgirl-systems/oto/internal/storage/db"
)

type communityBroadcastRun struct {
	requestID string
	cancel    context.CancelFunc
}
type CommunityBroadcastAction struct {
	CommunityIdentity
	RequestID string `json:"request_id"`
	Token     string `json:"token"`
	Action    string `json:"action"` // send or stop; stopping does not withdraw already queued PMs.
	Confirm   bool   `json:"confirm"`
}

func broadcastChildID(b communityBroadcast, user string) string {
	sum := sha256.Sum256([]byte(b.Token + "\x00" + user))
	return "broadcast-" + hex.EncodeToString(sum[:])
}
func saveBroadcast(ctx context.Context, q *db.Queries, b communityBroadcast) error {
	encoded, err := json.Marshal(b)
	if err != nil {
		return err
	}
	n, err := q.SetCommunitySubmissionResult(ctx, db.SetCommunitySubmissionResultParams{Account: b.Identity.Account, RequestID: b.RequestID, Result: string(encoded)})
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("broadcast receipt is missing")
	}
	return nil
}

// Reconcile from ordinary PM receipts, including a crash between enqueue and
// broadcast progress persistence. Never resubmit a child to discover its state.
func refreshBroadcastRecipient(ctx context.Context, q *db.Queries, b communityBroadcast, row *CommunityBroadcastRecipient) error {
	submission, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: b.Identity.Account, RequestID: broadcastChildID(b, row.Username)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	expected := communityPrivateFingerprint(row.Username, b.Text, true)
	if submission.Kind != "private-message" || !bytes.Equal(submission.Fingerprint, expected[:]) {
		row.MessageID = 0
		row.State = "failed"
		row.Error = "Request ID conflict; no broadcast message submitted"
		return nil
	}
	var sent CommunitySendResult
	if err := json.Unmarshal([]byte(submission.Result), &sent); err != nil {
		return err
	}
	row.MessageID = sent.MessageID
	message, err := q.GetCommunityMessage(ctx, db.GetCommunityMessageParams{Account: b.Identity.Account, ID: sent.MessageID})
	if errors.Is(err, sql.ErrNoRows) {
		row.State = "cleared"
		return nil
	}
	if err != nil {
		return err
	}
	row.State = message.State
	row.Error = message.Error
	return nil
}
func (s *Service) ActCommunityBroadcast(ctx context.Context, req CommunityBroadcastAction) (CommunityBroadcastPage, error) {
	if err := validateCommunityRequestID(req.RequestID); err != nil {
		return CommunityBroadcastPage{}, err
	}
	if req.Action != "send" && req.Action != "stop" {
		return CommunityBroadcastPage{}, errors.New("choose send or stop")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityBroadcastPage{}, err
	}
	b, err := loadBroadcast(ctx, s.stateDB.Queries(), req.Account, req.RequestID)
	if err != nil {
		return CommunityBroadcastPage{}, err
	}
	if b.State == "running" && (b.Identity != req.CommunityIdentity || s.community.broadcast == nil || s.community.broadcast.requestID != req.RequestID) {
		b.State = "interrupted"
	}
	if req.Token == "" || req.Token != b.Token {
		return CommunityBroadcastPage{}, errors.New("broadcast changed; reload preview")
	}
	if !req.Confirm {
		return broadcastPage(req.CommunityIdentity, b, 0)
	}
	if req.Action == "stop" {
		if b.State == "preview" {
			b.State = "stopped"
			for i := range b.Recipients {
				b.Recipients[i].State = "not-submitted"
			}
			if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error { return saveBroadcast(ctx, db.New(tx), b) }); err != nil {
				return CommunityBroadcastPage{}, err
			}
		}
		if run := s.community.broadcast; run != nil && run.requestID == req.RequestID {
			run.cancel()
		}
		return broadcastPage(req.CommunityIdentity, b, 0)
	}
	if b.State != "preview" {
		return broadcastPage(req.CommunityIdentity, b, 0)
	} // Confirmation retries never rebroadcast.
	if b.Identity != req.CommunityIdentity || time.Since(b.CreatedAt) > 5*time.Minute {
		return CommunityBroadcastPage{}, errors.New("broadcast preview expired; create a new preview")
	}
	if !s.community.online || s.client == nil || s.ctx == nil {
		return CommunityBroadcastPage{}, errors.New("connect before starting a broadcast")
	}
	if s.community.broadcast != nil {
		return CommunityBroadcastPage{}, errors.New("another broadcast is running; stop it or wait")
	}
	if _, err := communityOutgoingText(b.Text); err != nil {
		return CommunityBroadcastPage{}, err
	}
	b.State = "running"
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error { return saveBroadcast(ctx, db.New(tx), b) }); err != nil {
		return CommunityBroadcastPage{}, err
	}
	work, cancel := context.WithTimeout(s.ctx, 4*time.Hour)
	run := &communityBroadcastRun{requestID: req.RequestID, cancel: cancel}
	s.community.broadcast = run
	s.wg.Add(1)
	go s.runCommunityBroadcast(work, run, b)
	return broadcastPage(req.CommunityIdentity, b, 0)
}
func (s *Service) runCommunityBroadcast(ctx context.Context, run *communityBroadcastRun, b communityBroadcast) {
	defer s.wg.Done()
	defer run.cancel()
	defer func() {
		s.mu.Lock()
		if s.community.broadcast == run {
			s.community.broadcast = nil
		}
		s.mu.Unlock()
	}()
	b.State = "stopped"
	stopped := false
	for i := range b.Recipients {
		if ctx.Err() != nil {
			stopped = true
			break
		}
		row := &b.Recipients[i]
		out, err := s.sendCommunityPrivate(ctx, CommunitySendRequest{CommunityIdentity: b.Identity, RequestID: broadcastChildID(b, row.Username), Username: row.Username, Text: b.Text}, true)
		if err != nil {
			row.State = "failed"
			row.Error = "Submission failed; inspect private history before retrying"
			stopped = true
			break
		}
		row.MessageID, row.State = out.MessageID, out.State
		for row.State == "queued" || row.State == "sending" {
			if err := refreshBroadcastRecipient(ctx, s.stateDB.Queries(), b, row); err != nil {
				stopped = true
				break
			}
			select {
			case <-ctx.Done():
				stopped = true
			case <-time.After(100 * time.Millisecond):
			}
			if stopped {
				break
			}
		}
		if stopped {
			break
		}
		// Persist each terminal result before admitting another recipient. At most
		// one child per broadcast can remain in the ordinary durable PM outbox.
		if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
			progress := b
			progress.State = "running"
			return saveBroadcast(ctx, db.New(tx), progress)
		}); err != nil {
			stopped = true
			break
		}
		if i+1 < len(b.Recipients) {
			select {
			case <-ctx.Done():
				stopped = true
			case <-time.After(time.Second):
			}
		}
		if stopped {
			break
		}
	}
	if !stopped {
		b.State = "completed"
	}
	// Session loss / shutdown stops the remaining audience. There is deliberately
	// no resume worker: committed children retain normal PM history and controls.
	finish, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i := range b.Recipients {
		if b.Recipients[i].State == "preview" {
			b.Recipients[i].State = "not-submitted"
		}
	}
	err := s.stateDB.WriteTx(finish, func(tx *sql.Tx) error { return saveBroadcast(finish, db.New(tx), b) })
	if err != nil {
		s.mu.Lock()
		if s.community.identity.Account == b.Identity.Account {
			s.lastErr = "Broadcast progress could not be saved; inspect private history before retrying"
		}
		s.mu.Unlock()
	}
}
