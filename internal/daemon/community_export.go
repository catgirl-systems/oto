package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/storage"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

type CommunityExportRequest struct {
	CommunityIdentity
	ConversationID int64  `json:"conversation_id"`
	Cursor         int64  `json:"cursor"`
	ThroughID      int64  `json:"through_id"` // Zero captures the current upper bound on the first page.
	Limit          int    `json:"limit"`
	Format         string `json:"format"` // text or json; neither changes read state.
	Query          string `json:"query"`
}

type CommunityExportPage struct {
	CommunityIdentity
	ConversationID int64              `json:"conversation_id"`
	Format         string             `json:"format"`
	Text           string             `json:"text,omitempty"`
	Messages       []CommunityMessage `json:"messages,omitempty"`
	NextCursor     int64              `json:"next_cursor"`
	ThroughID      int64              `json:"through_id"`
	Revision       uint64             `json:"revision"`
}

// ExportCommunityHistory pages oldest first, with a fixed upper bound so new
// arrivals cannot make an export grow indefinitely. Explicitly cleared content
// is never retained in a second log or resurrected to complete a later page.
func (s *Service) ExportCommunityHistory(ctx context.Context, req CommunityExportRequest) (CommunityExportPage, error) {
	limit, err := communityPageLimit(req.Limit)
	if err != nil || req.ConversationID <= 0 || req.Cursor < 0 || req.ThroughID < req.Cursor || len(req.Query) > 1024 || !utf8.ValidString(req.Query) || req.Format != "text" && req.Format != "json" {
		return CommunityExportPage{}, errors.New("community: invalid export request; use text or json and the returned through_id on subsequent pages")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityExportPage{}, err
	}
	out := CommunityExportPage{CommunityIdentity: req.CommunityIdentity, ConversationID: req.ConversationID, Format: req.Format, ThroughID: req.ThroughID}
	err = s.stateDB.ReadSnapshot(ctx, func(tx *storage.ReadTx) error {
		q := tx.Queries()
		if _, err := q.GetCommunityConversation(ctx, db.GetCommunityConversationParams{Account: req.Account, ID: req.ConversationID}); err != nil {
			return err
		}
		account, err := q.GetCommunityAccount(ctx, req.Account)
		if err != nil {
			return err
		}
		out.Revision = s.community.revision + uint64(account.Revision)
		if out.ThroughID == 0 {
			info, err := q.CommunityHistoryInfo(ctx, db.CommunityHistoryInfoParams{Account: req.Account, ConversationID: req.ConversationID})
			if err != nil {
				return err
			}
			out.ThroughID = info.LatestID
		}
		rows, err := q.ExportCommunityMessages(ctx, db.ExportCommunityMessagesParams{Account: req.Account, ConversationID: req.ConversationID,
			AfterID: req.Cursor, ThroughID: out.ThroughID, SearchText: req.Query, PageSize: limit})
		if err != nil {
			return err
		}
		bytes, last := 0, int64(0)
		var text strings.Builder
		for _, row := range rows {
			message := communityMessage(row)
			var data []byte
			line := ""
			if req.Format == "text" {
				stamp := message.CreatedAt
				if message.ServerTime != nil {
					stamp = *message.ServerTime
				}
				line = fmt.Sprintf("[%s] %s %s (%s): %s\n", stamp.Format(time.RFC3339), message.Direction, message.Sender, message.State, strings.ReplaceAll(message.Text, "\n", "\n    "))
				data, err = json.Marshal(line)
			} else {
				data, err = json.Marshal(message)
			}
			if err != nil {
				return err
			}
			if bytes+len(data)+1 > communityPageBytes {
				if last == 0 {
					return errors.New("community: stored message exceeds export budget")
				}
				out.NextCursor = last
				break
			}
			bytes += len(data) + 1
			if req.Format == "text" {
				text.WriteString(line)
			} else {
				out.Messages = append(out.Messages, message)
			}
			last = message.ID
		}
		out.Text = text.String()
		if out.NextCursor == 0 && int64(len(rows)) == limit {
			out.NextCursor = last
		}
		return nil
	})
	return out, err
}
