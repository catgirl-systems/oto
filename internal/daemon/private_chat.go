package daemon

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
	"golang.org/x/text/encoding/charmap"
)

// communityDisplayText follows Nicotine+'s UTF-8/Latin-1 fallback only for
// display text. Keep the original bytes in the receipt fingerprint, not logs.
func communityDisplayText(text string) string {
	if !utf8.ValidString(text) {
		text, _ = charmap.ISO8859_1.NewDecoder().String(text)
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)) {
			return '\ufffd'
		}
		return r
	}, text)
}

// Persist and preview the same single-line text sent to the server, following
// Nicotine+'s room and private-chat behavior. Validate after transformations.
func communityOutgoingText(text string) (string, error) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", " ")
	if err := soulseek.ValidateChatText(text); err != nil {
		return "", err
	}
	if strings.IndexFunc(text, func(r rune) bool { return r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)) }) >= 0 {
		return "", errors.New("community: chat text contains terminal controls")
	}
	return text, nil
}

func (s *Service) receiveCommunityPrivate(ctx context.Context, identity CommunityIdentity, message soulseek.PrivateMessage) error {
	// XXX: skip undecodable senders; nicotine+ renders whatever it receives.
	if soulseek.ValidateUsername(message.Username) != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.communityCurrentLocked(identity) {
		return ErrCommunitySession
	}
	now := time.Now().UTC().UnixMilli()
	fingerprint := sha256.Sum256([]byte(message.Text))
	inserted := false
	disposition, state := "stored", "received"
	ignored, held := s.communityIgnoreLocked(message.Username)
	if ignored {
		disposition = "discarded"
	} else if held {
		disposition, state = "held", "held"
	}
	body, mentioned := s.community.text.incoming(message.Text, s.cfg.Soulseek.Username)
	mention := int64(0)
	if mentioned {
		mention = 1
	}
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		n, err := q.InsertCommunityReceipt(ctx, db.InsertCommunityReceiptParams{
			Account: identity.Account, Sender: message.Username,
			ServerID: int64(message.ID), ServerTime: int64(message.Timestamp),
			Fingerprint: fingerprint[:], Disposition: disposition, CreatedAt: now,
		})
		if err != nil || n == 0 {
			return err // A replay remains acknowledged even after its content was cleared.
		}
		if ignored {
			return nil
		}
		closed := int64(0)
		if held {
			previous, err := q.FindCommunityConversation(ctx, db.FindCommunityConversationParams{Account: identity.Account, Kind: "private", Target: message.Username})
			if errors.Is(err, sql.ErrNoRows) {
				closed = 1
			} else if err != nil {
				return err
			} else {
				closed = previous.Closed
			}
		}
		conversation, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{
			Account: identity.Account, Kind: "private", Target: message.Username,
		})
		if err != nil {
			return err
		}
		serverTime := int64(message.Timestamp)
		if _, err = q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{
			Account: identity.Account, ConversationID: conversation.ID, Sender: message.Username,
			Direction: "incoming", Body: body, Mention: mention, CreatedAt: now,
			ServerTime: &serverTime, State: state,
		}); err != nil {
			return err
		}
		if _, err = q.SetCommunityConversationClosed(ctx, db.SetCommunityConversationClosedParams{Account: identity.Account, ID: conversation.ID, Closed: closed}); err != nil {
			return err
		}
		_, err = q.BumpCommunityRevision(ctx, identity.Account)
		inserted = err == nil
		return err
	})
	if err == nil && inserted && !held {
		s.watchConversationLocked(message.Username, true)
		s.communityRoomNoticeLocked(message)
		s.queueCTCPReplyLocked(identity, message)
		s.queueAwayReplyLocked(identity, message)
	}
	return err // Only the client may ACK, after this transaction has committed.
}

func (s *Service) watchConversationLocked(username string, open bool) {
	lease := s.community.watches["conversations"]
	if slices.Contains(lease.users, username) == open {
		return
	}
	if open {
		lease.users = append(lease.users, username)
	} else {
		lease.users = slices.DeleteFunc(lease.users, func(user string) bool { return user == username })
	}
	s.setUserWatchesLocked("conversations", lease.users, time.Time{})
}
