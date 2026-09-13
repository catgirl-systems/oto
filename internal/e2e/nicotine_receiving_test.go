//go:build communitye2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func verifyNicotineReceiving(t *testing.T, h *terminal, ctx context.Context, state string) {
	t.Helper()
	summary, err := h.client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := summary.CommunityIdentity
	send := func(sequence int, files []string) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"sequence": sequence, "files": files})
		path := filepath.Join(state, "send-request.json")
		if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			t.Fatal(err)
		}
	}
	rejected := func(sequence int) {
		t.Helper()
		h.wait("Nicotine offer rejected", func() bool {
			data, err := os.ReadFile(filepath.Join(state, fmt.Sprintf("send-%d.json", sequence)))
			if err != nil {
				return false
			}
			var outcomes map[string]string
			if json.Unmarshal(data, &outcomes) != nil || len(outcomes) == 0 {
				return false
			}
			for _, status := range outcomes {
				if status != "Cancelled" {
					return false
				}
			}
			return true
		})
	}
	downloads := func() []daemon.Download {
		t.Helper()
		snapshot, err := h.client.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot.Downloads
	}
	send(1, []string{"blocked.txt"})
	rejected(1)
	if len(downloads()) != 0 {
		t.Fatal("default-off offer admitted")
	}
	before, err := h.client.ReceivingSettings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(h.root, "received")
	settings := config.Receiving{Mode: "users", Users: []string{"reference"}, Directory: root}
	h.attach("receiving", 120, 40)
	h.screen("receiving", "No matching results")
	h.command("send-keys", "-t", "receiving", "Tab", "Tab", "Tab", "Tab", "Tab", "Tab", "Tab")
	h.screen("receiving", "Supporter privileges / gifting")
	h.command("send-keys", "-t", "receiving", "Right", "Right", "Right")
	h.screen("receiving", "Consented received files")
	h.command("send-keys", "-t", "receiving", "End", "Enter")
	h.screen("receiving", "Users (JSON): []")
	h.command("send-keys", "-t", "receiving", "Right", "Tab", "C-u")
	h.command("send-keys", "-t", "receiving", "-l", `["reference"]`)
	h.command("send-keys", "-t", "receiving", "Tab")
	h.command("send-keys", "-t", "receiving", "-l", root)
	h.command("send-keys", "-t", "receiving", "Enter")
	h.screen("receiving", "[Cancel]")
	h.command("send-keys", "-t", "receiving", "Enter")
	h.screen("receiving", "Directory:")
	current, err := h.client.ReceivingSettings(ctx, id)
	if err != nil || current.Settings.Mode != before.Settings.Mode {
		t.Fatal("terminal implicitly enabled receiving", err)
	}
	h.command("send-keys", "-t", "receiving", "Enter")
	h.screen("receiving", "[Cancel]")
	h.command("send-keys", "-t", "receiving", "Right", "Enter")
	h.wait("receiving editor saved", func() bool {
		var err error
		current, err = h.client.ReceivingSettings(ctx, id)
		return err == nil && current.Settings.Mode == "users" && current.Settings.Directory == root && len(current.Settings.Users) == 1 && !current.Settings.CompletionHooks
	})
	occupied := filepath.Join(root, "reference", "Reference", "Album", "one.txt")
	if err := os.MkdirAll(filepath.Dir(occupied), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(occupied, []byte("keep existing"), 0600); err != nil {
		t.Fatal(err)
	}
	send(2, []string{"one.txt", "世界.txt", "empty"})
	h.wait("durable received folder", func() bool {
		rows := downloads()
		if len(rows) != 3 {
			return false
		}
		for _, d := range rows {
			if d.State != "completed" {
				return false
			}
		}
		return true
	})
	for _, d := range downloads() {
		if !strings.HasPrefix(d.ID, "d-received-") || d.StatsAccount != id.Account || d.DownloadDir != root {
			t.Fatal("wrong received ownership", d)
		}
		name := filepath.Base(strings.ReplaceAll(d.Filename, "\\", "/"))
		want := "reference offer " + name
		if name == "empty" {
			want = ""
		}
		data, err := os.ReadFile(filepath.Join(d.DownloadDir, d.Destination))
		if err != nil || string(data) != want {
			t.Fatal("wrong received bytes", d, err)
		}
	}
	data, err := os.ReadFile(occupied)
	if err != nil || string(data) != "keep existing" {
		t.Fatal("received file overwrote collision", err)
	}
	send(3, []string{"one.txt", "世界.txt", "empty"})
	rejected(3)
	if len(downloads()) != 3 {
		t.Fatal("duplicate offer created new downloads")
	}
	settings.Mode = "off"
	if _, err := h.client.SetReceivingSettings(ctx, daemon.ReceivingSettingsRequest{CommunityIdentity: id, Expected: current.Settings, Settings: settings, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	send(4, []string{"revoked.txt"})
	rejected(4)
	if len(downloads()) != 3 {
		t.Fatal("revoked consent accepted offer")
	}
	setMode := func(mode string) {
		t.Helper()
		before, err := h.client.ReceivingSettings(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		next := before.Settings
		next.Mode = mode
		next.Users = nil
		if mode == "users" {
			next.Users = []string{"reference"}
		}
		if _, err := h.client.SetReceivingSettings(ctx, daemon.ReceivingSettingsRequest{CommunityIdentity: id, Expected: before.Settings, Settings: next, Confirm: true}); err != nil {
			t.Fatal(err)
		}
	}
	accepted := func(sequence int, name string, total int) {
		t.Helper()
		send(sequence, []string{name})
		h.wait("received buddy/trusted file", func() bool {
			rows := downloads()
			if len(rows) != total {
				return false
			}
			d := rows[len(rows)-1]
			if d.State != "completed" {
				return false
			}
			data, err := os.ReadFile(filepath.Join(d.DownloadDir, d.Destination))
			return err == nil && string(data) == "reference offer "+name
		})
	}
	setMode("buddies")
	accepted(5, "buddy.txt", 4)
	setMode("trusted")
	accepted(6, "trusted.txt", 5)
	buddies, err := h.client.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Username: "reference"})
	if err != nil || len(buddies.Buddies) != 1 {
		t.Fatal("missing reference buddy", err)
	}
	buddy := buddies.Buddies[0]
	if _, err := h.client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: buddy.Username, Revision: &buddy.Revision, Note: buddy.Note, NotifyOnline: buddy.NotifyOnline, Priority: buddy.Priority, Trusted: false, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	send(7, []string{"untrusted.txt"})
	rejected(7)
	if len(downloads()) != 5 {
		t.Fatal("trusted receiving accepted untrusted buddy")
	}
	setMode("users")
	cfg, err := config.Load(h.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Downloads.FiltersEnabled = true
	cfg.Downloads.FilterPatterns = []string{"*.blocked"}
	if _, err := h.client.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	send(8, []string{"filtered.blocked"})
	rejected(8)
	rows := downloads()
	if len(rows) != 6 || rows[5].State != "filtered" || rows[5].Offset != 0 {
		t.Fatal("received filter bypassed", rows)
	}
	if _, err := os.Stat(filepath.Join(rows[5].DownloadDir, rows[5].Destination)); !os.IsNotExist(err) {
		t.Fatal("filtered file written", err)
	}
	cfg, err = config.Load(h.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Bandwidth.Profiles {
		cfg.Bandwidth.Profiles[i].DownloadSpeedLimitKiB = 32
	}
	if _, err := h.client.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	send(9, []string{"resume.bin"})
	var receiveID string
	h.wait("partial received stream", func() bool {
		snapshot, err := h.client.Status(ctx)
		if err != nil {
			return false
		}
		for _, tr := range snapshot.Transfers {
			if strings.HasSuffix(tr.Filename, `\resume.bin`) && tr.Done > 0 && tr.Done < tr.Total {
				receiveID = tr.ID
				return true
			}
		}
		return false
	})
	h.stopDaemon()
	partPath := filepath.Join(h.root, "oto", "incomplete", receiveID+".part")
	partial, err := os.ReadFile(partPath)
	if err != nil || len(partial) == 0 || len(partial) >= len("resume-test\n")*65536 {
		t.Fatal("received partial not checkpointed", len(partial), err)
	}
	cfg, err = config.Load(h.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Bandwidth.Profiles {
		cfg.Bandwidth.Profiles[i].DownloadSpeedLimitKiB = 0
	}
	if err := cfg.Save(h.configPath); err != nil {
		t.Fatal(err)
	}
	h.startDaemon()
	h.wait("received transfer resumed after daemon restart", func() bool {
		for _, d := range downloads() {
			if d.ID == receiveID {
				return d.State == "completed" && d.StatsAccount == id.Account
			}
		}
		return false
	})
	for _, d := range downloads() {
		if d.ID == receiveID {
			data, err := os.ReadFile(filepath.Join(d.DownloadDir, d.Destination))
			if err != nil || string(data) != strings.Repeat("resume-test\n", 65536) {
				t.Fatal("resumed received bytes changed", err)
			}
		}
	}
	if len(downloads()) != 7 {
		t.Fatal("restart duplicated receiving admission")
	}
}
