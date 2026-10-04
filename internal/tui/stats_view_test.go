package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/diagnostics"
	"github.com/catgirl-systems/oto/internal/stats"
)

func statsPreviewModel() model {
	at := time.Date(2026, 9, 5, 19, 34, 0, 0, time.UTC)
	m := model{workspace: workspaceStats, cfg: config.Default(), width: 120, height: 40}
	m.status = snapshot{status: daemon.StatusConnected, presence: daemon.PresenceOnline, user: "molly"}
	m.stats.overview = daemon.StatsOverview{
		Account: "server.slsknet.org:2242/alice", Since: at,
		UptimeSeconds: 3600 * 5, OnlineSeconds: 3540 * 5, Reconnects: 2, ActiveFiles: 2, ActiveBytes: 80 << 20, QueuedFiles: 5, QueuedBytes: 300 << 20,
		SessionTotals: daemon.DirectionTotals{
			Download: stats.Totals{Bytes: 32 << 20, CompletedFiles: 3, CompletedBytes: 30 << 20, AttemptsStarted: 4, AttemptsCompleted: 3, AttemptsFailed: 1, ActiveMillis: 32000, Peak: 4 << 20, UniquePeers: 2, First: at, Last: at},
			Upload:   stats.Totals{Bytes: 64 << 20, CompletedFiles: 6, AttemptsStarted: 7, ActiveMillis: 64000, First: at, Last: at},
		},
		Lifetime: daemon.DirectionTotals{
			Download: stats.Totals{Bytes: 2 << 30, CompletedFiles: 200, AttemptsStarted: 230, AttemptsCompleted: 200, AttemptsFailed: 20, AttemptsCancelled: 10, First: at, Last: at},
			Upload:   stats.Totals{Bytes: 4 << 30, CompletedFiles: 400, First: at, Last: at},
		},
		Logging: &diagnostics.Status{Level: "INFO", Directory: "/home/molly/.local/state/oto/logs", StoredBytes: 3 << 20, FileCount: 2, RotationBytes: 10 << 20, MaxArchives: 3},
	}
	for i := 0; i < 90; i++ {
		r := uint64((i*37)%11) << 19
		m.stats.overview.Samples = append(m.stats.overview.Samples, daemon.RateSample{At: at.Add(time.Duration(i) * time.Second), Download: r, Upload: r / 3})
	}
	for d := 0; d < 30; d++ {
		day := at.AddDate(0, 0, d-30).Format(time.DateOnly)
		m.stats.downloads = append(m.stats.downloads, stats.Daily{Day: day, Totals: stats.Totals{Bytes: uint64((d*13)%7) << 26, CompletedFiles: uint64(d % 5)}})
		m.stats.uploads = append(m.stats.uploads, stats.Daily{Day: day, Totals: stats.Totals{Bytes: uint64((d*5)%9) << 25, CompletedFiles: uint64(d % 3)}})
	}
	for i, p := range []string{"alice", "bob_the_sharer", "carol", "dmitri", "eve"} {
		m.stats.peers.Peers = append(m.stats.peers.Peers, stats.PeerStats{Peer: p, Totals: stats.Totals{Bytes: uint64(5-i) << 28, CompletedFiles: uint64(50 - i*9)}})
	}
	for i, k := range []string{"completed", "failed", "cancelled", "completed", "filtered"} {
		m.stats.log.Entries = append(m.stats.log.Entries, stats.Event{At: at.Add(time.Duration(i) * time.Minute), Kind: k, Direction: "download", Peer: "alice", Bytes: uint64(i+1) << 22, Filename: `music\Artist\Album\0` + fmt.Sprint(i) + ` track.flac`})
	}
	for i, l := range []string{"INFO", "WARN", "ERROR", "DEBUG"} {
		m.stats.logs = append(m.stats.logs, diagnostics.Record{Time: at.Add(time.Duration(i) * time.Second), Level: l, Msg: "peer connection", Text: "user=alice addr=1.2.3.4:2234"})
	}
	return m
}

func TestStatsPagesFitAndStayReadable(t *testing.T) {
	for _, color := range []string{"", "1"} {
		t.Setenv("NO_COLOR", color)
		for page := range statsPages {
			for _, width := range []int{1, 12, 40, 60, 80, 120, 150} {
				for _, height := range []int{6, 16, 40} {
					m := statsPreviewModel()
					m.stats.page = page
					lines := strings.Split(m.renderStats(width, height), "\n")
					failIfFmt(t, len(lines) > height, "page %d %dx%d: %d lines", page, width, height, len(lines))
					for _, line := range lines {
						failIfFmt(t, lipgloss.Width(line) > width, "page %d %dx%d: %q", page, width, height, line)
					}
				}
			}
		}
	}
	t.Setenv("NO_COLOR", "1")
	m := statsPreviewModel()
	wide := m.renderStats(146, 200)
	for _, want := range []string{"NOW", "LIVE RATE", "TOTALS", "SESSION RATIO", "VOLUME", "ATTEMPTS", "TIMING", "ACTIVITY", "Last (UTC)"} {
		failIfFmt(t, !strings.Contains(wide, want), "wide overview missing %q:\n%s", want, wide)
	}
	m.stats.page = 3
	log := m.renderStats(120, 20)
	failIf(t, !strings.Contains(log, "OUTCOME") || !strings.Contains(log, "› 09-05 19:34"), "log page lost its header or cursor:\n"+log)
	m.stats.page = 4
	failIf(t, !strings.Contains(m.renderStats(120, 20), "4 of 4 records"), "diagnostics count wording")
}
