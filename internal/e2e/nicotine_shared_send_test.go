//go:build communitye2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func verifyNicotineSharedSend(t *testing.T, h *terminal, ctx context.Context, state string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Album"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"blocked", "Album/one.txt", "Album/世界.txt"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("manual reference "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.client.AddShare(ctx, config.Share{Name: "manual", Path: root}); err != nil {
		t.Fatal(err)
	}
	summary, err := h.client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := summary.CommunityIdentity
	submit := func(request daemon.SharedSendRequest, want int) daemon.SharedSendPage {
		t.Helper()
		request.CommunityIdentity = id
		request.Username = "reference"
		var page daemon.SharedSendPage
		h.wait("reference shared-send preview", func() bool {
			var err error
			page, err = h.client.PreviewSharedSend(ctx, request)
			return err == nil && page.Total == want
		})
		if page.Eligible != want {
			t.Fatal("unexpected sender permission failure", page)
		}
		action := daemon.SharedSendAction{CommunityIdentity: id, RequestID: page.RequestID, Token: page.Token, Action: "send", Confirm: true}
		if _, err := h.client.ActSharedSend(ctx, action); err != nil {
			t.Fatal(err)
		}
		h.wait("reference shared-send outcomes", func() bool {
			var err error
			page, err = h.client.SharedSend(ctx, id, request.RequestID, 0)
			if err != nil || page.State != "completed" {
				return false
			}
			for _, file := range page.Files {
				if file.State != "completed" && file.State != "failed" {
					return false
				}
			}
			return true
		})
		return page
	}
	denied := submit(daemon.SharedSendRequest{RequestID: "reference-reject", Files: []string{`manual\blocked`}}, 1)
	if denied.Files[0].State != "failed" {
		t.Fatal("Nicotine default-off receiving accepted offer", denied)
	}
	if err := os.WriteFile(filepath.Join(state, "enable-receiving"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	h.wait("reference explicitly enables buddy receiving", func() bool { _, err := os.Stat(filepath.Join(state, "receiving-ready.json")); return err == nil })
	accepted := submit(daemon.SharedSendRequest{RequestID: "reference-folder", Folder: `manual\Album`}, 2)
	for _, file := range accepted.Files {
		if file.State != "completed" || file.UploadID == "" {
			t.Fatal("reference did not receive folder", accepted)
		}
	}
	for _, name := range []string{"one.txt", "世界.txt"} {
		h.wait("reference received exact file bytes", func() bool {
			data, err := os.ReadFile(filepath.Join(state, "received", "terminal", "Album", name))
			return err == nil && string(data) == "manual reference Album/"+name
		})
	}
}
