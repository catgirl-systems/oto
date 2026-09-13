//go:build communitye2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityScopedSearchTerminal(t *testing.T) {
	fixtures := map[string]testutil.WireFixture{}
	for _, f := range testutil.SocialFixtures(t) {
		fixtures[f.Name] = f
	}
	var roomSearches, buddySearches, globalSearches atomic.Int32
	server := testutil.ListenScript(t, func(_ context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		if err := testutil.WritePacket(conn, 1, fixtures["login-ok"].Payload(t)); err != nil {
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
			case 14:
				if err := testutil.WritePacket(conn, 14, fixtures["room-joined"].Payload(t)); err != nil {
					return err
				}
			case 26:
				globalSearches.Add(1)
			case 42, 120:
				d := soulseek.NewDecoder(data)
				name, err := d.String()
				if err != nil {
					return err
				}
				if _, err = d.U32(); err != nil {
					return err
				}
				query, err := d.String()
				if err != nil {
					return err
				}
				if query != "song" {
					return fmt.Errorf("unexpected query %q", query)
				}
				if code == 120 {
					if name != "oto test" {
						return fmt.Errorf("wrong room %q", name)
					}
					roomSearches.Add(1)
				} else {
					if !strings.HasPrefix(name, "buddy ") {
						return fmt.Errorf("wrong buddy %q", name)
					}
					buddySearches.Add(1)
				}
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	h.attach("scopes", 120, 40)
	h.screen("scopes", "No matching results")
	ctx := context.Background()
	summary, err := h.client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 35 {
		if _, err := h.client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: summary.CommunityIdentity, Username: fmt.Sprintf("buddy %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	h.command("send-keys", "-t", "scopes", "Tab", "Tab", "Tab", "Tab", "C-NPage")
	h.screen("scopes", "N join/create")
	h.command("send-keys", "-t", "scopes", "N")
	h.screen("scopes", "Join/create public room")
	h.command("send-keys", "-t", "scopes", "-l", "oto test")
	h.command("send-keys", "-t", "scopes", "Tab", "Enter")
	h.screen("scopes", "oto test (2) J")
	h.command("send-keys", "-t", "scopes", "u")
	h.screen("scopes", "Search scope")
	h.command("send-keys", "-t", "scopes", "-l", "song")
	h.command("send-keys", "-t", "scopes", "Enter")
	h.wait("room search", func() bool { return roomSearches.Load() == 1 })
	h.screen("scopes", "No results; scoped searches")
	h.command("send-keys", "-t", "scopes", "Tab", "Tab", "Tab", "Tab", "C-NPage")
	h.screen("scopes", "Buddies")
	h.command("send-keys", "-t", "scopes", "u")
	h.screen("scopes", "Search scope")
	h.command("send-keys", "-t", "scopes", "-l", "song")
	h.command("send-keys", "-t", "scopes", "Enter")
	h.wait("complete buddy set", func() bool { return buddySearches.Load() == 35 })
	h.screen("scopes", "No results; scoped searches")
	if globalSearches.Load() != 0 {
		t.Fatal("scoped search fell back to global")
	}
}
