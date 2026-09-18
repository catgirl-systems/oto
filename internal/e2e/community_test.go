//go:build communitye2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityTerminalShellAndUserActions(t *testing.T) {
	fixtures := testutil.SocialFixtureMap(t, "login-ok", "WatchUser-request", "watch-online", "user-stats")
	login, watch, stats := fixtures["login-ok"].Payload(t), fixtures["watch-online"].Payload(t), fixtures["user-stats"].Payload(t)
	request := fixtures["WatchUser-request"].Payload(t)
	var watches atomic.Int32
	server := testutil.ListenScript(t, func(ctx context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		if err := testutil.WritePacket(conn, 1, login); err != nil {
			return err
		}
		for {
			code, payload, err := testutil.ReadPacket(conn)
			if err == io.EOF {
				return nil
			} // Normal explicit Offline or daemon shutdown.
			if err != nil {
				return err
			}
			switch code {
			case 5, 36:
				if !bytes.Equal(payload, request) {
					return fmt.Errorf("unexpected user request for code %d: %x", code, payload)
				}
				response := stats
				if code == 5 {
					response = watch
					watches.Add(1)
				}
				if err := testutil.WritePacket(conn, code, response); err != nil {
					return err
				}
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	h.attach("community", 120, 40)
	h.screen("community", "No matching results")
	h.command("send-keys", "-t", "community", "Tab", "Tab", "Tab", "Tab")
	h.screen("community", "[Community]")
	h.wait("Community summary loaded", func() bool {
		screen, _ := h.tmux("capture-pane", "-p", "-t", "community")
		return strings.Contains(screen, "[Chats]") && !strings.Contains(screen, "Loading Community")
	})
	for _, view := range []string{"[Rooms]", "[Buddies]", "[Discover]", "[Chats]"} {
		h.command("send-keys", "-t", "community", "C-NPage")
		h.screen("community", view)
	}
	h.command("send-keys", "-t", "community", "F6")
	h.screen("community", "[Content]")
	h.command("send-keys", "-t", "community", "F6")
	h.screen("community", "[Inspector]")
	h.command("send-keys", "-t", "community", "S-F6")
	h.screen("community", "[Content]")
	h.command("send-keys", "-t", "community", "/")
	h.command("send-keys", "-t", "community", "-l", "q/?猫😀")
	h.screen("community", "q/?猫😀")
	h.command("resize-window", "-t", "community", "-x", "40", "-y", "16")
	h.screen("community", "q/?猫😀")
	h.command("send-keys", "-t", "community", "Escape")
	h.screen("community", "Chats / Content")
	h.command("send-keys", "-t", "community", "/")
	h.command("set-buffer", "-b", "input", "Alice\nnot-a-send")
	h.command("paste-buffer", "-p", "-b", "input", "-t", "community")
	h.screen("community", "Username must fit")
	if watches.Load() != 0 {
		t.Fatal("paste submitted a user request")
	}
	h.command("set-buffer", "-b", "input", "Alice")
	h.command("paste-buffer", "-p", "-b", "input", "-t", "community")
	h.screen("community", "Alice")
	if watches.Load() != 0 {
		t.Fatal("single-line paste submitted a user request")
	}
	h.command("send-keys", "-t", "community", "Enter")
	h.screen("community", "Status: online")
	h.screen("community", "Shares: 99 files, 4 folders")
	h.command("resize-window", "-t", "community", "-x", "80", "-y", "24")
	h.screen("community", "Chats / Inspector · Esc back")
	h.command("send-keys", "-t", "community", "U")
	h.screen("community", "User actions")
	h.command("send-keys", "-t", "community", "Down", "Down", "Enter")
	h.screen("community", "Search scope")
	if screen := h.screen("community", "User: Alice"); !strings.Contains(screen, "Specific users") {
		t.Fatal("user action became global search")
	}
	h.command("send-keys", "-t", "community", "Escape")
	h.screen("community", "Status: online")

	// Another actual frontend shares the daemon subscription, not UI state.
	h.attach("other", 80, 24)
	h.screen("other", "No matching results")
	h.command("send-keys", "-t", "other", "Tab", "Tab", "Tab", "Tab")
	h.screen("other", "[Community]")
	h.command("send-keys", "-t", "other", "/")
	h.screen("other", "Inspect user")
	h.command("send-keys", "-t", "other", "-l", "Alice")
	h.command("send-keys", "-t", "other", "Enter")
	h.screen("other", "Status: online")
	if watches.Load() != 1 {
		t.Fatalf("frontends duplicated the shared watch: %d", watches.Load())
	}
	if err := h.client.SetPresence(context.Background(), daemon.PresenceOffline); err != nil {
		t.Fatal(err)
	}
	h.screen("community", "online (stale)")
	h.screen("other", "online (stale)")
	h.command("resize-window", "-t", "community", "-x", "20", "-y", "6")
	h.screen("community", "[Community]")
	h.command("send-keys", "-t", "community", "q")
	h.wait("Community frontend detached", func() bool { _, err := h.tmux("has-session", "-t", "community"); return err != nil })
	h.screen("other", "online (stale)")
	h.command("send-keys", "-t", "other", "q")
}
