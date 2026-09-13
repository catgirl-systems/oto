package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestSharedSendConfirmationPartialAdmissionAndDeduplication(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	root := t.TempDir()
	for _, name := range []string{"changed", "good"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	index := soulseek.NewShareIndex()
	if err := index.AddRoot("Music", root); err != nil {
		t.Fatal(err)
	}
	if err := index.ScanContext(ctx); err != nil {
		t.Fatal(err)
	}
	var admissions atomic.Int32
	client := soulseek.NewClient(soulseek.ClientConfig{Share: index, UploadAccepted: func(e soulseek.TransferEvent) error { admissions.Add(1); return s.uploadAccepted(1, e) }, UploadUpdate: func(e soulseek.TransferEvent) { s.uploadUpdate(1, e) }})
	s.mu.Lock()
	s.client = client
	s.shares = index
	s.ctx = ctx
	s.community.online = true
	s.uploadEpoch = 1
	id := s.community.identity
	s.mu.Unlock()
	command, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "send-folder", RequestID: "send-folder", Args: []string{"Alice", "Music"}})
	if err != nil || command.SharedSend == nil {
		t.Fatal(command, err)
	}
	preview := *command.SharedSend
	checkpoint, err := loadSharedSend(ctx, s.stateDB.Queries(), id.Account, preview.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	action := SharedSendAction{CommunityIdentity: id, RequestID: preview.RequestID, Token: preview.Token, Action: "send"}
	if _, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "send-confirm", RequestID: "wrong", Confirm: true, Args: []string{preview.RequestID, preview.Token}}); err == nil {
		t.Fatal("confirmation ID mismatch")
	}
	if out, err := s.ActSharedSend(ctx, action); err != nil || out.State != "preview" || admissions.Load() != 0 {
		t.Fatal(out, err)
	}
	if err := os.WriteFile(filepath.Join(root, "changed"), []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	action.Confirm = true
	if _, err := s.ActSharedSend(ctx, action); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	out, err := s.SharedSend(ctx, id, preview.RequestID, 0)
	if err != nil || out.State != "completed" || out.Files[0].State != "failed" || out.Files[1].UploadID == "" || admissions.Load() != 1 {
		t.Fatal(out, err, admissions.Load())
	}
	if _, err := s.ActSharedSend(ctx, action); err != nil {
		t.Fatal(err)
	}
	if admissions.Load() != 1 {
		t.Fatal("confirmation retry queued duplicates")
	}
	// Simulate a crash before the full batch checkpoint: child receipts must
	// retain per-file outcomes, and an orphaned running batch must not restart.
	checkpoint.State = "running"
	if err := saveSharedSend(ctx, s.stateDB.Queries(), checkpoint); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.SharedSend(ctx, id, preview.RequestID, 0)
	if err != nil || recovered.State != "interrupted" || recovered.Files[0].State != "failed" || recovered.Files[1].UploadID == "" {
		t.Fatal(recovered, err)
	}
	if out, err := s.ActSharedSend(ctx, action); err != nil || out.State != "interrupted" || admissions.Load() != 1 {
		t.Fatal(out, err)
	}
	action.Token = "wrong"
	if _, err := s.ActSharedSend(ctx, action); err == nil {
		t.Fatal("invalid token")
	}
}
