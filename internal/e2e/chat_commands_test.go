//go:build communitye2e

package e2e

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityComposerCommands(t *testing.T) {
	fixtures := testutil.SocialPayloadMap(t)
	sent := make(chan string, 10)
	server := testutil.ListenScript(t, func(_ context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		if err := testutil.WritePacket(conn, 1, fixtures["login-ok"]); err != nil {
			return err
		}
		if err := testutil.WritePacket(conn, 22, fixtures["pm-online"]); err != nil {
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
			if code == 22 {
				d := soulseek.NewDecoder(data)
				if _, err := d.String(); err != nil {
					return err
				}
				text, err := d.String()
				if err != nil {
					return err
				}
				sent <- text
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	h.attach("commands", 120, 40)
	h.screen("commands", "No matching results")
	h.command("send-keys", "-t", "commands", "Tab", "Tab", "Tab", "Tab")
	h.screen("commands", "Alice")
	h.command("send-keys", "-t", "commands", "Enter")
	h.screen("commands", "hello 世界")
	h.command("send-keys", "-t", "commands", "i")
	enter := func(text string) {
		t.Helper()
		h.command("send-keys", "-t", "commands", "-l", text)
		h.command("send-keys", "-t", "commands", "Enter")
	}
	expect := func(want string) {
		t.Helper()
		h.wait("outgoing "+want, func() bool {
			select {
			case got := <-sent:
				failIfFmt(t, got != want, "got %q want %q", got, want)
				return true
			default:
				return false
			}
		})
	}
	enter(`/alias wave "me $*"`)
	h.screen("commands", "Command result")
	h.screen("commands", "expansion")
	h.command("send-keys", "-t", "commands", "Escape")
	h.screen("commands", "hello 世界")
	h.command("send-keys", "-t", "commands", "-l", "/wa")
	h.command("send-keys", "-t", "commands", "Tab")
	h.screen("commands", "/wave")
	enter(" waves 世界")
	expect("/me waves 世界")
	h.screen("commands", "[action]")
	enter("/not-a-command")
	h.screen("commands", "unknown command")
	select {
	case got := <-sent:
		t.Fatal("unknown command transmitted", got)
	default:
	}
	h.command("send-keys", "-t", "commands", "C-U")
	enter("//literal")
	expect("/literal")
	ctx := context.Background()
	summary, err := h.client.CommunitySummary(ctx)
	must(t, err)
	settings, err := h.client.CommunityTextSettings(ctx, summary.CommunityIdentity)
	must(t, err)
	enter(`/text-tools ` + settings.Revision + ` '{"substitutions":[{"from":"replace-me","to":"replaced 世界"}]}'`)
	h.screen("commands", "Command result")
	h.screen("commands", "substitutions")
	h.command("send-keys", "-t", "commands", "Escape")
	h.screen("commands", "hello 世界")
	enter("replace-me")
	expect("replaced 世界")
	settings, err = h.client.CommunityTextSettings(ctx, summary.CommunityIdentity)
	must(t, err)
	settings.Settings.Substitutions[0].To = "second replacement"
	if _, err = h.client.SetCommunityTextSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	enter("replace-me")
	expect("second replacement")
	enter("/ctcp Alice")
	expect("\x01VERSION\x01")
	h.screen("commands", "[CTCP] VERSION request")
	settings.Settings.Substitutions[0].To = "stale"
	if _, err = h.client.SetCommunityTextSettings(ctx, settings); err == nil {
		t.Fatal("stale settings accepted")
	}
	h.command("send-keys", "-t", "commands", "-l", "/he")
	h.command("send-keys", "-t", "commands", "Tab")
	h.screen("commands", "/help")
	h.command("send-keys", "-t", "commands", "Enter")
	h.screen("commands", "Command result")
	h.screen("commands", "usage")
	h.command("send-keys", "-t", "commands", "Escape")
	h.screen("commands", "[CTCP] VERSION request")
	h.command("send-keys", "-t", "commands", "Escape")
	h.screen("commands", "i compose")
	h.command("send-keys", "-t", "commands", "Tab", "Tab", "Tab")
	h.screen("commands", "Supporter privileges / gifting")
	h.command("send-keys", "-t", "commands", "Left", "Down", "Enter")
	h.screen("commands", "Chat text tools")
	h.screen("commands", "No rules")
	h.command("send-keys", "-t", "commands", "n")
	h.command("send-keys", "-t", "commands", "-l", "signal 世界")
	h.command("send-keys", "-t", "commands", "Enter", "s")
	h.screen("commands", "[Cancel]")
	h.command("send-keys", "-t", "commands", "Enter")
	unchanged, err := h.client.CommunityTextSettings(ctx, summary.CommunityIdentity)
	failIf(t, err != nil || len(unchanged.Settings.Keywords) != 0, "default save changed settings", err)
	h.command("send-keys", "-t", "commands", "s", "Right", "Enter")
	h.wait("text editor saved", func() bool {
		got, err := h.client.CommunityTextSettings(ctx, summary.CommunityIdentity)
		return err == nil && len(got.Settings.Keywords) == 1 && got.Settings.Keywords[0] == "signal 世界"
	})
	h.wait("text editor settled", func() bool {
		screen := h.command("capture-pane", "-p", "-t", "commands")
		return strings.Contains(screen, "signal 世界") && !strings.Contains(screen, "unsaved") && !strings.Contains(screen, "Working")
	})
	h.command("send-keys", "-t", "commands", "Escape")
	h.screen("commands", "Commands / aliases help")
	h.command("send-keys", "-t", "commands", "Down", "Enter")
	h.screen("commands", "No shell evaluation or plugins")
}
