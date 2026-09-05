package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/stats"
	"github.com/charmbracelet/x/ansi"
)

type statsViewState struct {
	page, rangeIndex   int
	request            uint64
	loading            bool
	overview           daemon.StatsOverview
	downloads, uploads []stats.Daily
	peers              stats.PeerPage
	log                stats.LogPage
	filter             stats.Filter
	edit               string
	value              string
	cursor             int
	detail             *stats.Event
	detailCursor       int
	sortBytes          bool
	prune              bool
	pruneConfirm       bool
	pruneChoice        int
	pruneRequest       ipc.PruneRequest
	preview            stats.PruneResult
	err                string
}
type statsMsg struct {
	request uint64
	view    statsViewState
}
type statsPruneMsg struct {
	result stats.PruneResult
	apply  bool
	err    error
}

var statsPages = []string{"Overview", "History", "Peers", "Log"}
var statsRanges = []int{7, 30, 90, 365, 0}

func (m *model) loadStats() tea.Cmd {
	if m.workspace != workspaceStats || m.stats.loading || m.stats.prune {
		return nil
	}
	m.stats.loading = true
	m.stats.request++
	view := m.stats
	f := view.filter
	if view.sortBytes {
		f.Sort = "bytes"
	} else {
		f.Sort = "peer"
	}
	if view.page != 3 {
		f.Kinds = nil
	}
	if f.Account == "" {
		f.Account = m.history.StatsAccount
	}
	f.Limit = 100
	f.Bins = max(1, min(120, m.width-14))
	if f.From.IsZero() && f.To.IsZero() && view.page == 1 && statsRanges[view.rangeIndex] > 0 {
		f.To = time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
		f.From = f.To.AddDate(0, 0, -statsRanges[view.rangeIndex])
	}
	return func() tea.Msg {
		summary := f
		summary.Kinds = nil
		summary.From, summary.To = time.Time{}, time.Time{}
		var err error
		view.overview, err = m.client.Statistics(m.ctx, summary)
		if err == nil {
			switch view.page {
			case 1:
				f.Direction = "download"
				view.downloads, err = m.client.StatsSeries(m.ctx, f)
				if err == nil {
					f.Direction = "upload"
					view.uploads, err = m.client.StatsSeries(m.ctx, f)
				}
			case 2:
				view.peers, err = m.client.StatsPeers(m.ctx, f)
			case 3:
				view.log, err = m.client.TransferLog(m.ctx, f)
			}
		}
		view.err = errText(err)
		view.loading = false
		return statsMsg{view.request, view}
	}
}
func (m *model) refreshStats() tea.Cmd { m.stats.loading = false; m.cursor = 0; return m.loadStats() }
func (m *model) statsKey(k tea.KeyPressMsg) tea.Cmd {
	v := &m.stats
	s := k.String()
	if v.prune {
		return m.statsPruneKey(k)
	}
	if v.detail != nil {
		switch s {
		case "esc", "enter":
			v.detail = nil
			m.cursor = v.detailCursor
		case "down", "j":
			m.cursor++
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "pgdown":
			m.cursor += m.pageRows()
		case "pgup":
			m.cursor = max(0, m.cursor-m.pageRows())
		}
		return nil
	}
	if v.edit != "" {
		if s == "esc" {
			v.edit = ""
			return nil
		}
		if s == "enter" {
			switch v.edit {
			case "peer":
				v.filter.Peer = strings.TrimSpace(v.value)
			case "from", "to":
				at := time.Time{}
				var err error
				if v.value != "" {
					at, err = time.Parse(time.DateOnly, v.value)
				}
				if err != nil {
					v.err = err.Error()
					return nil
				}
				if v.edit == "from" {
					v.filter.From = at
				} else {
					v.filter.To = at
				}
			}
			v.edit = ""
			v.filter.Cursor = ""
			return m.refreshStats()
		}
		v.value, v.cursor, _ = editText(v.value, v.cursor, k)
		return nil
	}
	switch s {
	case "ctrl+pgup", "ctrl+pgdown":
		step := 1
		if s == "ctrl+pgup" {
			step = 3
		}
		v.page = (v.page + step) % 4
		if v.page != 3 {
			v.filter.Kinds = nil
		}
		if v.page < 2 {
			v.filter.Direction = ""
		}
		v.filter.Cursor = ""
		return m.refreshStats()
	case "up", "k":
		m.cursor = max(0, m.cursor-1)
	case "down", "j":
		m.cursor++
	case "pgup":
		m.cursor = max(0, m.cursor-m.pageRows())
	case "pgdown":
		m.cursor += m.pageRows()
	case "home":
		m.cursor = 0
	case "r":
		v.rangeIndex = (v.rangeIndex + 1) % len(statsRanges)
		v.filter.From, v.filter.To = time.Time{}, time.Time{}
		v.filter.Cursor = ""
		return m.refreshStats()
	case "a":
		accounts := v.overview.Accounts
		if len(accounts) > 0 {
			i := slices.Index(accounts, v.overview.Account)
			v.filter.Account = accounts[(i+1)%len(accounts)]
			m.history.StatsAccount = v.filter.Account
			if m.historyPath != "" {
				disk, err := loadHistory(m.historyPath)
				if err == nil {
					disk.StatsAccount = v.filter.Account
					err = config.SaveJSON(m.historyPath, disk)
				}
				if err != nil {
					v.err = err.Error()
				}
			}
		}
		v.filter.Cursor = ""
		return m.refreshStats()
	case "/":
		v.edit = "peer"
		v.value = v.filter.Peer
		v.cursor = len([]rune(v.value))
	case "[":
		v.edit = "from"
		v.value = ""
		v.cursor = 0
	case "]":
		v.edit = "to"
		v.value = ""
		v.cursor = 0
	case "d":
		if v.page < 2 {
			return nil
		}
		switch v.filter.Direction {
		case "":
			v.filter.Direction = "download"
		case "download":
			v.filter.Direction = "upload"
		default:
			v.filter.Direction = ""
		}
		v.filter.Cursor = ""
		return m.refreshStats()
	case "e":
		if v.page != 3 {
			return nil
		}
		kinds := []string{"", "completed", "failed", "cancelled", "interrupted", "filtered", "forced", "rejected"}
		current := ""
		if len(v.filter.Kinds) > 0 {
			current = v.filter.Kinds[0]
		}
		next := kinds[(slices.Index(kinds, current)+1)%len(kinds)]
		v.filter.Kinds = nil
		if next != "" {
			v.filter.Kinds = []string{next}
		}
		v.filter.Cursor = ""
		return m.refreshStats()
	case "n":
		next := ""
		if v.page == 2 {
			next = v.peers.NextCursor
		} else if v.page == 3 {
			next = v.log.NextCursor
		}
		if next == "" {
			return nil
		}
		v.filter.Cursor = next
		return m.refreshStats()
	case "p":
		v.filter.Cursor = ""
		return m.refreshStats()
	case "s":
		v.sortBytes = !v.sortBytes
		v.filter.Cursor = ""
		return m.refreshStats()
	case "P":
		m.openStatsPrune()
	case "esc":
		v.filter.Peer = ""
		v.filter.Cursor = ""
		return m.refreshStats()
	case "enter":
		if v.page == 3 && m.cursor < len(v.log.Entries) {
			e := v.log.Entries[m.cursor]
			v.detail = &e
			v.detailCursor = m.cursor
			m.cursor = 0
		}
		if v.page == 2 && m.cursor < len(v.peers.Peers) {
			v.filter.Peer = v.peers.Peers[m.cursor].Peer
			v.page = 1
			v.filter.Cursor = ""
			return m.refreshStats()
		}
	}
	return nil
}
func statsRatio(upload, download uint64) string {
	if download == 0 {
		return "—"
	}
	return fmt.Sprintf("%.3f", float64(upload)/float64(download))
}
func totalsSummary(label string, t stats.Totals) []string {
	average := "—"
	if t.ActiveMillis > 0 {
		rate := float64(t.Bytes) / (float64(t.ActiveMillis) / 1000)
		n := ^uint64(0)
		if rate < float64(n) {
			n = uint64(rate)
		}
		average = formatBytes(n) + "/s"
	}
	return []string{
		fmt.Sprintf("%s: %s payload · %d files / %s", label, formatBytes(t.Bytes), t.CompletedFiles, formatBytes(t.CompletedBytes)),
		fmt.Sprintf("  attempts %d: %d OK / %d failed / %d cancelled / %d interrupted", t.AttemptsStarted, t.AttemptsCompleted, t.AttemptsFailed, t.AttemptsCancelled, t.AttemptsInterrupted),
		fmt.Sprintf("  retries %d · resumes %d · filtered %d · forced %d · rejected %d", t.Retries, t.Resumes, t.Filtered, t.Forced, t.Rejected),
		fmt.Sprintf("  cumulative stream %.1fs · queue wait %.1fs · avg %s · peak %s/s", float64(t.ActiveMillis)/1000, float64(t.WaitMillis)/1000, average, formatBytes(t.Peak)),
		fmt.Sprintf("  unique peers %d · first %s · last %s", t.UniquePeers, statsDate(t.First), statsDate(t.Last)),
	}
}
func statsDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}

// Charts need no color and aggregate long series to the available columns.
func statsChart(label string, values []uint64, width int, ascii bool) []string {
	width = max(1, width)
	if len(values) == 0 {
		return []string{label + ": no data"}
	}
	bins := min(width, len(values))
	heights := make([]float64, bins)
	var peak uint64
	for _, n := range values {
		peak = max(peak, n)
	}
	for i := range heights {
		start, end := i*len(values)/bins, (i+1)*len(values)/bins
		for _, n := range values[start:end] {
			heights[i] += float64(n) / float64(end-start)
		}
	}
	glyphs := []rune("▁▂▃▄▅▆▇█")
	if ascii {
		glyphs = []rune(" .:-=+*#")
	}
	var line strings.Builder
	for _, n := range heights {
		i := 0
		if peak > 0 {
			i = min(len(glyphs)-1, int(n/float64(peak)*float64(len(glyphs)-1)))
		}
		line.WriteRune(glyphs[i])
	}
	return []string{fmt.Sprintf("%s · peak %s", label, formatBytes(peak)), line.String(), "0 " + strings.Repeat("-", max(0, bins-2))}
}
func (m model) renderStats(width, height int) string {
	v := m.stats
	if v.prune {
		return m.renderStatsPrune(width, height)
	}
	if v.detail != nil {
		e := v.detail
		lines := []string{"Transfer event · up/down scroll · esc close", e.At.Format(time.RFC3339Nano), fmt.Sprintf("Account: %q", e.Account), fmt.Sprintf("Peer: %q", e.Peer), e.Direction + " · " + e.Kind, fmt.Sprintf("File: %q", e.Filename), fmt.Sprintf("Destination: %q", e.Destination), "Payload: " + formatBytes(e.Bytes), "Completed: " + formatBytes(e.CompletedBytes), fmt.Sprintf("Error: %q", e.Error)}
		wrapped := strings.Split(ansi.Hardwrap(strings.Join(lines, "\n"), max(1, width), false), "\n")
		start := min(max(0, m.cursor), max(0, len(wrapped)-height))
		return strings.Join(wrapped[start:min(len(wrapped), start+max(1, height))], "\n")
	}
	tabs := slices.Clone(statsPages)
	tabs[v.page] = "[" + tabs[v.page] + "]"
	lines := []string{strings.Join(tabs, "  "), "Account: " + v.overview.Account + "  (a change)", "Peer: " + v.filter.Peer + " · " + v.filter.Direction + " · " + strings.Join(v.filter.Kinds, ",")}
	if v.err != "" {
		lines = append(lines, v.err)
	}
	if v.overview.Warning != "" {
		lines = append(lines, v.overview.Warning)
	}
	if v.edit != "" {
		lines = append(lines, "Edit "+v.edit+": "+v.value+"▏  (enter apply, esc cancel)")
	}
	switch v.page {
	case 0:
		lines = append(lines, fmt.Sprintf("Since %s · uptime %ds · online %ds · reconnects %d", statsDate(v.overview.Since), v.overview.UptimeSeconds, v.overview.OnlineSeconds, v.overview.Reconnects), fmt.Sprintf("Active %d / %s · queued %d / %s", v.overview.ActiveFiles, formatBytes(v.overview.ActiveBytes), v.overview.QueuedFiles, formatBytes(v.overview.QueuedBytes)))
		lines = append(lines, fmt.Sprintf("Session: down %s / up %s · ratio %s", formatBytes(v.overview.SessionTotals.Download.Bytes), formatBytes(v.overview.SessionTotals.Upload.Bytes), statsRatio(v.overview.SessionTotals.Upload.Bytes, v.overview.SessionTotals.Download.Bytes)), "Lifetime ratio: "+statsRatio(v.overview.Lifetime.Upload.Bytes, v.overview.Lifetime.Download.Bytes))
		down, up := []uint64{}, []uint64{}
		for _, sample := range v.overview.Samples {
			down = append(down, sample.Download)
			up = append(up, sample.Upload)
		}
		lines = append(lines, statsChart("Download B/s (-5m → now)", down, width-2, m.cfg.Statistics.ASCIICharts)...)
		lines = append(lines, statsChart("Upload B/s (-5m → now)", up, width-2, m.cfg.Statistics.ASCIICharts)...)
		lines = append(lines, totalsSummary("Session download", v.overview.SessionTotals.Download)...)
		lines = append(lines, totalsSummary("Session upload", v.overview.SessionTotals.Upload)...)
		lines = append(lines, totalsSummary("Lifetime download", v.overview.Lifetime.Download)...)
		lines = append(lines, totalsSummary("Lifetime upload", v.overview.Lifetime.Upload)...)
	case 1:
		period := "All"
		if n := statsRanges[v.rangeIndex]; n > 0 {
			period = fmt.Sprintf("%d days", n)
		}
		lines = append(lines, "UTC traffic · "+period+" (r change)")
		for _, series := range []struct {
			label string
			rows  []stats.Daily
		}{{"Downloads", v.downloads}, {"Uploads", v.uploads}} {
			values := []uint64{}
			var count, bytes uint64
			for _, d := range series.rows {
				values = append(values, d.Bytes)
				count += d.CompletedFiles
				bytes += d.Bytes
			}
			lines = append(lines, statsChart(series.label, values, width-2, m.cfg.Statistics.ASCIICharts)...)
			lines = append(lines, fmt.Sprintf("%s · %d completed files", formatBytes(bytes), count))
			if len(series.rows) > 0 {
				lines = append(lines, series.rows[0].Day+" → "+series.rows[len(series.rows)-1].Day)
			}
		}
		if v.filter.Peer != "" {
			lines = append(lines, totalsSummary("Peer download", v.overview.Lifetime.Download)...)
			lines = append(lines, totalsSummary("Peer upload", v.overview.Lifetime.Upload)...)
		}
	case 2:
		peers := v.peers.Peers
		start, end := visibleRange(len(peers), m.cursor, max(1, height-len(lines)-2))
		var peak uint64
		for _, p := range peers {
			peak = max(peak, p.Bytes)
		}
		for i := start; i < end; i++ {
			p := peers[i]
			bar := 0
			if peak > 0 {
				bar = int(float64(p.Bytes) / float64(peak) * float64(max(1, min(20, width/5))))
			}
			glyph := "█"
			if m.cfg.Statistics.ASCIICharts {
				glyph = "#"
			}
			lines = append(lines, selectedRow(fmt.Sprintf("%-20s %10s %5d files %s", trunc(p.Peer, 20), formatBytes(p.Bytes), p.CompletedFiles, strings.Repeat(glyph, bar)), i == m.cursor))
		}
	case 3:
		start, end := visibleRange(len(v.log.Entries), m.cursor, max(1, height-len(lines)-2))
		for i := start; i < end; i++ {
			e := v.log.Entries[i]
			lines = append(lines, selectedRow(fmt.Sprintf("%s %-11s %-12s %9s %s", e.At.UTC().Format("01-02 15:04"), e.Kind, e.Peer, formatBytes(e.Bytes), e.Filename), i == m.cursor))
		}
	}
	lines = append(lines, "ctrl+pgup/down pages · / peer · d direction · e outcome · [ ] dates · n next · p first · s sort · P prune")
	if v.page < 2 {
		lines = strings.Split(ansi.Hardwrap(strings.Join(lines, "\n"), max(1, width), false), "\n")
	} else {
		for i := range lines {
			lines[i] = trunc(lines[i], max(1, width))
		}
	}
	if len(lines) > height {
		start := 0
		if v.page < 2 {
			start = min(m.cursor, max(0, len(lines)-height))
		}
		lines = lines[start:min(len(lines), start+max(1, height))]
	}
	return strings.Join(lines, "\n")
}
func (m *model) openStatsPrune() {
	m.stats.prune = true
	m.stats.pruneConfirm = false
	m.stats.pruneChoice = 0
	m.stats.pruneRequest = ipc.PruneRequest{Cutoff: time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -30), Logs: true, Daily: true}
	m.stats.value = m.stats.pruneRequest.Cutoff.Format(time.DateOnly)
	m.stats.cursor = len(m.stats.value)
}
func (m model) pruneStats(apply bool) tea.Cmd {
	return func() tea.Msg {
		result, err := m.client.PruneStatistics(m.ctx, m.stats.pruneRequest, apply)
		return statsPruneMsg{result, apply, err}
	}
}
func (m *model) statsPruneKey(k tea.KeyPressMsg) tea.Cmd {
	v := &m.stats
	s := k.String()
	if s == "esc" || s == "n" {
		v.prune = false
		return nil
	}
	if v.pruneConfirm {
		switch s {
		case "left", "right", "up", "down":
			v.pruneChoice = 1 - v.pruneChoice
		case "enter":
			if v.pruneChoice == 1 {
				return m.pruneStats(true)
			}
			v.prune = false
		case "y":
			return m.pruneStats(true)
		}
		return nil
	}
	switch s {
	case "l":
		v.pruneRequest.Logs = !v.pruneRequest.Logs
	case "d":
		v.pruneRequest.Daily = !v.pruneRequest.Daily
	case "enter":
		at, err := time.Parse(time.DateOnly, v.value)
		if err != nil {
			v.err = err.Error()
			return nil
		}
		v.pruneRequest.Cutoff = at
		return m.pruneStats(false)
	default:
		v.value, v.cursor, _ = editText(v.value, v.cursor, k)
	}
	return nil
}
func (m model) renderStatsPrune(width, height int) string {
	v := m.stats
	lines := []string{"Prune statistics (all accounts)", "Before UTC date: " + v.value, fmt.Sprintf("l logs: %t · d daily rollups: %t", v.pruneRequest.Logs, v.pruneRequest.Daily), "Lifetime and peer totals are never pruned.", v.err, "enter preview · esc cancel"}
	if v.pruneConfirm {
		choice := "[No] Yes"
		if v.pruneChoice == 1 {
			choice = "No [Yes]"
		}
		lines = append(lines, fmt.Sprintf("Delete %d log rows and %d daily rows?", v.preview.Logs, v.preview.Daily), choice, "arrows choose · enter accept · esc cancel")
	}
	for i := range lines {
		lines[i] = trunc(lines[i], max(1, width))
	}
	return strings.Join(lines[:min(len(lines), max(1, height))], "\n")
}
