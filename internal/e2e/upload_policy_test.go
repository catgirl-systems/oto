//go:build communitye2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityUploadPolicyTerminalPersistence(t *testing.T) {
	login := testutil.SocialFixture(t, "login-ok")
	payload := login.Payload(t)
	server := testutil.ListenScript(t, func(_ context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		if err := testutil.WritePacket(conn, 1, payload); err != nil {
			return err
		}
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	h := newTerminal(t, server.Listener.Addr().String())
	h.attach("preferences", 120, 40)
	h.screen("preferences", "No matching results")
	h.command("send-keys", "-t", "preferences", "Tab", "Tab", "Tab", "Tab", "Tab", "Tab", "Tab")
	h.screen("preferences", "SETTINGS")
	h.command("send-keys", "-t", "preferences", "Right", "Right", "Right", "Right")
	h.screen("preferences", "Prioritize all buddies")
	h.command("send-keys", "-t", "preferences", "Down", "Down", "Enter", "Down", "Enter", "Down", "Enter")
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}, {120, 40}} {
		h.command("resize-window", "-t", "preferences", "-x", fmt.Sprint(size[0]), "-y", fmt.Sprint(size[1]))
		text := "Exempt buddies"
		if size[1] < 8 {
			text = "oto"
		}
		h.screen("preferences", text)
	}
	before, err := h.client.Status(context.Background())
	if err != nil || before.Config.Uploads.PrioritizeBuddies || before.Config.Uploads.PrioritizePrivileged || before.Config.Uploads.ExemptBuddiesFromQueueLimits {
		t.Fatal("unsaved draft changed daemon", err)
	}
	h.command("send-keys", "-t", "preferences", "s")
	h.screen("preferences", "Settings saved")
	saved, err := config.Load(h.configPath)
	if err != nil || !saved.Uploads.PrioritizeBuddies || !saved.Uploads.PrioritizePrivileged || !saved.Uploads.ExemptBuddiesFromQueueLimits {
		t.Fatal("terminal preferences not persisted", saved.Uploads, err)
	}
	h.command("kill-session", "-t", "preferences")
	h.stopDaemon()
	h.startDaemon()
	after, err := h.client.Status(context.Background())
	if err != nil || after.Config.Uploads != saved.Uploads {
		t.Fatal("restart lost queue preferences", err)
	}
	h.attach("restored", 80, 24)
	h.screen("restored", "No matching results")
	h.command("send-keys", "-t", "restored", "Tab", "Tab", "Tab", "Tab", "Tab", "Tab", "Tab", "Right", "Right", "Right", "Right")
	h.screen("restored", "Prioritize all buddies")
}
