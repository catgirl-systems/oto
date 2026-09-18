//go:build communitye2e

package e2e

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityTerminalDiscoveryAndProfiles(t *testing.T) {
	fixtures := testutil.SocialFixtureMap(t)
	fixture := func(name string) testutil.WireFixture {
		t.Helper()
		f, ok := fixtures[name]
		failIfFmt(t, !ok, "missing mandatory fixture %s", name)
		return f
	}
	var failProfile atomic.Bool
	var profiles atomic.Int32
	peer := testutil.ListenScript(t, func(ctx context.Context, c net.Conn) error {
		var length uint32
		if err := binary.Read(c, binary.LittleEndian, &length); err != nil {
			return err
		}
		if length < 1 || length > 4096 {
			return fmt.Errorf("unexpected peer init length %d", length)
		}
		init := make([]byte, length)
		if _, err := io.ReadFull(c, init); err != nil {
			return err
		}
		if init[0] != 1 {
			return fmt.Errorf("not PeerInit: %d", init[0])
		}
		code, payload, err := testutil.ReadPacket(c)
		if err != nil {
			return err
		}
		if code != 15 || len(payload) != 0 {
			return fmt.Errorf("unexpected peer request %d", code)
		}
		profiles.Add(1)
		if failProfile.Load() {
			return c.Close()
		}
		f := fixture("profile-picture")
		return testutil.WritePacket(c, f.Code, f.Payload(t))
	})
	requests := map[uint32]string{51: "interest-add-like", 52: "interest-remove-like", 117: "interest-add-dislike", 118: "interest-remove-dislike", 54: "discovery-personal-request", 56: "discovery-global-request", 110: "discovery-similar-request", 111: "discovery-item-request", 112: "discovery-item-users-request", 57: "discovery-user-interests-request"}
	replies := map[uint32]string{54: "discovery-personal", 56: "discovery-global", 110: "discovery-similar", 111: "discovery-item", 112: "discovery-item-users", 57: "discovery-user-interests"}
	var mu sync.Mutex
	counts := map[uint32]int{}
	count := func(code uint32) int { mu.Lock(); defer mu.Unlock(); return counts[code] }
	server := testutil.ListenScript(t, func(ctx context.Context, c net.Conn) error {
		if _, err := testutil.WaitForPacket(c, 1); err != nil {
			return err
		}
		login := fixture("login-ok")
		if err := testutil.WritePacket(c, login.Code, login.Payload(t)); err != nil {
			return err
		}
		for {
			code, data, err := testutil.ReadPacket(c)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if name, ok := requests[code]; ok {
				if !bytes.Equal(data, fixture(name).Payload(t)) {
					return fmt.Errorf("request %d differs from independent fixture", code)
				}
				mu.Lock()
				counts[code]++
				mu.Unlock()
				if name, ok := replies[code]; ok {
					f := fixture(name)
					if err := testutil.WritePacket(c, f.Code, f.Payload(t)); err != nil {
						return err
					}
				}
			}
			switch code {
			case 3:
				if bytes.Equal(data, fixture("GetPeerAddress-request").Payload(t)) {
					f := fixture("peer-address")
					payload := f.Payload(t)
					// Only the ephemeral loopback port differs from the reference-checked frame.
					binary.LittleEndian.PutUint32(payload[13:17], uint32(peer.Listener.Addr().(*net.TCPAddr).Port))
					if err := testutil.WritePacket(c, f.Code, payload); err != nil {
						return err
					}
				}
			case 5:
				if bytes.Equal(data, fixture("WatchUser-request").Payload(t)) {
					f := fixture("watch-online")
					if err := testutil.WritePacket(c, f.Code, f.Payload(t)); err != nil {
						return err
					}
				}
			case 7:
				if !bytes.Equal(data, fixture("GetUserStatus-request").Payload(t)) {
					return fmt.Errorf("unexpected status target")
				}
				f := fixture("status-online")
				if err := testutil.WritePacket(c, f.Code, f.Payload(t)); err != nil {
					return err
				}
			case 22:
				return fmt.Errorf("discovery editing unexpectedly sent a PM")
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	ctx := context.Background()
	identity := func() daemon.CommunityIdentity {
		t.Helper()
		s, err := h.client.CommunitySummary(ctx)
		must(t, err)
		return s.CommunityIdentity
	}
	interests := func() daemon.CommunityInterestsPage {
		t.Helper()
		p, err := h.client.CommunityInterests(ctx, daemon.CommunityInterestsRequest{CommunityIdentity: identity()})
		must(t, err)
		return p
	}
	self := func() daemon.CommunitySelfProfile {
		t.Helper()
		p, err := h.client.CommunitySelfProfile(ctx, identity())
		must(t, err)
		return p
	}
	keys := func(name string, key ...string) {
		args := append([]string{"send-keys", "-t", name}, key...)
		h.command(args...)
	}
	for _, name := range []string{"first", "second"} {
		h.attach(name, 120, 40)
		h.screen(name, "No matching results")
		keys(name, "Tab", "Tab", "Tab", "Tab", "C-NPage", "C-NPage", "C-NPage", "Enter")
		h.screen(name, "No interests saved.")
	}
	modes := map[string]int{"first": 0, "second": 0}
	mode := func(name string, next int) {
		t.Helper()
		keys(name, "S-F6")
		for current := modes[name]; current != next; current = (current + 1) % 8 {
			keys(name, "Down")
		}
		modes[name] = next
		keys(name, "Enter")
	}
	keys("first", "a")
	h.screen("first", "like")
	h.command("set-buffer", "--", "TeChNo")
	h.command("paste-buffer", "-p", "-t", "first")
	h.screen("first", "TeChNo")
	failIf(t, len(interests().Interests) != 0, "paste submitted interest")
	keys("first", "Enter")
	h.wait("normalized like written", func() bool { return count(51) == 1 })
	h.screen("first", "techno (like · sent)")
	h.screen("second", "techno")
	keys("first", "e", "Tab", "Enter")
	h.wait("opinion replacement written", func() bool { return count(52) == 1 && count(117) == 1 })
	h.screen("first", "techno (dislike · sent)")
	keys("first", "D")
	h.screen("first", "[Cancel]")
	keys("first", "Enter")
	failIf(t, len(interests().Interests) != 1, "Cancel removed interest")
	// Each frontend keeps its own description edit, with an explicit stale-edit conflict.
	mode("first", 7)
	mode("second", 7)
	keys("first", "e")
	h.screen("first", "Edit self-description")
	h.command("set-buffer", "--", "q/?猫😀\nline")
	h.command("paste-buffer", "-p", "-t", "first")
	h.screen("first", "q/?猫😀↵line")
	failIf(t, self().Description != "", "description paste submitted")
	for _, size := range [][2]int{{80, 24}, {40, 16}, {20, 6}, {120, 40}} {
		h.command("resize-window", "-t", "first", "-x", fmt.Sprint(size[0]), "-y", fmt.Sprint(size[1]))
		h.wait("resized description editor", func() bool {
			screen, err := h.tmux("capture-pane", "-p", "-t", "first")
			if err != nil {
				return false
			}
			if size[0] < 36 {
				return strings.Contains(screen, "Edit self-") && !strings.Contains(screen, "╭")
			}
			return strings.Contains(screen, "╭"+strings.Repeat("─", size[0]-2)+"╮")
		})
		h.screen("first", "猫😀")
	}
	keys("second", "e")
	h.screen("second", "Edit self-description")
	keys("second", "-l", "other frontend")
	keys("second", "Enter")
	h.screen("second", "Description:")
	h.wait("second description committed", func() bool { return self().Description == "other frontend" })
	keys("first", "Enter")
	h.screen("first", "description changed")
	keys("first", "C-r")
	h.screen("first", "[Cancel]")
	keys("first", "Enter")
	h.screen("first", "q/?猫😀↵line")
	keys("first", "C-r", "Right", "Enter")
	h.screen("first", "other frontend")
	keys("first", "e")
	h.screen("first", "Edit self-description")
	keys("first", "C-a", "C-k")
	h.command("set-buffer", "--", "q/?猫😀\nline")
	h.command("paste-buffer", "-p", "-t", "first")
	keys("first", "Enter")
	h.wait("Unicode description saved", func() bool { return self().Description == "q/?猫😀\nline" })
	// Exercise personal/global/item recommendations and exact-case similar users.
	mode("first", 1)
	h.wait("personal request", func() bool { return count(54) > 0 })
	h.screen("first", "score 42")
	h.screen("first", "score -7")
	mode("first", 2)
	h.wait("global request", func() bool { return count(56) > 0 })
	h.screen("first", "score 42")
	keys("first", "Home", "i")
	modes["first"] = 4
	h.wait("item request", func() bool { return count(111) > 0 })
	h.screen("first", "Target: techno")
	h.screen("first", "score 42")
	keys("first", "Home", "u")
	modes["first"] = 5
	h.wait("item-users request", func() bool { return count(112) > 0 })
	h.screen("first", "Alice")
	h.screen("first", "alice")
	mode("first", 3)
	h.wait("similar-users request", func() bool { return count(110) > 0 })
	h.screen("first", "Alice · rating 9")
	h.screen("first", "alice · rating 2")
	keys("first", "Home", "U")
	h.screen("first", "User actions")
	h.screen("first", "Message user")
	keys("first", "Escape")
	h.screen("first", "rating 9")
	keys("first", "Home", "Enter")
	h.screen("first", "hello 世界")
	h.screen("first", "Queue: 7")
	h.screen("first", "Country: \"FR\"")
	h.screen("first", "Supporter: yes")
	h.screen("first", "127.0.0.1")
	h.screen("first", "like: techno")
	keys("first", "P")
	h.screen("first", "Save picture for Alice")
	path := filepath.Join(t.TempDir(), "portrait.png")
	keys("first", "C-a", "C-k")
	h.command("set-buffer", "--", path)
	h.command("paste-buffer", "-p", "-t", "first")
	keys("first", "Enter")
	h.screen("first", "[Cancel]")
	keys("first", "Enter")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Cancel saved picture")
	}
	keys("first", "Enter", "Right", "Enter")
	h.screen("first", "Saved profile picture")
	data, err := os.ReadFile(path)
	failIf(t, err != nil || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")), "picture export", err)
	// A failed explicit refresh retains useful description, picture and server metadata.
	failProfile.Store(true)
	before := profiles.Load()
	keys("first", "r")
	h.wait("profile refresh attempted", func() bool { return profiles.Load() > before })
	h.screen("first", "Peer profile: stale")
	h.screen("first", "hello 世界")
	h.screen("first", "Queue (stale): 7")
	keys("first", "Escape") // inspector -> discovery content
	h.screen("first", "[Content]")
	mode("first", 7)
	keys("first", "e")
	h.screen("first", "Edit self-description")
	keys("first", "-l", " unsaved")
	h.stopDaemon()
	h.screen("second", "Community unavailable")
	h.startDaemon()
	h.wait("interest replay after restart", func() bool { return count(117) == 2 })
	h.screen("first", "unsaved")
	h.screen("second", "猫😀")
	failIf(t, self().Description != "q/?猫😀\nline" || len(interests().Interests) != 1 || interests().Interests[0].Opinion != "dislike", "description/interest restart persistence")
	keys("first", "Escape")
	h.screen("first", "Description:")
	keys("first", "q")
	h.screen("first", "[Cancel]")
	keys("first", "Enter")
	mode("second", 0)
	h.screen("second", "techno (dislike · sent)")
	keys("second", "D")
	h.screen("second", "[Cancel]")
	keys("second", "Right", "Enter")
	h.wait("interest removal written", func() bool { return count(118) == 1 })
	h.screen("second", "No interests saved.")
	failIf(t, len(interests().Interests) != 0, "confirmed removal retained interest")
	// The other frontend's retained edit never entered daemon state or diagnostics.
	failIf(t, strings.Contains(self().Description, "unsaved"), "unsaved draft persisted")
	logs, err := os.ReadFile(filepath.Join(h.root, "daemon.log"))
	must(t, err)
	for _, private := range []string{"q/?猫", "other frontend", "hello 世界", " unsaved"} {
		failIf(t, bytes.Contains(logs, []byte(private)), "profile content leaked into diagnostics")
	}
}
