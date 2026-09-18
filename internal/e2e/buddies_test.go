//go:build communitye2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityTerminalBuddies(t *testing.T) {
	fixtures := testutil.SocialFixtureMap(t, "login-ok", "WatchUser-request", "UnwatchUser-request", "watch-online", "status-offline", "status-online", "status-away")
	var mu sync.Mutex
	var active net.Conn
	var watches, unwatches atomic.Int32
	write := func(conn net.Conn, name string) error {
		f := fixtures[name]
		return testutil.WritePacket(conn, f.Code, f.Payload(t))
	}
	server := testutil.ListenScript(t, func(ctx context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		mu.Lock()
		active = conn
		err := write(conn, "login-ok")
		mu.Unlock()
		if err != nil {
			return err
		}
		defer func() {
			mu.Lock()
			if active == conn {
				active = nil
			}
			mu.Unlock()
		}()
		for {
			code, data, err := testutil.ReadPacket(conn)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			switch code {
			case 5:
				if bytes.Equal(data, fixtures["WatchUser-request"].Payload(t)) {
					watches.Add(1)
					mu.Lock()
					err = write(conn, "watch-online")
					mu.Unlock()
					if err != nil {
						return err
					}
				}
			case 6:
				if bytes.Equal(data, fixtures["UnwatchUser-request"].Payload(t)) {
					unwatches.Add(1)
				}
			case 22:
				return fmt.Errorf("buddy editing unexpectedly sent a PM")
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	ctx := context.Background()
	send := func(name string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		failIf(t, active == nil, "missing scripted connection")
		must(t, write(active, name))
	}
	summary := func() daemon.CommunitySummary {
		t.Helper()
		s, err := h.client.CommunitySummary(ctx)
		must(t, err)
		return s
	}
	page := func() daemon.CommunityBuddiesPage {
		t.Helper()
		p, err := h.client.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: summary().CommunityIdentity})
		must(t, err)
		return p
	}
	for _, name := range []string{"first", "second"} {
		h.attach(name, 120, 40)
		h.screen(name, "No matching results")
		h.command("send-keys", "-t", name, "Tab", "Tab", "Tab", "Tab", "C-NPage", "C-NPage")
		h.screen(name, "No matching buddies")
	}
	h.command("send-keys", "-t", "first", "a")
	h.screen("first", "Exact username")
	h.command("send-keys", "-t", "first", "-l", "Alice")
	h.command("send-keys", "-t", "first", "Tab")
	h.command("set-buffer", "--", "q/?猫😀\nline")
	h.command("paste-buffer", "-p", "-t", "first")
	h.screen("first", "q/?猫😀↵line")
	failIf(t, len(page().Buddies) != 0, "paste submitted buddy")
	for _, size := range [][2]int{{80, 24}, {40, 16}, {20, 6}, {120, 40}} {
		h.command("resize-window", "-t", "first", "-x", fmt.Sprint(size[0]), "-y", fmt.Sprint(size[1]))
		h.screen("first", "猫😀")
	}
	h.command("send-keys", "-t", "first", "Tab", "Space", "Tab", "Space", "Tab", "Space", "Enter")
	h.screen("first", "Buddy: Alice")
	h.screen("second", "Alice")
	h.wait("one shared watch", func() bool { return watches.Load() == 1 })
	buddy := page().Buddies[0]
	failIf(t, buddy.Note != "q/?猫😀\nline" || !buddy.NotifyOnline || !buddy.Priority || !buddy.Trusted || summary().BuddyNotification.Sequence != 0, "flags/paste/hydration", buddy)
	h.command("send-keys", "-t", "second", "Enter")
	h.screen("second", "Country: FR")
	h.command("send-keys", "-t", "first", "e")
	h.screen("first", "Buddy editor")
	h.command("send-keys", "-t", "first", "-l", " local draft")
	h.command("send-keys", "-t", "second", "e")
	h.screen("second", "Buddy editor")
	h.command("send-keys", "-t", "second", "-l", " second saved")
	h.command("send-keys", "-t", "second", "Enter")
	h.screen("second", "Buddy: Alice")
	h.screen("first", "local draft")
	h.command("send-keys", "-t", "first", "Enter")
	h.screen("first", "buddy changed")
	h.command("send-keys", "-t", "first", "C-r")
	h.screen("first", "[Cancel]")
	h.command("send-keys", "-t", "first", "Enter")
	h.screen("first", "local draft")
	h.command("send-keys", "-t", "first", "C-r", "Right", "Enter")
	h.screen("first", "second saved")
	h.command("send-keys", "-t", "first", "Escape")
	h.screen("first", "Buddy: Alice")
	send("status-offline")
	h.screen("first", "Status: offline")
	h.wait("observed last-seen", func() bool { return !page().Buddies[0].LastSeen.IsZero() })
	seen := page().Buddies[0].LastSeen
	send("status-online")
	h.screen("first", "Buddy \"Alice\" is online")
	h.screen("second", "Buddy \"Alice\" is online")
	send("status-online")
	send("status-away")
	h.wait("away transition", func() bool { return page().Buddies[0].Status == 1 })
	failIf(t, summary().BuddyNotification.Sequence != 1, "duplicate/away transition notified")
	// Keep an unsaved note across daemon restart. Hydration must not generate an alert.
	h.command("send-keys", "-t", "first", "e")
	h.screen("first", "Buddy editor")
	h.command("send-keys", "-t", "first", "-l", " restart draft")
	h.screen("first", "restart draft")
	h.stopDaemon()
	h.screen("second", "Community unavailable")
	h.startDaemon()
	h.wait("buddy watch restored", func() bool { return watches.Load() == 2 })
	h.screen("first", "restart draft")
	h.screen("second", "Status: online")
	buddy = page().Buddies[0]
	failIf(t, buddy.LastSeen.UnixMilli() != seen.UnixMilli() || !buddy.Priority || !buddy.Trusted || !buddy.NotifyOnline || !strings.Contains(buddy.Note, "second saved") || strings.Contains(buddy.Note, "restart draft") || summary().BuddyNotification.Sequence != 0, "restart persistence/hydration", buddy)
	h.command("send-keys", "-t", "first", "Escape")
	h.screen("first", "Buddy: Alice")
	// A separate watch consumer survives removing the buddy. Removing that last
	// consumer sends exactly one unwatch, regardless of the two attached TUIs.
	id := summary().CommunityIdentity
	must(t, h.client.WatchCommunityUsers(ctx, daemon.CommunityWatchRequest{CommunityIdentity: id, Frontend: "buddy-e2e", Users: []string{"Alice"}}))
	h.command("send-keys", "-t", "second", "D")
	h.screen("second", "[Cancel]")
	h.command("send-keys", "-t", "second", "Enter")
	h.screen("second", "Buddy: Alice")
	failIf(t, len(page().Buddies) != 1, "Cancel removed buddy")
	h.command("send-keys", "-t", "second", "D", "Right", "Enter")
	h.screen("second", "No matching buddies")
	h.screen("first", "No matching buddies")
	failIf(t, unwatches.Load() != 0, "buddy removal released another consumer's watch")
	must(t, h.client.WatchCommunityUsers(ctx, daemon.CommunityWatchRequest{CommunityIdentity: id, Frontend: "buddy-e2e"}))
	h.wait("last consumer unwatched", func() bool { return unwatches.Load() == 1 })
	h.command("send-keys", "-t", "first", "q")
	h.screen("first", "[Cancel]")
	h.screen("first", "local draft")
	h.command("send-keys", "-t", "first", "Enter")
	h.screen("first", "No matching buddies")
	h.command("send-keys", "-t", "first", "q", "Right", "Enter")
	h.wait("draft-aware detach", func() bool { _, err := h.tmux("has-session", "-t", "first"); return err != nil })
}
