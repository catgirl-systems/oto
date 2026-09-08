//go:build communitye2e

package e2e

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityTerminalPrivateChatLifecycle(t *testing.T) {
	fixtures := map[string][]byte{}
	for _, f := range testutil.SocialFixtures(t) {
		fixtures[f.Name] = f.Payload(t)
	}
	for _, name := range []string{"login-ok", "watch-online", "user-stats", "pm-online", "pm-offline", "pm-send", "pm-ack"} {
		if fixtures[name] == nil {
			t.Fatalf("missing required fixture %s", name)
		}
	}
	incoming := make(chan []byte, 100)
	sent := make(chan []byte, 10)
	var acknowledged atomic.Int32
	server := testutil.ListenScript(t, func(ctx context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		if err := testutil.WritePacket(conn, 1, fixtures["login-ok"]); err != nil {
			return err
		}
		type packet struct {
			code uint32
			data []byte
			err  error
		}
		packets := make(chan packet, 1)
		readerCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		defer func() { cancel(); _ = conn.Close(); <-done }()
		go func() {
			defer close(done)
			for {
				code, data, err := testutil.ReadPacket(conn)
				select {
				case packets <- packet{code, data, err}:
				case <-readerCtx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return nil
			case data := <-incoming:
				if err := testutil.WritePacket(conn, 22, data); err != nil {
					return err
				}
			case p := <-packets:
				if p.err == io.EOF {
					return nil
				}
				if p.err != nil {
					return p.err
				}
				switch p.code {
				case 5:
					if err := testutil.WritePacket(conn, 5, fixtures["watch-online"]); err != nil {
						return err
					}
				case 36:
					if err := testutil.WritePacket(conn, 36, fixtures["user-stats"]); err != nil {
						return err
					}
				case 22:
					sent <- p.data
				case 23:
					if len(p.data) != 4 {
						return fmt.Errorf("bad PM ack length: %d", len(p.data))
					}
					if acknowledged.Load() == 0 && !bytes.Equal(p.data, fixtures["pm-ack"]) {
						return fmt.Errorf("ack differs from independent fixture")
					}
					acknowledged.Add(1)
				}
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	navigate := func(name string) {
		h.command("send-keys", "-t", name, "Escape")
		h.screen(name, "i compose") // Separate Escape from a following legacy Alt-key sequence.
	}
	h.attach("first", 120, 40)
	h.attach("second", 80, 24)
	h.screen("first", "No matching results")
	h.screen("second", "No matching results")
	incoming <- fixtures["pm-online"]
	h.wait("durable incoming ACK", func() bool { return acknowledged.Load() == 1 })
	h.screen("first", "Unread:1")
	h.screen("second", "Unread:1")
	h.command("send-keys", "-t", "first", "Tab", "Tab", "Tab", "Tab")
	h.screen("first", "Alice")
	h.command("send-keys", "-t", "first", "Enter")
	h.screen("first", "hello 世界")
	h.wait("visible history read", func() bool {
		s, err := h.client.CommunitySummary(context.Background())
		return err == nil && s.Unread == 0
	})
	// Verify the actual outgoing bytes independently, not only a rendered echo.
	h.command("send-keys", "-t", "first", "i")
	h.command("send-keys", "-t", "first", "-l", "hello 世界")
	h.command("send-keys", "-t", "first", "Enter")
	h.wait("fixture PM sent", func() bool {
		select {
		case p := <-sent:
			if !bytes.Equal(p, fixtures["pm-send"]) {
				t.Fatalf("unexpected PM frame: %x", p)
			}
			return true
		default:
			return false
		}
	})
	h.screen("first", "[sent]")
	h.command("send-keys", "-t", "first", "-l", "q/?猫😀")
	h.command("set-buffer", "-b", "chat-paste", "\nsecond line")
	h.command("paste-buffer", "-p", "-b", "chat-paste", "-t", "first")
	h.screen("first", "Multiline")
	h.command("send-keys", "-t", "first", "Enter")
	h.screen("first", "[Cancel]")
	h.command("send-keys", "-t", "first", "Enter") // Default cancels, never sends.
	h.screen("first", "q/?猫😀")
	select {
	case <-sent:
		t.Fatal("paste or Cancel sent a PM")
	default:
	}
	h.command("send-keys", "-t", "second", "Tab", "Tab", "Tab", "Tab")
	h.screen("second", "Alice")
	h.command("send-keys", "-t", "second", "Enter")
	h.screen("second", "hello 世界")
	h.command("send-keys", "-t", "second", "i")
	h.command("send-keys", "-t", "second", "-l", "independent draft")
	h.screen("second", "independent draft")
	h.screen("first", "q/?猫😀")
	navigate("second")
	h.command("send-keys", "-t", "second", "Tab")
	h.screen("second", "[Stats]")
	// Resize the live composer, including the compact fallback, without edits.
	for _, size := range [][2]int{{80, 24}, {40, 16}, {20, 6}, {120, 40}} {
		h.command("resize-window", "-t", "first", "-x", fmt.Sprint(size[0]), "-y", fmt.Sprint(size[1]))
		h.screen("first", "猫😀")
	}
	navigate("first")
	for id := uint32(43); id <= 102; id++ {
		p := append([]byte(nil), fixtures["pm-online"]...)
		binary.LittleEndian.PutUint32(p, id)
		incoming <- p
	}
	h.wait("burst acknowledged", func() bool { return acknowledged.Load() == 61 })
	h.wait("burst loaded", func() bool {
		screen, _ := h.tmux("capture-pane", "-p", "-t", "first")
		return strings.Contains(screen, "#62 ")
	})
	h.command("send-keys", "-t", "first", "Home")
	h.screen("first", "0 new messages")
	p := append([]byte(nil), fixtures["pm-online"]...)
	binary.LittleEndian.PutUint32(p, 103)
	incoming <- p
	h.wait("new pinned message acknowledged", func() bool { return acknowledged.Load() == 62 })
	h.screen("first", "1 new messages")
	h.screen("second", "[Stats]") // Incoming must never steal workspace focus.
	h.command("send-keys", "-t", "first", "End", "i")
	h.screen("first", "q/?猫😀")
	h.command("send-keys", "-t", "first", "Enter", "Right", "Enter")
	h.wait("deliberate multiline PM", func() bool {
		select {
		case payload := <-sent:
			want := "q/?猫😀 second line"
			if len(payload) != 13+len(want) || !bytes.Equal(payload[:9], fixtures["pm-send"][:9]) || binary.LittleEndian.Uint32(payload[9:13]) != uint32(len(want)) || string(payload[13:]) != want {
				t.Fatal("multiline preview did not match server-compatible wire text")
			}
			return true
		default:
			return false
		}
	})
	h.screen("first", "[sent]")
	// Offline sends remain durable and expose usable cancel/retry controls.
	if err := h.client.SetPresence(context.Background(), daemon.PresenceOffline); err != nil {
		t.Fatal(err)
	}
	h.screen("first", "Offline")
	h.command("send-keys", "-t", "first", "-l", "queued across restart")
	h.command("send-keys", "-t", "first", "Enter")
	h.screen("first", "[queued]")
	navigate("first")
	h.command("send-keys", "-t", "first", "X")
	h.screen("first", "[Cancel]")
	h.command("send-keys", "-t", "first", "Right", "Enter")
	h.screen("first", "[cancelled]")
	h.command("send-keys", "-t", "first", "R", "Right", "Enter")
	h.screen("first", "[queued]")
	h.command("send-keys", "-t", "first", "q")
	h.wait("first detached", func() bool { _, err := h.tmux("has-session", "-t", "first"); return err != nil })
	h.command("send-keys", "-t", "second", "BTab", "i")
	h.screen("second", "independent draft")
	navigate("second")
	h.command("send-keys", "-t", "second", "q")
	h.screen("second", "[Cancel]")
	h.command("send-keys", "-t", "second", "Right", "Enter")
	h.wait("second detached", func() bool { _, err := h.tmux("has-session", "-t", "second"); return err != nil })
	before, err := h.client.CommunitySummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.stopDaemon()
	h.startDaemon()
	after, err := h.client.CommunitySummary(context.Background())
	if err != nil || after.Daemon == before.Daemon {
		t.Fatal("daemon did not restart", err)
	}
	h.wait("offline outbox restored", func() bool {
		select {
		case <-sent:
			return true
		default:
			return false
		}
	})
	h.attach("reattached", 120, 40)
	h.screen("reattached", "No matching results")
	h.command("send-keys", "-t", "reattached", "Tab", "Tab", "Tab", "Tab")
	h.screen("reattached", "Alice")
	h.command("send-keys", "-t", "reattached", "Enter")
	h.screen("reattached", "queued across restart")
	h.screen("reattached", "[sent]")
	// Clear is deliberate and replay receipts survive both clear and restart.
	h.command("send-keys", "-t", "reattached", "C")
	h.screen("reattached", "[Cancel]")
	h.command("send-keys", "-t", "reattached", "Right", "Enter")
	h.screen("reattached", "No messages in this page")
	incoming <- fixtures["pm-offline"]
	h.wait("cleared replay acknowledged", func() bool { return acknowledged.Load() == 63 })
	page, err := h.client.CommunityConversations(context.Background(), daemon.CommunityConversationsRequest{CommunityIdentity: after.CommunityIdentity, Kind: "private"})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].LatestID != 0 || page.Conversations[0].Unread != 0 {
		t.Fatal("replay resurrected cleared content", page, err)
	}
	h.screen("reattached", "No messages in this page")
	h.command("send-keys", "-t", "reattached", "q")
}
