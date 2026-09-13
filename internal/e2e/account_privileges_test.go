//go:build communitye2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/catgirl-systems/oto/internal/daemon"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityPrivilegeGiftTerminalConfirmation(t *testing.T) {
	fixtures := map[string]testutil.WireFixture{}
	for _, f := range testutil.SocialFixtures(t) {
		fixtures[f.Name] = f
	}
	var gifts atomic.Int32
	server := testutil.ListenScript(t, func(_ context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		if err := testutil.WritePacket(conn, 1, fixtures["login-ok"].Payload(t)); err != nil {
			return err
		}
		for {
			code, payload, err := testutil.ReadPacket(conn)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			switch code {
			case 92:
				f := fixtures["privilege-balance"]
				if err := testutil.WritePacket(conn, 92, f.Payload(t)); err != nil {
					return err
				}
			case 123:
				if !bytes.Equal(payload, fixtures["give-privileges-request"].Payload(t)) {
					return fmt.Errorf("unexpected gift payload")
				}
				gifts.Add(1) // No acknowledgement exists; even this scripted server must not invent one.
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	h.attach("gift", 120, 40)
	h.screen("gift", "No matching results")
	h.command("send-keys", "-t", "gift", "Tab", "Tab", "Tab", "Tab", "Tab", "Tab", "Tab")
	h.screen("gift", "Supporter privileges / gifting")
	h.command("send-keys", "-t", "gift", "Down", "Down", "Enter")
	h.screen("gift", "3 whole days")
	h.command("send-keys", "-t", "gift", "g")
	h.command("set-buffer", "--", "Alice")
	h.command("paste-buffer", "-p", "-t", "gift")
	h.screen("gift", "Alice")
	h.command("send-keys", "-t", "gift", "Tab", "Enter")
	h.screen("gift", "Confirm privilege gift")
	if gifts.Load() != 0 {
		t.Fatal("preview sent gift")
	}
	h.command("send-keys", "-t", "gift", "Enter")
	h.screen("gift", "Gift only after explicit confirmation")
	if gifts.Load() != 0 {
		t.Fatal("default cancel sent gift")
	}
	h.command("send-keys", "-t", "gift", "Enter")
	h.screen("gift", "Confirm privilege gift")
	h.command("send-keys", "-t", "gift", "Right", "Enter")
	h.screen("gift", "Outcome: unknown")
	h.wait("one explicitly confirmed gift", func() bool { return gifts.Load() == 1 })
	h.command("send-keys", "-t", "gift", "r")
	h.screen("gift", "Outcome: unknown")
	h.command("send-keys", "-t", "gift", "Enter", "Enter")
	if gifts.Load() != 1 {
		t.Fatal("uncertain gift retried")
	}
	command := func(args ...string) daemon.CommandResult {
		t.Helper()
		cmd := exec.Command(binaryPath, append([]string{"command"}, args...)...)
		cmd.Env = h.env
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal("command failed", err, string(data))
		}
		var out daemon.CommandResult
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err, string(data))
		}
		return out
	}
	if out := command("help"); len(out.Help) < 2 {
		t.Fatal("missing shared command help")
	}
	if out := command("privileges"); out.Privileges == nil || !out.Privileges.Fresh {
		t.Fatal("headless balance", out)
	}
	preview := command("gift", "Alice", "1").Gift
	if preview == nil || preview.State != "preview" || gifts.Load() != 1 {
		t.Fatal("headless preview", preview)
	}
	args := []string{"--confirm", "--request-id", preview.RequestID, "--revision", strconv.FormatUint(preview.Balance.Revision, 10), "--account", preview.Account, "--daemon", preview.Daemon, "--session", strconv.FormatUint(preview.Session, 10), "gift", "Alice", "1"}
	if out := command(args...); out.Gift == nil || out.Gift.State != "unknown" {
		t.Fatal("headless confirmation", out)
	}
	if out := command(args...); out.Gift == nil || !out.Gift.Duplicate {
		t.Fatal("headless reconciliation", out)
	}
	if gifts.Load() != 2 {
		t.Fatal("headless gift duplicated", gifts.Load())
	}
	alias := command("alias", "credits", "privileges").Alias
	if alias == nil || alias.Revision == "" {
		t.Fatal("alias was not created")
	}
	aliases := command("aliases").Aliases
	if aliases == nil || len(aliases.Aliases) != 1 || aliases.Aliases[0].Name != "credits" {
		t.Fatal("alias listing", aliases)
	}
	page, err := h.client.CommunityAliases(context.Background(), daemon.CommunityAliasesRequest{CommunityIdentity: aliases.CommunityIdentity})
	if err != nil || len(page.Aliases) != 1 {
		t.Fatal("alias resource", page, err)
	}
	if _, err := h.client.SetCommunityAlias(context.Background(), daemon.CommunityAliasRequest{CommunityIdentity: aliases.CommunityIdentity, Name: "credits", Expansion: "help", Revision: "stale"}); err == nil {
		t.Fatal("stale alias edit accepted")
	}
	console := exec.Command(binaryPath, "console")
	console.Env = h.env
	console.Stdin = strings.NewReader("help\ncredits\ngift Alice 1\nquit\n")
	data, err := console.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(data))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for _, kind := range []string{"help", "balance", "preview"} {
		var result daemon.CommandResult
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(kind, err, string(data))
		}
		if kind == "help" && len(result.Help) < 2 || kind == "balance" && (result.Privileges == nil || !result.Privileges.Fresh) || kind == "preview" && (result.Gift == nil || result.Gift.State != "preview") {
			t.Fatal(kind, result)
		}
	}
	if gifts.Load() != 2 {
		t.Fatal("console implicitly confirmed a gift")
	}
	command("--confirm", "--request-id", "remove-credits", "--account", aliases.Account, "--daemon", aliases.Daemon, "--session", strconv.FormatUint(aliases.Session, 10), "unalias", "credits", alias.Revision)
	if page := command("aliases").Aliases; page == nil || len(page.Aliases) != 0 {
		t.Fatal("alias removal failed", page)
	}
}
