package daemon

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestSharedSendPreviewCapturesFolderAndPermissionFailures(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	root := t.TempDir()
	for i := range 205 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d", i)), []byte("abc"), 0600); err != nil {
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
	client := soulseek.NewClient(soulseek.ClientConfig{Share: index, SharePolicy: func(user string, _ netip.Addr) soulseek.SharePermission {
		if user == "Denied" {
			return soulseek.SharePermission{Banned: true}
		}
		return soulseek.SharePermission{Roots: map[string]soulseek.ShareVisibility{"Music": soulseek.ShareAllowed}}
	}})
	s.mu.Lock()
	s.client = client
	s.shares = index
	s.community.online = true
	id := s.community.identity
	s.mu.Unlock()
	req := SharedSendRequest{CommunityIdentity: id, RequestID: "folder-preview", Username: "Alice", Folder: "Music"}
	out, err := s.PreviewSharedSend(ctx, req)
	if err != nil || out.Total != 205 || out.Eligible != 205 || out.Bytes != 615 || !out.SizesKnown || len(out.Files) != 200 || out.NextCursor != 200 {
		t.Fatal(out, err)
	}
	last, err := s.SharedSend(ctx, id, req.RequestID, out.NextCursor)
	if err != nil || len(last.Files) != 5 || last.NextCursor != 0 {
		t.Fatal(last, err)
	}
	if err := os.Remove(filepath.Join(root, "file-000")); err != nil {
		t.Fatal(err)
	}
	again, err := s.PreviewSharedSend(ctx, req)
	if err != nil || again.Token != out.Token || again.Bytes != 615 {
		t.Fatal("retry changed snapshot", again, err)
	}
	req.RequestID = "permission-preview"
	req.Username = "Denied"
	req.Folder = ""
	req.Files = []string{"Music/file-001"}
	denied, err := s.PreviewSharedSend(ctx, req)
	if err != nil || denied.Eligible != 0 || denied.Total != 1 || denied.Bytes != 3 || denied.Files[0].State != "denied" {
		t.Fatal(denied, err)
	}
	req.Username = "Alice"
	if _, err := s.PreviewSharedSend(ctx, req); err == nil {
		t.Fatal("request ID reused")
	}
	s.mu.Lock()
	queued, busy := len(s.journal.Uploads), s.community.sharedPreview != nil
	s.community.identity.Session++
	current := s.community.identity
	s.mu.Unlock()
	if queued != 0 || busy {
		t.Fatal("preview queued files or retained worker")
	}
	stale, err := s.SharedSend(ctx, current, out.RequestID, 0)
	if err != nil || stale.State != "stale-preview" {
		t.Fatal(stale, err)
	}
}
