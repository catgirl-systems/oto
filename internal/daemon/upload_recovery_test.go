package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestUploadJournalRecoveryAndAccounts(t *testing.T) {
	cfg := testConfig(t)
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	index := soulseek.NewShareIndex()
	if err := index.AddRoot("Music", root); err != nil {
		t.Fatal(err)
	}
	if err := index.ScanContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	newService := func(c config.Config, manager *soulseek.UploadManager, ready <-chan struct{}, accepted *[]string) *Service {
		t.Helper()
		s, err := New(c, path)
		if err != nil {
			t.Fatal(err)
		}
		s.shares = index
		s.uploadEpoch = 1
		s.status = StatusConnected
		s.client = soulseek.NewClient(soulseek.ClientConfig{Address: c.Soulseek.Server, Username: c.Soulseek.Username, Share: index, Uploads: manager, UploadsReady: ready, UploadAccepted: func(e soulseek.TransferEvent) error {
			if err := s.uploadAccepted(1, e); err != nil {
				return err
			}
			if accepted != nil {
				*accepted = append(*accepted, e.Filename)
			}
			return nil
		}, UploadUpdate: func(e soulseek.TransferEvent) { s.uploadUpdate(1, e) }})
		t.Cleanup(func() { s.Close() })
		return s
	}
	manager := soulseek.NewUploadManager(1)
	hold := manager.Enqueue("hold", soulseek.TransferRequest{Filename: "hold", Size: 1})
	if err := manager.Wait(context.Background(), hold); err != nil {
		t.Fatal(err)
	}
	defer manager.Done(hold)
	first := newService(cfg, manager, nil, nil)
	for _, name := range []string{`Music\a`, `Music\b`} {
		if _, err := first.client.QueueUpload("peer", name); err != nil {
			t.Fatal(err)
		}
	}
	first.Close()
	if len(first.journal.Uploads) != 2 || !first.journal.Uploads[0].Recoverable || first.journal.UploadSequence != 2 {
		t.Fatalf("lost interrupted queue: %+v", first.journal)
	}
	// SQLite persists queue order explicitly; recovery must use it rather than row order.
	limited := soulseek.NewUploadManager(1)
	limited.Configure(soulseek.UploadPolicy{MaxQueuedFilesPerUser: 1, MaxQueuedBytesPerUser: 1})
	ready := make(chan struct{})
	var accepted []string
	restored := newService(cfg, limited, ready, &accepted)
	restored.recoverUploads(restored.client, 1)
	if !slices.Equal(accepted, []string{`Music\a`, `Music\b`}) {
		t.Fatalf("recovery FIFO/grandfathering: %v", accepted)
	}
	for _, u := range restored.journal.Uploads {
		if u.State != "queued" || u.ID != "upload:1" && u.ID != "upload:2" {
			t.Fatalf("restored identity: %+v", u)
		}
	}
	restored.Close()
	other := cfg
	other.Soulseek.Username += "-other"
	accepted = nil
	offline := newService(other, soulseek.NewUploadManager(1), make(chan struct{}), &accepted)
	offline.recoverUploads(offline.client, 1)
	if len(accepted) != 0 {
		t.Fatal("recovered another account's queue")
	}
	offline.Close()
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	accepted = nil
	changed := newService(cfg, soulseek.NewUploadManager(1), make(chan struct{}), &accepted)
	changed.recoverUploads(changed.client, 1)
	for _, u := range changed.journal.Uploads {
		if u.Filename == `Music\a` && (u.State != "failed" || u.Recoverable) {
			t.Fatalf("changed upload resumed: %+v", u)
		}
	}
}
