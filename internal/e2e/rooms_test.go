//go:build communitye2e

package e2e

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/testutil"
)

// Real binary/PTY/daemon/IPC, with a deliberately scripted, reference-checked
// local server. Real-server room interoperability is a separate Soulfind test.
func TestCommunityTerminalPublicRooms(t *testing.T) {
	fixtures := testutil.SocialFixtureMap(t, "login-ok", "room-directory", "room-joined", "room-leave", "room-echo", "room-send", "public-feed-message", "public-feed-subscribe")
	var joins, leaves, sends, feeds atomic.Int32
	server := testutil.ListenScript(t, func(ctx context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		write := func(name string) error { f := fixtures[name]; return testutil.WritePacket(conn, f.Code, f.Payload(t)) }
		if err := write("login-ok"); err != nil {
			return err
		}
		for {
			code, data, err := testutil.ReadPacket(conn)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			switch code {
			case 64:
				if err := write("room-directory"); err != nil {
					return err
				}
			case 14:
				if !bytes.Equal(data, fixtures["room-join-public"].Payload(t)) {
					return fmt.Errorf("join differs from independent wire fixture")
				}
				joins.Add(1)
				if err := write("room-joined"); err != nil {
					return err
				}
				if err := write("room-echo"); err != nil {
					return err
				}
			case 15:
				leaves.Add(1)
				if err := write("room-leave"); err != nil {
					return err
				}
			case 13:
				if !bytes.Equal(data, fixtures["room-send"].Payload(t)) {
					return fmt.Errorf("room send differs from independent fixture")
				}
				sends.Add(1)
				// Server echo adds the exact local username between room and text.
				n := 4 + int(binary.LittleEndian.Uint32(data))
				echo := append([]byte{}, data[:n]...)
				echo = binary.LittleEndian.AppendUint32(echo, uint32(len("terminal")))
				echo = append(echo, "terminal"...)
				echo = append(echo, data[n:]...)
				if err := testutil.WritePacket(conn, 13, echo); err != nil {
					return err
				}
			case 150:
				feeds.Add(1)
				if err := write("public-feed-message"); err != nil {
					return err
				}
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	h.attach("rooms", 120, 40)
	h.screen("rooms", "No matching results")
	h.command("send-keys", "-t", "rooms", "Tab", "Tab", "Tab", "Tab", "C-NPage")
	h.screen("rooms", "N join/create")
	h.command("send-keys", "-t", "rooms", "N")
	h.screen("rooms", "Join/create public room")
	h.command("send-keys", "-t", "rooms", "-l", "oto test")
	h.command("send-keys", "-t", "rooms", "Tab", "Enter")
	h.wait("join confirmed", func() bool { return joins.Load() == 1 })
	h.screen("rooms", "oto test")
	// Filter avoids relying on a directory's sort order or incoming population.
	h.command("send-keys", "-t", "rooms", "f")
	h.command("send-keys", "-t", "rooms", "-l", "oto test")
	h.command("send-keys", "-t", "rooms", "Enter")
	h.screen("rooms", "Find: oto test")
	h.command("send-keys", "-t", "rooms", "Enter")
	h.screen("rooms", "hello 世界")
	h.screen("rooms", "joined")
	h.command("send-keys", "-t", "rooms", "i")
	h.command("send-keys", "-t", "rooms", "-l", "hello 世界")
	h.command("send-keys", "-t", "rooms", "Enter")
	h.wait("room frame written", func() bool { return sends.Load() == 1 })
	h.screen("rooms", "[sent]")
	h.command("send-keys", "-t", "rooms", "-l", "q/?猫😀")
	for _, size := range [][2]int{{80, 24}, {40, 16}, {20, 6}, {120, 40}} {
		h.command("resize-window", "-t", "rooms", "-x", fmt.Sprint(size[0]), "-y", fmt.Sprint(size[1]))
		h.screen("rooms", "猫😀")
	}
	h.command("send-keys", "-t", "rooms", "Escape")
	h.screen("rooms", "i compose")
	h.command("send-keys", "-t", "rooms", "F6")
	h.screen("rooms", "Members")
	h.command("send-keys", "-t", "rooms", "Enter")
	h.screen("rooms", "User:")
	h.command("send-keys", "-t", "rooms", "Escape")
	h.screen("rooms", "Members")
	h.command("send-keys", "-t", "rooms", "BTab") // Outside editing retains workspace Tab behavior.
	h.screen("rooms", "[Transfers")
	h.command("send-keys", "-t", "rooms", "Tab", "F6") // pane 2 -> list
	h.screen("rooms", "N join/create")
	h.command("send-keys", "-t", "rooms", "Enter", "L")
	h.screen("rooms", "[Cancel]")
	h.command("send-keys", "-t", "rooms", "Enter")
	if leaves.Load() != 0 {
		t.Fatal("Cancel left room")
	}
	h.command("send-keys", "-t", "rooms", "L", "Right", "Enter")
	h.wait("leave confirmed", func() bool { return leaves.Load() == 1 })
	h.screen("rooms", "not-joined")
	h.command("send-keys", "-t", "rooms", "J")
	h.wait("rejoin confirmed", func() bool { return joins.Load() == 2 })
	h.screen("rooms", "History gap")
	h.command("send-keys", "-t", "rooms", "G")
	h.screen("rooms", "Public feed")
	if feeds.Load() != 0 {
		t.Fatal("viewing feed implicitly subscribed")
	}
	h.command("send-keys", "-t", "rooms", "g")
	h.wait("explicit feed subscription", func() bool { return feeds.Load() == 1 })
	h.screen("rooms", "Bob in oto test")
	h.command("send-keys", "-t", "rooms", "i", "Enter")
	if sends.Load() != 1 {
		t.Fatal("feed sent chat")
	}
	h.command("send-keys", "-t", "rooms", "Escape")
	h.screen("rooms", "i compose")
	if err := h.client.SetPresence(context.Background(), daemon.PresenceOffline); err != nil {
		t.Fatal(err)
	}
	h.screen("rooms", "offline; history")
	h.command("send-keys", "-t", "rooms", "q")
	h.screen("rooms", "[Cancel]")
	h.command("send-keys", "-t", "rooms", "Right", "Enter")
	h.wait("frontend detached", func() bool { _, err := h.tmux("has-session", "-t", "rooms"); return err != nil })
	h.stopDaemon()
	h.startDaemon()
	h.wait("remembered room restored", func() bool { return joins.Load() == 3 })
	summary, err := h.client.CommunitySummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.client.CommunityRooms(context.Background(), daemon.CommunityRoomsRequest{CommunityIdentity: summary.CommunityIdentity, Room: "oto test"})
	if err != nil || len(page.Rooms) != 1 || !page.Rooms[0].Remembered {
		t.Fatal("remembered preference lost", err, page)
	}
}
