package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func uploadService(t *testing.T) (*Service, *soulseek.UploadManager, *soulseek.UploadJob, string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("shared"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	shares := soulseek.NewShareIndex()
	if err := shares.AddRoot("Music", root); err != nil {
		t.Fatal(err)
	}
	if err := shares.ScanContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager := soulseek.NewUploadManager(1)
	blocker := manager.Enqueue("blocker", soulseek.TransferRequest{})
	s := &Service{transfers: map[string]Transfer{}, uploadEpoch: 1, status: StatusConnected}
	s.client = soulseek.NewClient(soulseek.ClientConfig{Share: shares, Uploads: manager, UploadUpdate: func(e soulseek.TransferEvent) { s.uploadUpdate(1, e) }})
	c := s.client
	t.Cleanup(func() { _ = c.Close(); manager.Done(blocker) })
	return s, manager, blocker, root
}
func uploadRow(t *testing.T, s *Service, user, file string) Transfer {
	t.Helper()
	for _, row := range s.Transfers() {
		if row.Username == user && row.Filename == file {
			return row
		}
	}
	t.Fatalf("missing %s %s", user, file)
	return Transfer{}
}

func TestUploadActionsLifecycle(t *testing.T) {
	s, _, _, root := uploadService(t)
	for _, item := range [][2]string{{"alice", `Music\one`}, {"alice", `Music\two`}, {"bob", `Music\one`}} {
		if _, err := s.client.QueueUpload(item[0], item[1]); err != nil {
			t.Fatal(err)
		}
	}
	download := Transfer{ID: "d-1", Direction: "download", State: "running"}
	s.mu.Lock()
	s.transfers[download.ID] = download
	s.mu.Unlock()
	one := uploadRow(t, s, "alice", `Music\one`).ID
	result, err := s.UploadAction(UploadActionRequest{Action: "cancel", Usernames: []string{"alice", "alice"}})
	if err != nil || result.Changed != 2 {
		t.Fatalf("abort users: %+v %v", result, err)
	}
	if uploadRow(t, s, "bob", `Music\one`).State != "queued" {
		t.Fatal("unrelated user cancelled")
	}
	if uploadRow(t, s, "alice", `Music\one`).State != "cancelled" {
		t.Fatal("missing cancelled history")
	}
	result, err = s.UploadAction(UploadActionRequest{Action: "retry", IDs: []string{one, one}})
	if err != nil || result.Changed != 1 || uploadRow(t, s, "alice", `Music\one`).State != "queued" {
		t.Fatalf("retry: %+v %v", result, err)
	}
	if err := s.TransferAction(one, "cancel"); err != nil {
		t.Fatal(err)
	}
	result, err = s.UploadAction(UploadActionRequest{Action: "clear", States: []string{"cancelled"}})
	if err != nil || result.Changed != 2 {
		t.Fatalf("clear cancelled: %+v %v", result, err)
	}
	result, err = s.UploadAction(UploadActionRequest{Action: "clear", All: true})
	if err != nil || result.Changed != 1 {
		t.Fatalf("clear all: %+v %v", result, err)
	}
	if rows := s.Transfers(); len(rows) != 1 || rows[0] != download {
		t.Fatalf("upload clear touched download: %+v", rows)
	}
	content, err := os.ReadFile(filepath.Join(root, "one"))
	if err != nil || string(content) != "shared" {
		t.Fatal("shared file changed")
	}
	// Future peer requests are not banned by clear-all.
	if started, err := s.client.QueueUpload("alice", `Music\one`); err != nil || !started {
		t.Fatalf("peer requeue %v %v", started, err)
	}
	if uploadRow(t, s, "alice", `Music\one`).State != "queued" {
		t.Fatal("requeue invisible")
	}
}

func TestUploadActionValidationAndStates(t *testing.T) {
	s, _, _, _ := uploadService(t)
	if _, err := s.client.QueueUpload("a", `Music\one`); err != nil {
		t.Fatal(err)
	}
	id := uploadRow(t, s, "a", `Music\one`).ID
	s.mu.Lock()
	s.transfers["d-1"] = Transfer{ID: "d-1", Direction: "download"}
	s.mu.Unlock()
	for _, req := range []UploadActionRequest{
		{Action: "clear"}, {Action: "clear", All: true, IDs: []string{id}},
		{Action: "cancel", States: []string{"queued"}}, {Action: "retry", All: true},
		{Action: "clear", Usernames: []string{"a"}}, {Action: "clear", States: []string{"running"}},
		{Action: "clear", States: []string{"unknown"}}, {Action: "pause", IDs: []string{id}},
		{Action: "clear", IDs: []string{""}}, {Action: "clear", IDs: []string{id, "d-1"}},
	} {
		if _, err := s.UploadAction(req); err == nil {
			t.Fatalf("accepted %+v", req)
		}
		if uploadRow(t, s, "a", `Music\one`).State != "queued" {
			t.Fatal("invalid batch mutated state")
		}
	}
	result, err := s.UploadAction(UploadActionRequest{Action: "cancel", IDs: []string{id, "upload:missing"}})
	if err != nil || result.Changed != 1 || len(result.Errors) != 1 {
		t.Fatalf("mixed result %+v %v", result, err)
	}
	result, err = s.UploadAction(UploadActionRequest{Action: "cancel", IDs: []string{id}})
	if err != nil || result.Skipped != 1 {
		t.Fatalf("idempotent abort %+v %v", result, err)
	}
	s.mu.Lock()
	s.status = StatusStopped
	s.mu.Unlock()
	if _, err := s.UploadAction(UploadActionRequest{Action: "retry", IDs: []string{id}}); !errors.Is(err, ErrUploadUnavailable) {
		t.Fatalf("offline retry %v", err)
	}
	for _, states := range [][]string{{"completed", "cancelled", "failed"}, {"completed", "cancelled"}, {"completed"}, {"cancelled"}, {"failed"}, {"queued"}} {
		result, err = s.UploadAction(UploadActionRequest{Action: "clear", States: states})
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err = s.UploadAction(UploadActionRequest{Action: "cancel", Usernames: []string{"missing"}})
	if err != nil || result.Changed != 0 {
		t.Fatalf("empty match %+v %v", result, err)
	}
}

func TestUploadRetirementAndStaleUpdates(t *testing.T) {
	s, _, _, _ := uploadService(t)
	c := s.client
	if _, err := c.QueueUpload("a", `Music\one`); err != nil {
		t.Fatal(err)
	}
	id := uploadRow(t, s, "a", `Music\one`).ID
	s.mu.RLock()
	old := s.uploadOwners[id]
	s.mu.RUnlock()
	if _, err := s.UploadAction(UploadActionRequest{Action: "clear", All: true}); err != nil {
		t.Fatal(err)
	}
	stale := soulseek.TransferEvent{Username: "a", Filename: `Music\one`, Attempt: old.target.Attempt, State: "running"}
	s.uploadUpdate(1, stale)
	if len(s.Transfers()) != 0 {
		t.Fatal("stale progress resurrected clear")
	}
	if _, err := c.QueueUpload("a", `Music\one`); err != nil {
		t.Fatal(err)
	}
	s.uploadUpdate(1, stale)
	if uploadRow(t, s, "a", `Music\one`).State != "queued" {
		t.Fatal("old attempt replaced new attempt")
	}
	s.lifecycleMu.Lock()
	s.stopSessionLocked(true)
	s.lifecycleMu.Unlock()
	if uploadRow(t, s, "a", `Music\one`).State != "interrupted" {
		t.Fatal("offline left a live upload")
	}
	stale.State = "queued"
	stale.Attempt += 100
	s.uploadUpdate(1, stale)
	if uploadRow(t, s, "a", `Music\one`).State != "interrupted" {
		t.Fatal("retired session update accepted")
	}
}
