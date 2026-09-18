//go:build communitye2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityBroadcastCommandsAndPacedOutcomes(t *testing.T) {
	login := testutil.SocialFixture(t, "login-ok").Payload(t)
	var sent atomic.Int32
	arrivals := make(chan time.Time, 4)
	server := testutil.ListenScript(t, func(_ context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		if err := testutil.WritePacket(conn, 1, login); err != nil {
			return err
		}
		for {
			code, body, err := testutil.ReadPacket(conn)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if code == 22 {
				d := soulseek.NewDecoder(body)
				name, e1 := d.String()
				text, e2 := d.String()
				if e1 != nil || e2 != nil || (name != "Alice" && name != "Bob") || text != "hello 世界" {
					return fmt.Errorf("unexpected broadcast message")
				}
				sent.Add(1)
				arrivals <- time.Now()
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	h.attach("broadcast", 120, 40)
	h.screen("broadcast", "No matching results")
	ctx := context.Background()
	var summary daemon.CommunitySummary
	h.wait("connected", func() bool {
		var err error
		summary, err = h.client.CommunitySummary(ctx)
		return err == nil && summary.Connected
	})
	for _, name := range []string{"Alice", "Bob"} {
		if _, err := h.client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: summary.CommunityIdentity, Username: name}); err != nil {
			t.Fatal(err)
		}
	}
	command := func(args ...string) daemon.CommandResult {
		t.Helper()
		cmd := exec.Command(binaryPath, append([]string{"command"}, args...)...)
		cmd.Env = h.env
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command failed: %s: %v", data, err)
		}
		var out daemon.CommandResult
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	preview := command("broadcast", "buddies", "hello 世界", "Alice", "Bob").Broadcast
	if preview == nil || preview.Total != 2 || preview.Text != "hello 世界" || sent.Load() != 0 {
		t.Fatal(preview, sent.Load())
	}
	if out := command("broadcast-send", preview.RequestID, preview.Token); out.Broadcast == nil || out.Broadcast.State != "preview" || sent.Load() != 0 {
		t.Fatal("implicit confirmation", out)
	}
	args := []string{"--confirm", "--request-id", preview.RequestID, "--account", preview.Account, "--daemon", preview.Daemon, "--session", strconv.FormatUint(preview.Session, 10), "broadcast-send", preview.RequestID, preview.Token}
	command(args...)
	h.wait("two paced messages", func() bool { return sent.Load() == 2 })
	first, second := <-arrivals, <-arrivals
	if second.Sub(first) < time.Second {
		t.Fatal("unpaced broadcast")
	}
	h.wait("persisted per-recipient outcomes", func() bool {
		out, err := h.client.CommunityBroadcast(ctx, preview.CommunityIdentity, preview.RequestID, 0)
		return err == nil && out.State == "completed" && out.Recipients[0].State == "sent" && out.Recipients[1].State == "sent"
	})
	if out := command(args...); out.Broadcast == nil || out.Broadcast.State != "completed" {
		t.Fatal("confirmation retry", out)
	}
	h.command("send-keys", "-t", "broadcast", "Tab", "Tab", "Tab", "Tab")
	h.screen("broadcast", "Alice")
	h.command("send-keys", "-t", "broadcast", "Enter")
	h.screen("broadcast", "hello 世界")
	h.command("send-keys", "-t", "broadcast", "i")
	h.command("send-keys", "-t", "broadcast", "-l", "/broadcast-show "+preview.RequestID)
	h.command("send-keys", "-t", "broadcast", "Enter")
	h.screen("broadcast", "Command result")
	h.screen("broadcast", "Broadcast · buddies · completed")
	if sent.Load() != 2 {
		t.Fatal("broadcast repeated", sent.Load())
	}
	h.command("send-keys", "-t", "broadcast", "Escape")
	h.screen("broadcast", "Enter send")
	h.command("send-keys", "-t", "broadcast", "-l", `/broadcast buddies "hello 世界" Alice Bob`)
	h.command("send-keys", "-t", "broadcast", "Enter")
	h.screen("broadcast", "Broadcast · buddies · preview")
	screen := h.command("capture-pane", "-p", "-t", "broadcast")
	requestID := ""
	for _, line := range strings.Split(screen, "\n") {
		if strings.HasPrefix(line, "Request: ") {
			requestID = strings.TrimSpace(strings.TrimPrefix(line, "Request: "))
		}
	}
	if requestID == "" {
		t.Fatal("missing broadcast request ID", screen)
	}
	h.command("send-keys", "-t", "broadcast", "s")
	h.screen("broadcast", "[Cancel]")
	h.command("send-keys", "-t", "broadcast", "Enter")
	h.screen("broadcast", "Broadcast · buddies · preview")
	if sent.Load() != 2 {
		t.Fatal("default confirmation sent messages")
	}
	h.command("send-keys", "-t", "broadcast", "s")
	h.screen("broadcast", "[Cancel]")
	h.command("send-keys", "-t", "broadcast", "Right", "Enter")
	h.wait("TUI broadcast completed", func() bool {
		out, err := h.client.CommunityBroadcast(ctx, preview.CommunityIdentity, requestID, 0)
		return err == nil && out.State == "completed" && sent.Load() == 4
	})
	h.command("send-keys", "-t", "broadcast", "r")
	h.screen("broadcast", "Broadcast · buddies · completed")
}
