package daemon

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestReceivingRecoveryKeepsAccountAndSuppressesHooks(t *testing.T) {
	for _, mode := range []string{"allowed", "off", "other-account", "hooks", "banned", "unresolved-ban"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			cfg := testConfig(t)
			account := accountKey(cfg)
			cfg.Receiving = map[string]config.Receiving{account: {Mode: "users", Users: []string{"sender"}, CompletionHooks: mode == "hooks"}}
			marker := filepath.Join(t.TempDir(), "hook")
			cfg.Downloads.AfterFileCommand = "touch " + marker
			dbPath := filepath.Join(t.TempDir(), "state.sqlite3")
			s, err := New(cfg, dbPath)
			must(t, err)
			root := filepath.Join(cfg.DownloadDir, "received")
			now := time.Now().UTC()
			d := Download{ID: "d-received-1", StatsAccount: account, Username: "sender", Filename: `Music\song`, DownloadDir: root, Destination: "sender/Music/song", Size: 4, State: "queued", CreatedAt: now, UpdatedAt: now}
			s.mu.Lock()
			s.seq = 1
			s.journal.DownloadSequence = 1
			s.journal.Downloads = []Download{d}
			err = s.persistDownloadLocked(d)
			s.mu.Unlock()
			must(t, err)
			part := putPartial(t, d.ID, "data")
			must(t, s.Close())
			if mode == "off" {
				cfg.Receiving[account] = config.Receiving{}
			}
			if mode == "other-account" {
				cfg.Soulseek.Username = "different"
			}
			reopened, err := New(cfg, dbPath)
			must(t, err)
			defer reopened.Close()
			rows := reopened.Downloads()
			failIf(t, len(rows) != 1 || !receivedDownload(rows[0]) || rows[0].StatsAccount != account, "lost receiving admission", rows)
			if mode == "banned" {
				reopened.community.rules = []CommunityRule{{Action: "ban", Kind: "username", Value: "sender"}}
			}
			if mode == "unresolved-ban" {
				reopened.community.rules = []CommunityRule{{Action: "ban", Kind: "ip", prefix: netip.MustParsePrefix("192.0.2.0/24")}}
			}
			reopened.runDownload(context.Background(), rows[0], make(chan struct{}, 1))
			result := reopened.Downloads()[0]
			if mode == "off" || mode == "other-account" || mode == "banned" || mode == "unresolved-ban" {
				failIf(t, result.State != "failed" || result.StatsAccount != account, "unconsented recovery", result)
				data, err := os.ReadFile(part)
				failIf(t, err != nil || string(data) != "data", "partial modified without consent", err)
			} else {
				data, err := os.ReadFile(filepath.Join(root, result.Destination))
				failIf(t, result.State != "completed" || err != nil || string(data) != "data", "received recovery failed", result, err)
			}
			if mode == "hooks" {
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(marker); err == nil {
						break
					}
					time.Sleep(time.Millisecond)
				}
			}
			must(t, reopened.Close())
			_, err = os.Stat(marker)
			if mode == "hooks" {
				if err != nil {
					t.Fatal("explicit hook not run", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("received completion hook ran without consent", err)
			}
		})
	}
}
func TestReceivingPermissionAndPeerDirectory(t *testing.T) {
	s := downloadService(t)
	account := accountKey(s.cfg)
	s.cfg.Receiving = map[string]config.Receiving{account: {Mode: "users", Users: []string{"Alice", s.cfg.Soulseek.Username}}}
	address := netip.MustParseAddr("127.0.0.1")
	failIf(t, s.receivingPermissionLocked(account, "Alice", address) != nil || s.receivingPermissionLocked(account, "alice", address) == nil || s.receivingPermissionLocked(account, s.cfg.Soulseek.Username, address) == nil || s.receivingPermissionLocked("other", "Alice", address) == nil, "incorrect exact-user consent")
	s.community.rules = []CommunityRule{{Action: "ban", Kind: "ip", prefix: netip.MustParsePrefix("127.0.0.0/8")}}
	if s.receivingPermissionLocked(account, "Alice", address) == nil || s.receivingPermissionLocked(account, "Alice", netip.Addr{}) == nil {
		t.Fatal("ban or unresolved address bypass")
	}
	failIf(t, s.receivingPermissionLocked(account, "Alice", netip.MustParseAddr("192.0.2.1")) != nil, "unrelated address denied")
	seen := map[string]bool{}
	for _, name := range []string{"Alice", "alice", "a/b", "a_b", "a%2Fb", ".", "..", receivedUserDirectory(".")} {
		dir := receivedUserDirectory(name)
		failIf(t, dir == "" || dir == "." || dir == ".." || filepath.Base(dir) != dir || seen[dir], "unsafe/colliding peer directory", name, dir)
		seen[dir] = true
	}
}
