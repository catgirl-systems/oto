package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestReceivingFinalizationChecksPublishedPolicyAndWorkerCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			s := downloadService(t)
			account := accountKey(s.cfg)
			s.cfg.Receiving = map[string]config.Receiving{account: {Mode: "users", Users: []string{"sender"}}}
			now := time.Now().UTC()
			root := filepath.Join(t.TempDir(), "received")
			d := Download{ID: "d-received-1", StatsAccount: account, Username: "sender", Filename: `Music\song`, DownloadDir: root, Destination: "sender/Music/song", Size: 4, Offset: 4, State: "running", CreatedAt: now, UpdatedAt: now}
			s.journal.Downloads = []Download{d}
			s.transfers[d.ID] = Transfer{ID: d.ID, Direction: "download", State: "running"}
			if err := s.persistDownloadLocked(d); err != nil {
				t.Fatal(err)
			}
			part := putPartial(t, d.ID, "data")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			} else {
				s.cfg.Receiving[account] = config.Receiving{}
			}
			// No revalidation pass has run: the finalization boundary itself must refuse.
			s.completeDownloadContext(ctx, d.ID, root, part)
			if _, err := os.Stat(filepath.Join(root, d.Destination)); !os.IsNotExist(err) {
				t.Fatal("unconsented publication", err)
			}
			data, err := os.ReadFile(part)
			if err != nil || string(data) != "data" {
				t.Fatal("partial lost", err)
			}
			if state := s.Downloads()[0].State; state == "completed" || state == "finalizing" {
				t.Fatal("invalid finalization state", state)
			}
		})
	}
}
func TestReceivingCapacityIncludesRecoveredAndPausedAdmissions(t *testing.T) {
	s := downloadService(t)
	account := accountKey(s.cfg)
	for i := range 128 {
		s.journal.Downloads = append(s.journal.Downloads, Download{ID: fmt.Sprintf("d-received-%d", i), StatsAccount: account, Username: fmt.Sprintf("peer-%d", i), State: "queued"})
	}
	if s.receivedOffers != nil {
		t.Fatal("expected recovered state without live offers")
	}
	if s.receivingCapacityLocked(account, "new") {
		t.Fatal("recovered global admission limit ignored")
	}
	s.journal.Downloads[0].State = "completed"
	if !s.receivingCapacityLocked(account, "new") {
		t.Fatal("completed record consumed capacity")
	}
	for i := range 16 {
		s.journal.Downloads[i+1].Username = "sender"
		s.journal.Downloads[i+1].State = "paused"
	}
	if s.receivingCapacityLocked(account, "sender") {
		t.Fatal("paused peer admissions ignored")
	}
	if !s.receivingCapacityLocked("another-account", "sender") {
		t.Fatal("account capacity leaked")
	}
}
