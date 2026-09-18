package daemon

import (
	"net/netip"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestReceivingRevocationCancelsEvenWhenPauseCannotPersist(t *testing.T) {
	s := downloadService(t)
	account := accountKey(s.cfg)
	s.cfg.Receiving = map[string]config.Receiving{account: {Mode: "users", Users: []string{"sender"}}}
	now := time.Now().UTC()
	d := Download{ID: "d-received-1", StatsAccount: account, Username: "sender", Filename: `Music\song`, Size: 3, State: "running", Destination: "sender/Music/song", CreatedAt: now, UpdatedAt: now}
	s.journal.Downloads = []Download{d}
	s.transfers[d.ID] = Transfer{ID: d.ID, Username: d.Username, Direction: "download", State: "running"}
	must(t, s.persistDownloadLocked(d))
	cancelled := false
	s.downloadCancels[d.ID] = func() { cancelled = true }
	s.receivedAddresses = map[string]netip.Addr{d.ID: netip.MustParseAddr("127.0.0.1")}
	s.revalidateReceivedDownloads()
	failIf(t, cancelled, "valid consent cancelled")
	storageTrigger(t, s, "reject_pause", "CREATE TRIGGER reject_pause BEFORE UPDATE ON downloads WHEN NEW.state = 'paused' BEGIN SELECT RAISE(ABORT, 'injected failure'); END")
	s.cfg.Receiving[account] = config.Receiving{}
	s.revalidateReceivedDownloads()
	failIf(t, !cancelled, "persistence failure left unconsented stream running")
	delete(s.downloadCancels, d.ID)
}
