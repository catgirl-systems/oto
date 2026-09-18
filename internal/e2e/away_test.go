//go:build communitye2e

package e2e

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityAutoAwayUsesRealInputNotPolling(t *testing.T) {
	var login, pm []byte
	var replies atomic.Int32
	var awayPeriods uint32
	pm = testutil.SocialFixture(t, "pm-online").Payload(t)
	login = testutil.SocialFixture(t, "login-ok").Payload(t)
	var status atomic.Uint32
	status.Store(2)
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
			if code == 28 {
				if len(body) != 4 {
					return fmt.Errorf("invalid SetStatus length")
				}
				status.Store(binary.LittleEndian.Uint32(body))
				if status.Load() == 1 {
					awayPeriods++
					message := append([]byte(nil), pm...)
					binary.LittleEndian.PutUint32(message, 100+awayPeriods)
					for range 2 {
						if err := testutil.WritePacket(conn, 22, message); err != nil {
							return err
						}
					}
				}
			}
			if code == 22 {
				d := soulseek.NewDecoder(body)
				user, e1 := d.String()
				text, e2 := d.String()
				if e1 != nil || e2 != nil || user != "Alice" || text != "[Automatic Message] back later" {
					return fmt.Errorf("unexpected automatic reply")
				}
				replies.Add(1)
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	h.attach("away-a", 120, 40)
	h.attach("away-b", 80, 24)
	h.screen("away-a", "No matching results")
	h.screen("away-b", "No matching results")
	ctx := context.Background()
	summary, err := h.client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, err := h.client.CommunityAwaySettings(ctx, summary.CommunityIdentity)
	if err != nil {
		t.Fatal(err)
	}
	req := daemon.CommunityAwaySettingsRequest{CommunityIdentity: summary.CommunityIdentity, Expected: current.Settings, Settings: current.Settings}
	req.Settings.AutoAwaySeconds = 2
	req.Settings.AutoReply = "back later"
	h.command("send-keys", "-t", "away-a", "Tab", "Tab", "Tab", "Tab", "Tab", "Tab", "Tab")
	h.screen("away-a", "Supporter privileges / gifting")
	h.command("send-keys", "-t", "away-a", "Left", "Down", "Down", "Down", "Enter")
	h.screen("away-a", "Idle seconds (0 off)")
	h.command("send-keys", "-t", "away-a", "-l", "2")
	h.command("send-keys", "-t", "away-a", "Tab")
	h.command("send-keys", "-t", "away-a", "-l", "back later")
	h.command("send-keys", "-t", "away-a", "Enter")
	h.screen("away-a", "[Cancel]")
	h.command("send-keys", "-t", "away-a", "Right", "Enter")
	h.wait("away editor saved", func() bool {
		v, err := h.client.CommunityAwaySettings(ctx, summary.CommunityIdentity)
		screen := h.command("capture-pane", "-p", "-t", "away-a")
		return err == nil && v.Settings == req.Settings && !strings.Contains(screen, "unsaved") && !strings.Contains(screen, "Working")
	})
	h.command("send-keys", "-t", "away-a", "Escape")
	h.screen("away-a", "Automatic away / replies")
	h.wait("automatic away despite polling", func() bool {
		s, err := h.client.CommunitySummary(ctx)
		return err == nil && s.AutomaticAway && status.Load() == 1 && replies.Load() == 1
	})
	h.command("send-keys", "-t", "away-b", "Down")
	h.wait("second frontend restores online", func() bool {
		s, err := h.client.CommunitySummary(ctx)
		return err == nil && !s.AutomaticAway && status.Load() == 2
	})
	if err := h.client.SetPresence(ctx, daemon.PresenceAway); err != nil {
		t.Fatal(err)
	}
	before, err := h.client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.command("send-keys", "-t", "away-a", "Down")
	h.wait("manual away receives activity", func() bool {
		s, err := h.client.CommunitySummary(ctx)
		return err == nil && s.LastActivity.After(before.LastActivity)
	})
	time.Sleep(2200 * time.Millisecond) // Cross two daemon idle ticks; activity must never clear manual Away.
	if replies.Load() != 2 {
		t.Fatalf("expected one reply in each away period, got %d", replies.Load())
	}
	now, err := h.client.CommunitySummary(ctx)
	if err != nil || now.AutomaticAway || status.Load() != 1 {
		t.Fatal("manual away overridden", now, err)
	}
	if err := h.client.SetPresence(ctx, daemon.PresenceOnline); err != nil {
		t.Fatal(err)
	}
	req.Expected = req.Settings
	req.Settings.AutoAwaySeconds = 0
	if _, err := h.client.SetCommunityAwaySettings(ctx, req); err != nil {
		t.Fatal(err)
	}
	h.wait("explicit online", func() bool { return status.Load() == 2 })
}
