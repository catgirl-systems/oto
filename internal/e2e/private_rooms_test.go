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

type privateRoomProgress struct {
	joins, leaves, walls, roleWrites atomic.Int32
	role                             atomic.Int32 // -1 not created, -2 revoked, 0 member, 2 owner.
	invitations                      atomic.Bool
}

// Advanced private-room state is scripted because the pinned local Soulfind
// does not implement it. Every fixed frame is independently reference-checked.
func privateRoomScript(t *testing.T) (*testutil.ScriptSocket, *privateRoomProgress) {
	t.Helper()
	fixtures := map[string]testutil.WireFixture{}
	for _, f := range testutil.SocialFixtures(t) {
		fixtures[f.Name] = f
	}
	for _, name := range []string{"login-ok", "private-directory-empty", "private-directory-owner", "private-directory-member", "private-joined-owner", "room-join-private", "room-left", "role-members", "wall-snapshot", "wall-set", "wall-clear", "role-add-member-request", "role-remove-member-request", "role-add-operator-request", "role-remove-operator-request", "role-cancel-membership-request", "role-cancel-ownership-request", "role-membership-revoked"} {
		if _, ok := fixtures[name]; !ok {
			t.Fatalf("missing mandatory fixture %s", name)
		}
	}
	progress := &privateRoomProgress{}
	progress.role.Store(-1)
	server := testutil.ListenScript(t, func(ctx context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		write := func(name string) error {
			f, ok := fixtures[name]
			if !ok {
				return fmt.Errorf("missing mandatory fixture %s", name)
			}
			return testutil.WritePacket(conn, f.Code, f.Payload(t))
		}
		directory := func() error {
			name := "private-directory-empty"
			if progress.role.Load() == 2 {
				name = "private-directory-owner"
			} else if progress.role.Load() == 0 {
				name = "private-directory-member"
			}
			return write(name)
		}
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
				if err = directory(); err != nil {
					return err
				}
			case 14:
				progress.joins.Add(1)
				if !bytes.Equal(data, fixtures["room-join-private"].Payload(t)) {
					return fmt.Errorf("private join differs from reference fixture")
				}
				if progress.role.Load() == -2 {
					return fmt.Errorf("revoked room automatically rejoined")
				}
				if progress.role.Load() == -1 {
					progress.role.Store(2)
				}
				if err = write("private-joined-owner"); err != nil {
					return err
				}
				if err = write("role-members"); err != nil {
					return err
				}
				if err = write("wall-snapshot"); err != nil {
					return err
				}
			case 15:
				progress.leaves.Add(1)
				if !bytes.Equal(data, fixtures["room-leave"].Payload(t)) {
					return fmt.Errorf("leave differs from reference fixture")
				}
				if err = write("room-left"); err != nil {
					return err
				}
			case 141:
				if len(data) != 1 || data[0] > 1 {
					return fmt.Errorf("invalid invitation preference")
				}
				progress.invitations.Store(data[0] == 1)
				if err = testutil.WritePacket(conn, 141, data); err != nil {
					return err
				}
			case 116:
				clear := bytes.Equal(data, fixtures["wall-clear"].Payload(t))
				if !clear && !bytes.Equal(data, fixtures["wall-set"].Payload(t)) {
					return fmt.Errorf("wall differs from reference fixture")
				}
				progress.walls.Add(1)
				// Insert the sending username into the reference room/text packet.
				n := 4 + int(binary.LittleEndian.Uint32(data))
				echo := append([]byte{}, data[:n]...)
				echo = binary.LittleEndian.AppendUint32(echo, uint32(len("terminal")))
				echo = append(echo, "terminal"...)
				if clear {
					err = testutil.WritePacket(conn, 115, echo)
				} else {
					echo = append(echo, data[n:]...)
					err = testutil.WritePacket(conn, 114, echo)
				}
				if err != nil {
					return err
				}
			case 134, 135, 143, 144, 136, 137:
				progress.roleWrites.Add(1)
				names := map[uint32]string{134: "add-member", 135: "remove-member", 143: "add-operator", 144: "remove-operator", 136: "cancel-membership", 137: "cancel-ownership"}
				name := names[code]
				if !bytes.Equal(data, fixtures["role-"+name+"-request"].Payload(t)) {
					return fmt.Errorf("role change differs from reference fixture")
				}
				if code == 136 {
					if progress.role.Load() != 0 {
						return fmt.Errorf("membership cancellation sent with wrong role")
					}
					progress.role.Store(-2)
					if err = write("role-membership-revoked"); err != nil {
						return err
					}
				} else if code == 137 {
					if progress.role.Load() != 2 {
						return fmt.Errorf("non-owner relinquished ownership")
					}
					progress.role.Store(0)
					if err = directory(); err != nil {
						return err
					}
				} else {
					if progress.role.Load() != 2 {
						return fmt.Errorf("unauthorized management write")
					}
					if err = write("role-" + name); err != nil {
						return err
					}
				}
			case 13:
				return fmt.Errorf("room wall editing sent chat text")
			}
		}
	})
	return server, progress
}

func TestCommunityTerminalPrivateRooms(t *testing.T) {
	server, progress := privateRoomScript(t)
	h := newTerminal(t, server.Listener.Addr().String())
	h.attach("private", 120, 40)
	h.screen("private", "No matching results")
	h.command("send-keys", "-t", "private", "Tab", "Tab", "Tab", "Tab", "C-NPage")
	h.screen("private", "N join/create")
	h.command("send-keys", "-t", "private", "N", "C-p")
	h.screen("private", "Create private room")
	h.command("send-keys", "-t", "private", "-l", "oto test")
	h.command("send-keys", "-t", "private", "Tab", "Enter")
	h.wait("private creation", func() bool { return progress.joins.Load() == 1 })
	h.screen("private", "oto test")
	h.command("send-keys", "-t", "private", "Enter")
	h.screen("private", "joined")
	h.command("send-keys", "-t", "private", "W")
	h.screen("private", "Room wall")
	h.screen("private", "Bob: hello 世界")
	h.command("send-keys", "-t", "private", "i")
	h.screen("private", "local draft")
	h.command("set-buffer", "--", "hello\n世界")
	h.command("paste-buffer", "-p", "-t", "private")
	h.screen("private", "hello↵世界")
	if progress.walls.Load() != 0 {
		t.Fatal("paste submitted wall")
	}
	h.command("send-keys", "-t", "private", "Enter")
	h.screen("private", "[Cancel]")
	h.command("send-keys", "-t", "private", "Enter")
	h.screen("private", "local draft")
	if progress.walls.Load() != 0 {
		t.Fatal("Cancel submitted wall")
	}
	h.command("send-keys", "-t", "private", "Enter", "Right", "Enter")
	h.wait("own wall confirmed", func() bool { return progress.walls.Load() == 1 })
	h.screen("private", "terminal: hello 世界")
	h.stopDaemon()
	h.screen("private", "Community unavailable")
	h.startDaemon()
	h.wait("own wall restored after daemon restart", func() bool { return progress.joins.Load() == 2 && progress.walls.Load() == 2 })
	h.screen("private", "Room wall · fresh · confirmed")
	h.screen("private", "terminal: hello 世界")
	h.command("send-keys", "-t", "private", "Escape")
	h.screen("private", "i compose")
	h.command("send-keys", "-t", "private", "L", "Right", "Enter")
	h.wait("private leave", func() bool { return progress.leaves.Load() == 1 })
	h.screen("private", "not-joined")
	h.command("send-keys", "-t", "private", "J")
	h.wait("own wall restored after rejoin", func() bool { return progress.joins.Load() == 3 && progress.walls.Load() == 3 })
	h.screen("private", "joined")
	h.command("send-keys", "-t", "private", "W", "C")
	h.screen("private", "[Cancel]")
	h.command("send-keys", "-t", "private", "Enter")
	h.screen("private", "Room wall")
	if progress.walls.Load() != 3 {
		t.Fatal("Cancel cleared wall")
	}
	h.command("send-keys", "-t", "private", "C", "Right", "Enter")
	h.wait("clear own wall", func() bool { return progress.walls.Load() == 4 })
	h.screen("private", "Bob: hello 世界")
	h.command("send-keys", "-t", "private", "i")
	h.screen("private", "local draft")
	h.command("send-keys", "-t", "private", "-l", "q/?猫😀")
	for _, size := range [][2]int{{80, 24}, {40, 16}, {20, 6}, {120, 40}} {
		h.command("resize-window", "-t", "private", "-x", fmt.Sprint(size[0]), "-y", fmt.Sprint(size[1]))
		h.screen("private", "猫😀")
	}
	if progress.walls.Load() != 4 {
		t.Fatal("editing sent wall")
	}
	h.command("send-keys", "-t", "private", "Escape")
	h.screen("private", "Room wall")
	h.command("send-keys", "-t", "private", "Escape")
	h.screen("private", "i compose")
	h.command("send-keys", "-t", "private", "M")
	h.screen("private", "Role: owner (fresh)")
	h.screen("private", "Bob")
	h.command("send-keys", "-t", "private", "Home", "Down", "d")
	h.screen("private", "exact user \"Bob\"")
	h.screen("private", "[Cancel]")
	h.command("send-keys", "-t", "private", "Enter")
	h.screen("private", "Private roles")
	if progress.roleWrites.Load() != 0 {
		t.Fatal("Cancel changed membership")
	}
	h.command("send-keys", "-t", "private", "d", "Right", "Enter")
	h.screen("private", "Last remove-member: confirmed")
	h.command("send-keys", "-t", "private", "a")
	h.screen("private", "exact username")
	h.command("send-keys", "-t", "private", "-l", "Bob")
	h.command("send-keys", "-t", "private", "Enter", "Right", "Enter")
	h.screen("private", "Last add-member: confirmed")
	h.command("send-keys", "-t", "private", "A")
	h.screen("private", "exact username")
	h.command("send-keys", "-t", "private", "-l", "Bob")
	h.command("send-keys", "-t", "private", "Enter", "Right", "Enter")
	h.screen("private", "Last add-operator: confirmed")
	h.command("send-keys", "-t", "private", "Home", "Down", "O", "Right", "Enter")
	h.screen("private", "Last remove-operator: confirmed")
	if progress.roleWrites.Load() != 4 {
		t.Fatal("unexpected management writes", progress.roleWrites.Load())
	}
	h.command("send-keys", "-t", "private", "I", "Right", "Enter")
	h.wait("invitations disabled", func() bool { return !progress.invitations.Load() })
	h.screen("private", "Invitations: false")
	h.command("send-keys", "-t", "private", "C", "Right", "Enter")
	h.screen("private", "Role: member (fresh)")
	h.command("send-keys", "-t", "private", "a")
	h.screen("private", "does not permit")
	if progress.roleWrites.Load() != 5 {
		t.Fatal("member sent management action")
	}
	h.command("send-keys", "-t", "private", "c", "Right", "Enter")
	h.wait("membership revoked", func() bool { return progress.role.Load() == -2 })
	h.screen("private", "revoked")
	h.command("send-keys", "-t", "private", "q")
	h.screen("private", "[Cancel]")
	h.command("send-keys", "-t", "private", "Right", "Enter")
	h.wait("private frontend detached", func() bool { _, err := h.tmux("has-session", "-t", "private"); return err != nil })
	h.stopDaemon()
	progress.invitations.Store(true)
	h.startDaemon()
	h.wait("invitation preference restored", func() bool { return !progress.invitations.Load() })
	summary, err := h.client.CommunitySummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.client.CommunityRooms(context.Background(), daemon.CommunityRoomsRequest{CommunityIdentity: summary.CommunityIdentity, Room: "oto test"})
	if err != nil || len(page.Rooms) != 1 || page.Rooms[0].Remembered || progress.joins.Load() != 3 {
		t.Fatal("revoked room rejoined or lost retained history", err, page)
	}
}
