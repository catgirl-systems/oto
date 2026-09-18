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
		must(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d", i)), []byte("abc"), 0600))
	}
	index := soulseek.NewShareIndex()
	must(t, index.AddRoot("Music", root))
	must(t, index.ScanContext(ctx))
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
	failIf(t, err != nil || out.Total != 205 || out.Eligible != 205 || out.Bytes != 615 || !out.SizesKnown || len(out.Files) != 200 || out.NextCursor != 200, out, err)
	last, err := s.SharedSend(ctx, id, req.RequestID, out.NextCursor)
	failIf(t, err != nil || len(last.Files) != 5 || last.NextCursor != 0, last, err)
	must(t, os.Remove(filepath.Join(root, "file-000")))
	again, err := s.PreviewSharedSend(ctx, req)
	failIf(t, err != nil || again.Token != out.Token || again.Bytes != 615, "retry changed snapshot", again, err)
	req.RequestID = "permission-preview"
	req.Username = "Denied"
	req.Folder = ""
	req.Files = []string{"Music/file-001"}
	denied, err := s.PreviewSharedSend(ctx, req)
	failIf(t, err != nil || denied.Eligible != 0 || denied.Total != 1 || denied.Bytes != 3 || denied.Files[0].State != "denied", denied, err)
	req.Username = "Alice"
	if _, err := s.PreviewSharedSend(ctx, req); err == nil {
		t.Fatal("request ID reused")
	}
	s.mu.Lock()
	queued, busy := len(s.journal.Uploads), s.community.sharedPreview != nil
	s.community.identity.Session++
	current := s.community.identity
	s.mu.Unlock()
	failIf(t, queued != 0 || busy, "preview queued files or retained worker")
	stale, err := s.SharedSend(ctx, current, out.RequestID, 0)
	failIf(t, err != nil || stale.State != "stale-preview", stale, err)
}
