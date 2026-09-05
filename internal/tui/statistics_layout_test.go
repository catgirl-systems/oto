package tui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/stats"
	"github.com/charmbracelet/x/ansi"
)

func TestStatsOverviewLayout(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	at := time.Date(2026, 9, 5, 19, 34, 0, 0, time.UTC)
	m := model{workspace: workspaceStats, cfg: config.Default()}
	m.stats.overview = daemon.StatsOverview{
		Account: "server.slsknet.org:2242/alice", Since: at,
		UptimeSeconds: 3600, OnlineSeconds: 3540, ActiveFiles: 2, QueuedFiles: 5,
		SessionTotals: daemon.DirectionTotals{
			Download: stats.Totals{Bytes: 32 << 20, CompletedFiles: 3, AttemptsStarted: 4, ActiveMillis: 32000, First: at, Last: at},
			Upload:   stats.Totals{Bytes: 64 << 20, CompletedFiles: 6, AttemptsStarted: 7, ActiveMillis: 64000, First: at, Last: at},
		},
		Lifetime: daemon.DirectionTotals{
			Download: stats.Totals{Bytes: 2 << 30, CompletedFiles: 200, First: at, Last: at},
			Upload:   stats.Totals{Bytes: 4 << 30, CompletedFiles: 400, First: at, Last: at},
		},
	}
	for i, rate := range []uint64{0, 1 << 20, 2 << 20, 1 << 20, 3 << 20, 2 << 20} {
		m.stats.overview.Samples = append(m.stats.overview.Samples, daemon.RateSample{At: at.Add(time.Duration(i) * time.Second), Download: rate, Upload: rate / 2})
	}
	wide := m.renderStats(144, 40)
	t.Log("\n" + wide)
	for _, want := range []string{"32 MiB", "64 MiB", "2.0 GiB", "4.0 GiB", "Session", "Lifetime", "Last (UTC)", "2.0 MiB/s", "1.0 MiB/s", "19:34:00", "19:34:05 UTC"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("missing %q in overview", want)
		}
	}
	if strings.Contains(wide, "Peer:") || strings.Contains(wide, "\x1b") {
		t.Fatal("empty filter or color escapes in overview")
	}
	beside := false
	for _, line := range strings.Split(wide, "\n") {
		beside = beside || (strings.Contains(line, "Download") && strings.Contains(line, "Upload"))
	}
	if !beside {
		t.Fatal("wide overview did not put directions side by side")
	}
	for _, width := range []int{1, 12, 40, 80, 123, 124, 144} {
		for _, ascii := range []bool{false, true} {
			m.cfg.Statistics.ASCIICharts = ascii
			m.cursor = 0
			for _, height := range []int{8, 24, 40} {
				view := m.renderStats(width, height)
				lines := strings.Split(view, "\n")
				if len(lines) > height {
					t.Fatalf("height overflow at %dx%d", width, height)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("width overflow at %dx%d: %q", width, height, line)
					}
				}
			}
		}
	}
	m.cursor = 0
	narrow := m.renderStats(80, 200)
	last, upload := strings.Index(narrow, "Last (UTC)"), strings.Index(narrow, "Upload  ")
	if last < 0 || upload <= last {
		t.Fatal("narrow overview did not stack directions")
	}
	top := m.renderStats(80, 12)
	m.statsKey(key("pgdown"))
	if m.cursor == 0 || m.renderStats(80, 12) == top {
		t.Fatal("narrow overview cannot scroll")
	}
	m.cursor = 1000
	if !strings.Contains(m.renderStats(80, 12), "Last (UTC)") {
		t.Fatal("bottom of stacked overview is inaccessible")
	}
	m.cursor = 0
	m.stats.filter.Peer = "bob"
	peer := m.renderStats(80, 200)
	if !strings.Contains(peer, "Peer: bob") || !strings.Contains(peer, "Live activity is account-wide") || !strings.Contains(peer, "Last (UTC)") {
		t.Fatal("peer scope is missing or ambiguous")
	}
	// Color styling must preserve the same cell geometry.
	t.Setenv("NO_COLOR", "")
	for _, line := range strings.Split(m.renderStats(144, 40), "\n") {
		if ansi.StringWidth(line) > 144 {
			t.Fatalf("colored overview overflow: %q", line)
		}
	}
}

func TestStatsPlotAndContextHints(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, ascii := range []bool{false, true} {
		for _, values := range [][]uint64{nil, {0, 0}, {0, math.MaxUint64}} {
			lines := statsChart("Rate B/s", values, 40, ascii)
			if len(lines) != 6 {
				t.Fatal("unstable chart height")
			}
			for _, line := range lines[1:5] {
				if ansi.StringWidth(line) != 40 {
					t.Fatal("plot does not use available width")
				}
				if ascii && strings.ContainsAny(line, "▁▂▃▄▅▆▇█") {
					t.Fatal("Unicode plot in ASCII mode")
				}
			}
			if len(values) == 0 || values[len(values)-1] == 0 {
				if strings.TrimSpace(strings.Join(lines[1:5], "")) != "" {
					t.Fatal("empty or zero traffic rendered as a positive bar")
				}
			} else if strings.TrimSpace(lines[1]) == "" {
				t.Fatal("large counters did not reach the top of the plot")
			}
		}
	}
	m := model{workspace: workspaceStats}
	for page := range statsPages {
		m.stats.page = page
		hints := strings.Join(m.footerHints(), " · ")
		if !strings.Contains(hints, "ctrl+pgup/down") || !strings.Contains(hints, "P prune") {
			t.Fatal("lost common controls")
		}
		if strings.Contains(hints, "e outcome") != (page == 3) || strings.Contains(hints, "s sort") != (page == 2) {
			t.Fatalf("irrelevant controls on page %d: %s", page, hints)
		}
	}
}
