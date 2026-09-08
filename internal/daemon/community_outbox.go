package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

type CommunitySendRequest struct {
	CommunityIdentity
	Username  string `json:"username"`
	Text      string `json:"text"`
	RequestID string `json:"request_id"`
}

type CommunitySendResult struct {
	CommunityIdentity
	ConversationID int64  `json:"conversation_id"`
	MessageID      int64  `json:"message_id"`
	State          string `json:"state"`
	Duplicate      bool   `json:"duplicate"`
}

func validateCommunityRequestID(id string) error {
	if len(id) > 128 || soulseek.ValidateUsername(id) != nil {
		return errors.New("community: request_id must be a nonempty identifier of at most 128 bytes")
	}
	return nil
}

func (s *Service) SendCommunityPrivate(ctx context.Context, req CommunitySendRequest) (CommunitySendResult, error) {
	if err := validateCommunityRequestID(req.RequestID); err != nil {
		return CommunitySendResult{}, err
	}
	if err := soulseek.ValidateUsername(req.Username); err != nil {
		return CommunitySendResult{}, err
	}
	// Nicotine+ privatechat.send_message also flattens line breaks: the server
	// may reject them in PMs as well as rooms. Persist the actual wire text.
	req.Text = strings.ReplaceAll(strings.ReplaceAll(req.Text, "\r\n", "\n"), "\n", " ")
	if err := soulseek.ValidateChatText(req.Text); err != nil {
		return CommunitySendResult{}, err
	}
	if strings.IndexFunc(req.Text, func(r rune) bool {
		return r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r))
	}) >= 0 {
		return CommunitySendResult{}, errors.New("community: chat text contains terminal controls")
	}
	fingerprint := sha256.Sum256([]byte(req.Username + "\x00" + req.Text))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunitySendResult{}, err
	}
	out := CommunitySendResult{CommunityIdentity: req.CommunityIdentity}
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		previous, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID})
		if err == nil {
			if previous.Kind != "private-message" || !bytes.Equal(previous.Fingerprint, fingerprint[:]) {
				return errors.New("community: request_id was already used for a different submission")
			}
			if err = json.Unmarshal([]byte(previous.Result), &out); err != nil {
				return err
			}
			out.CommunityIdentity, out.Duplicate = req.CommunityIdentity, true
			message, err := q.GetCommunityMessage(ctx, db.GetCommunityMessageParams{Account: req.Account, ID: out.MessageID})
			if errors.Is(err, sql.ErrNoRows) {
				out.State = "cleared"
				return nil
			}
			out.State = message.State
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		conversation, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{Account: req.Account, Kind: "private", Target: req.Username})
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMilli()
		message, err := q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: req.Account, ConversationID: conversation.ID,
			Sender: s.cfg.Soulseek.Username, Direction: "outgoing", Body: req.Text, State: "queued", CreatedAt: now})
		if err != nil {
			return err
		}
		out.ConversationID, out.MessageID, out.State = conversation.ID, message.ID, message.State
		result, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if _, err = q.InsertCommunitySubmission(ctx, db.InsertCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID,
			Kind: "private-message", Fingerprint: fingerprint[:], Result: string(result), CreatedAt: now}); err != nil {
			return err
		}
		if _, err = q.SetCommunityConversationClosed(ctx, db.SetCommunityConversationClosedParams{Account: req.Account, ID: conversation.ID}); err != nil {
			return err
		}
		_, err = q.BumpCommunityRevision(ctx, req.Account)
		return err
	})
	if err == nil && !out.Duplicate {
		s.watchConversationLocked(req.Username, true)
		s.wakeCommunityOutboxLocked()
	}
	return out, err
}

type CommunityMessageActionRequest struct {
	CommunityIdentity
	MessageID int64  `json:"message_id"`
	Action    string `json:"action"` // retry or cancel; a sending message cannot be cancelled safely.
	RequestID string `json:"request_id"`
	Confirm   bool   `json:"confirm"` // Retrying Unknown can duplicate a message at the recipient.
}

func (s *Service) CommunityMessageAction(ctx context.Context, req CommunityMessageActionRequest) error {
	if req.MessageID <= 0 || req.Action != "retry" && req.Action != "cancel" {
		return errors.New("community: invalid message action")
	}
	if req.Action == "retry" && !req.Confirm {
		return errors.New("community: confirm retry; a previous unknown attempt may have reached the server")
	}
	if err := validateCommunityRequestID(req.RequestID); err != nil {
		return err
	}
	fingerprint := sha256.Sum256(fmt.Appendf(nil, "%d:%s:%t", req.MessageID, req.Action, req.Confirm))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return err
	}
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		previous, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID})
		if err == nil {
			if previous.Kind != "message-action" || !bytes.Equal(previous.Fingerprint, fingerprint[:]) {
				return errors.New("community: request_id was already used for a different submission")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		message, err := q.GetCommunityMessage(ctx, db.GetCommunityMessageParams{Account: req.Account, ID: req.MessageID})
		if err != nil {
			return err
		}
		if message.Direction != "outgoing" {
			return ErrCommunityMessageState
		}
		state := "cancelled"
		if req.Action == "retry" {
			if message.State != "failed" && message.State != "unknown" && message.State != "cancelled" {
				return ErrCommunityMessageState
			}
			state = "queued"
		} else if message.State != "queued" && message.State != "failed" && message.State != "unknown" && message.State != "cancelled" {
			return ErrCommunityMessageState
		}
		// Cancelling Unknown only removes it from the local outbox; it cannot
		// retract network bytes or erase uncertainty. Preserve that explanation.
		detail := ""
		if req.Action == "cancel" && (message.State == "unknown" || message.State == "cancelled" && message.Error != "") {
			detail = "Cancelled locally; a previous attempt may have reached the server"
		}
		if _, err = q.SetCommunityMessageState(ctx, db.SetCommunityMessageStateParams{Account: req.Account, ID: req.MessageID,
			OldState: message.State, NewState: state, Error: detail}); err != nil {
			return err
		}
		if _, err = q.InsertCommunitySubmission(ctx, db.InsertCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID,
			Kind: "message-action", Fingerprint: fingerprint[:], Result: `{}`, CreatedAt: time.Now().UTC().UnixMilli()}); err != nil {
			return err
		}
		_, err = q.BumpCommunityRevision(ctx, req.Account)
		return err
	})
	if err == nil {
		s.wakeCommunityOutboxLocked()
	}
	return err
}

func (s *Service) wakeCommunityOutboxLocked() {
	select {
	case s.community.wake <- struct{}{}:
	default:
	}
}

// syncCommunityOutbox runs only on the existing session worker. It is joined
// before a new connection can start, so an old completion cannot finish a retry.
func (s *Service) syncCommunityOutbox(ctx context.Context, client *soulseek.Client, identity CommunityIdentity) error {
	s.mu.RLock()
	if !s.communityCurrentLocked(identity) || s.client != client {
		s.mu.RUnlock()
		return ErrCommunitySession
	}
	if s.shuttingDown {
		s.mu.RUnlock()
		return nil
	}
	rows, err := s.stateDB.Queries().ListCommunityOutbox(ctx, db.ListCommunityOutboxParams{Account: identity.Account, PageSize: 20})
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.sendCommunityQueued(ctx, client, identity, row); err != nil {
			if errors.Is(err, ErrClosed) {
				return nil
			} // Intentional upload draining, not a transport failure.
			if errors.Is(err, ErrCommunityMessageState) {
				continue
			} // Cancelled while awaiting the writer.
			return err
		}
		// Ordinary PMs, including future audience sends, share this bounded pace.
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if len(rows) == 20 {
		s.mu.Lock()
		if s.community.identity == identity {
			s.wakeCommunityOutboxLocked()
		}
		s.mu.Unlock()
	}
	return nil
}

func (s *Service) sendCommunityQueued(ctx context.Context, client *soulseek.Client, identity CommunityIdentity, message db.CommunityMessage) error {
	s.mu.RLock()
	conversation, err := s.stateDB.Queries().GetCommunityConversation(ctx, db.GetCommunityConversationParams{Account: identity.Account, ID: message.ConversationID})
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if conversation.Kind != "private" {
		return errors.New("community: only private messages can use the outbox")
	}
	prepared := false
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	attempted, sendErr := client.SendPrivateMessage(writeCtx, conversation.Target, message.Body, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.communityCurrentLocked(identity) || s.client != client {
			return ErrCommunitySession
		}
		if s.shuttingDown {
			return ErrClosed
		}
		err := s.stateDB.WriteTx(writeCtx, func(tx *sql.Tx) error {
			q := db.New(tx)
			n, err := q.SetCommunityMessageState(writeCtx, db.SetCommunityMessageStateParams{Account: identity.Account, ID: message.ID, OldState: "queued", NewState: "sending"})
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrCommunityMessageState
			}
			_, err = q.BumpCommunityRevision(writeCtx, identity.Account)
			return err
		})
		prepared = err == nil
		return err
	})
	cancel()
	if !prepared && !errors.Is(sendErr, soulseek.ErrMalformed) && !errors.Is(sendErr, soulseek.ErrTooLarge) {
		return sendErr
	}
	oldState, state, detail := "sending", "sent", ""
	if !prepared {
		oldState, state, detail = "queued", "failed", "Invalid queued message; cancel and compose it again"
	} else if !attempted {
		state = "queued" // Cancellation after preparation but before invoking Write is safe to restore.
	} else if sendErr != nil {
		state, detail = "unknown", "Write interrupted; the server may have received this message. Retry explicitly to risk a duplicate."
	}
	// Persist the outcome even when the network context or account has changed.
	// Shutdown waits for this worker before closing SQLite.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	err = s.stateDB.WriteTx(finishCtx, func(tx *sql.Tx) error {
		q := db.New(tx)
		n, err := q.SetCommunityMessageState(finishCtx, db.SetCommunityMessageStateParams{Account: identity.Account, ID: message.ID, OldState: oldState, NewState: state, Error: detail})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrCommunityMessageState
		}
		_, err = q.BumpCommunityRevision(finishCtx, identity.Account)
		return err
	})
	if err != nil {
		return fmt.Errorf("community: persist send outcome: %w", err)
	}
	if state == "failed" {
		return nil
	}
	return sendErr
}

// An empty account is used only at process startup, before any workers exist.
// Account-specific recovery runs after joining the previous connection worker.
func (s *Service) recoverCommunityOutbox(ctx context.Context, account string) error {
	return s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		n, err := q.RecoverCommunityOutbox(ctx, account)
		if err != nil || n == 0 || account == "" {
			return err
		}
		_, err = q.BumpCommunityRevision(ctx, account)
		return err
	})
}
