package daemon

import (
	"context"
	"database/sql"
	"time"

	"github.com/catgirl-systems/oto/internal/storage/db"
)

// The caller holds s.mu and reserves its bounded policy budget. Automatic
// responses are recorded before write, but never queued for offline delivery.
func (s *Service) sendCommunityAutomaticLocked(id CommunityIdentity, username, text string, eligible func() bool, finished func()) {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	client := s.client
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer func() { s.mu.Lock(); finished(); s.mu.Unlock() }()
		var messageID int64
		_, sendErr := client.SendPrivateMessage(ctx, username, text, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			if s.closed || s.shuttingDown || s.client != client || !s.communityCurrentLocked(id) || !eligible() {
				return ErrCommunitySession
			}
			var candidateID int64
			err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
				q := db.New(tx)
				conversation, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{Account: id.Account, Kind: "private", Target: username})
				if err != nil {
					return err
				}
				message, err := q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: id.Account, ConversationID: conversation.ID, Sender: s.cfg.Soulseek.Username, Direction: "outgoing", Body: text, State: "sending", CreatedAt: time.Now().UnixMilli()})
				if err != nil {
					return err
				}
				candidateID = message.ID
				_, err = q.BumpCommunityRevision(ctx, id.Account)
				return err
			})
			if err == nil {
				messageID = candidateID
			}
			return err
		})
		if messageID == 0 {
			return
		}
		state := "unknown"
		if sendErr == nil {
			state = "sent"
		}
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer finishCancel()
		// Failure leaves Sending, which normal restart recovery makes Unknown;
		// neither state is automatically retried by the outbox.
		err := s.stateDB.WriteTx(finishCtx, func(tx *sql.Tx) error {
			q := db.New(tx)
			if _, err := q.SetCommunityMessageState(finishCtx, db.SetCommunityMessageStateParams{Account: id.Account, ID: messageID, OldState: "sending", NewState: state}); err != nil {
				return err
			}
			_, err := q.BumpCommunityRevision(finishCtx, id.Account)
			return err
		})
		if err != nil {
			s.mu.Lock()
			if s.community.identity.Account == id.Account {
				s.lastErr = "Automatic reply status could not be saved; restart to recover it as Unknown"
			}
			s.mu.Unlock()
		}
	}()
}
