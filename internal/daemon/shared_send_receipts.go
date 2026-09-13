package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/catgirl-systems/oto/internal/storage/db"
)

func sharedSendFileID(b sharedSend, filename string) string {
	sum := sha256.Sum256([]byte(b.Token + "\x00" + filename))
	return "shared-file-" + hex.EncodeToString(sum[:])
}

// Per-file receipts keep admission persistence linear in the selection size.
// The full immutable selection is only rewritten at batch start/completion.
func saveSharedSendFile(ctx context.Context, q *db.Queries, b sharedSend, item sharedSendFile) error {
	id := sharedSendFileID(b, item.File.Filename)
	fingerprint := sha256.Sum256([]byte(b.Username + "\x00" + item.Fingerprint))
	data, err := json.Marshal(item.File)
	if err != nil {
		return err
	}
	n, err := q.InsertCommunitySubmission(ctx, db.InsertCommunitySubmissionParams{Account: b.Identity.Account, RequestID: id, Kind: "shared-send-file", Fingerprint: fingerprint[:], Result: string(data), CreatedAt: b.CreatedAt.UnixMilli()})
	if err != nil || n == 1 {
		return err
	}
	old, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: b.Identity.Account, RequestID: id})
	if err != nil {
		return err
	}
	if old.Kind != "shared-send-file" || !bytes.Equal(old.Fingerprint, fingerprint[:]) {
		return errors.New("shared-send file receipt collision")
	}
	_, err = q.SetCommunitySubmissionResult(ctx, db.SetCommunitySubmissionResultParams{Account: b.Identity.Account, RequestID: id, Result: string(data)})
	return err
}

func refreshSharedSendFile(ctx context.Context, q *db.Queries, b sharedSend, item sharedSendFile, row *SharedSendFile) error {
	receipt, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: b.Identity.Account, RequestID: sharedSendFileID(b, row.Filename)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	expected := sha256.Sum256([]byte(b.Username + "\x00" + item.Fingerprint))
	if receipt.Kind != "shared-send-file" || !bytes.Equal(receipt.Fingerprint, expected[:]) || len(receipt.Result) > 128<<10 {
		return errors.New("invalid shared-send file receipt")
	}
	var saved SharedSendFile
	if err := json.Unmarshal([]byte(receipt.Result), &saved); err != nil {
		return err
	}
	if saved.Filename != row.Filename || saved.Size != row.Size {
		return errors.New("shared-send file receipt changed")
	}
	*row = saved
	return nil
}
