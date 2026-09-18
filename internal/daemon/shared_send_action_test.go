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
		must(t, os.WriteFile(filepath.Join(root, name), []byte("data"), 0600))
	}
	index := soulseek.NewShareIndex()
	must(t, index.AddRoot("Music", root))
	must(t, index.ScanContext(ctx))
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
	failIf(t, err != nil || command.SharedSend == nil, command, err)
	preview := *command.SharedSend
	checkpoint, err := loadSharedSend(ctx, s.stateDB.Queries(), id.Account, preview.RequestID)
	must(t, err)
	must(t, err)
	action := SharedSendAction{CommunityIdentity: id, RequestID: preview.RequestID, Token: preview.Token, Action: "send"}
	if _, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "send-confirm", RequestID: "wrong", Confirm: true, Args: []string{preview.RequestID, preview.Token}}); err == nil {
		t.Fatal("confirmation ID mismatch")
	}
	if out, err := s.ActSharedSend(ctx, action); err != nil || out.State != "preview" || admissions.Load() != 0 {
		t.Fatal(out, err)
	}
	must(t, os.WriteFile(filepath.Join(root, "changed"), []byte("different"), 0600))
	action.Confirm = true
	if _, err := s.ActSharedSend(ctx, action); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	out, err := s.SharedSend(ctx, id, preview.RequestID, 0)
	failIf(t, err != nil || out.State != "completed" || out.Files[0].State != "failed" || out.Files[1].UploadID == "" || admissions.Load() != 1, out, err, admissions.Load())
	if _, err := s.ActSharedSend(ctx, action); err != nil {
		t.Fatal(err)
	}
	failIf(t, admissions.Load() != 1, "confirmation retry queued duplicates")
	// Simulate a crash before the full batch checkpoint: child receipts must
	// retain per-file outcomes, and an orphaned running batch must not restart.
	checkpoint.State = "running"
	must(t, saveSharedSend(ctx, s.stateDB.Queries(), checkpoint))
	recovered, err := s.SharedSend(ctx, id, preview.RequestID, 0)
	failIf(t, err != nil || recovered.State != "interrupted" || recovered.Files[0].State != "failed" || recovered.Files[1].UploadID == "", recovered, err)
	if out, err := s.ActSharedSend(ctx, action); err != nil || out.State != "interrupted" || admissions.Load() != 1 {
		t.Fatal(out, err)
	}
	action.Token = "wrong"
	if _, err := s.ActSharedSend(ctx, action); err == nil {
		t.Fatal("invalid token")
	}
}
