package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	pruneDays          string
	prunePending       bool
	pruneGeneration    uint64
	pruneRequest       ipc.PruneRequest
	preview            stats.PruneResult
	err                string
}
type statsMsg struct {
	request uint64
	view    statsViewState
}
type statsPruneMsg struct {
	request uint64
	result  stats.PruneResult
	apply   bool
	err     error
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
func statsAverage(t stats.Totals) string {
	if t.ActiveMillis == 0 {
		return "—"
	}
	rate := float64(t.Bytes) / (float64(t.ActiveMillis) / 1000)
	n := ^uint64(0)
	if rate < float64(n) {
		n = uint64(rate)
	}
	return formatBytes(n) + "/s"
}

func statsComparison(a, b stats.Totals, left, right string, width int) []string {
	rows := []struct {
		label string
		a, b  any
	}{
		{"Payload", formatBytes(a.Bytes), formatBytes(b.Bytes)},
		{"Completed files", a.CompletedFiles, b.CompletedFiles},
		{"Completed bytes", formatBytes(a.CompletedBytes), formatBytes(b.CompletedBytes)},
		{"Attempts", a.AttemptsStarted, b.AttemptsStarted},
		{"Successful", a.AttemptsCompleted, b.AttemptsCompleted},
		{"Failed", a.AttemptsFailed, b.AttemptsFailed},
		{"Cancelled", a.AttemptsCancelled, b.AttemptsCancelled},
		{"Interrupted", a.AttemptsInterrupted, b.AttemptsInterrupted},
		{"Retries / resumes", fmt.Sprintf("%d / %d", a.Retries, a.Resumes), fmt.Sprintf("%d / %d", b.Retries, b.Resumes)},
		{"Filtered / forced", fmt.Sprintf("%d / %d", a.Filtered, a.Forced), fmt.Sprintf("%d / %d", b.Filtered, b.Forced)},
		{"Rejected", a.Rejected, b.Rejected},
		{"Cumul. stream time", formatDuration(a.ActiveMillis / 1000), formatDuration(b.ActiveMillis / 1000)},
		{"Queue wait", formatDuration(a.WaitMillis / 1000), formatDuration(b.WaitMillis / 1000)},
		{"Average rate", statsAverage(a), statsAverage(b)},
		{"Peak rate", formatBytes(a.Peak) + "/s", formatBytes(b.Peak) + "/s"},
		{"Unique peers", a.UniquePeers, b.UniquePeers},
		{"First (UTC)", statsDate(a.First), statsDate(b.First)},
		{"Last (UTC)", statsDate(a.Last), statsDate(b.Last)},
	}
	column := max(1, (width-21)/2)
	lines := []string{strong(fmt.Sprintf("%-19s %*s %*s", "", column, left, column, right)), muted(strings.Repeat("─", max(1, width)))}
	for _, row := range rows {
		av, bv := fmt.Sprint(row.a), fmt.Sprint(row.b)
		if width < 43 || ansi.StringWidth(av) > column || ansi.StringWidth(bv) > column {
			lines = append(lines, muted(row.label), "  "+left+": "+av, "  "+right+": "+bv)
		} else {
			lines = append(lines, muted(fmt.Sprintf("%-19s", row.label))+fmt.Sprintf(" %*s %*s", column, av, column, bv))
		}
	}
	return lines
}
func statsDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

// Charts need no color and aggregate long series to the available columns.
func statsChart(label string, values []uint64, width int, ascii bool) []string {
	width = max(1, width)
	var peak uint64
	for _, n := range values {
		peak = max(peak, n)
	}
	title := fmt.Sprintf("%s · peak %s", label, formatBytes(peak))
	if len(values) == 0 {
		title = label + ": no data yet"
	} else if peak == 0 {
		title = label + ": no activity"
	}
	lines := []string{muted(title)}
	glyphs := []rune(" ▁▂▃▄▅▆▇█")
	if ascii {
		glyphs = []rune(" .:-=+*#@")
	}
	heights := make([]float64, width)
	if peak > 0 {
		for i := range heights {
			start := i * len(values) / width
			end := min(len(values), max(start+1, (i+1)*len(values)/width))
			for _, n := range values[start:end] {
				heights[i] += float64(n) / float64(end-start) / float64(peak) * 4
			}
		}
	}
	for row := 3; row >= 0; row-- {
		var line strings.Builder
		for _, h := range heights {
			level := max(0, min(8, int((h-float64(row))*8)))
			line.WriteRune(glyphs[level])
		}
		lines = append(lines, accent(line.String()))
	}
	axis := "─"
	if ascii {
		axis = "-"
	}
	return append(lines, muted("0"+strings.Repeat(axis, width-1)))
}

func (m model) statsOverview(width int) []string {
	o := m.stats.overview
	lines := []string{
		muted(fmt.Sprintf("Tracking since %s · uptime %s · online %s · reconnects %d", statsDate(o.Since), formatDuration(o.UptimeSeconds), formatDuration(o.OnlineSeconds), o.Reconnects)),
		fmt.Sprintf("%s %d / %s     %s %d / %s", strong("Active"), o.ActiveFiles, formatBytes(o.ActiveBytes), strong("Queued"), o.QueuedFiles, formatBytes(o.QueuedBytes)),
		muted(fmt.Sprintf("Upload/download ratio   session %s · lifetime %s", statsRatio(o.SessionTotals.Upload.Bytes, o.SessionTotals.Download.Bytes), statsRatio(o.Lifetime.Upload.Bytes, o.Lifetime.Download.Bytes))),
		"",
	}
	if m.stats.filter.Peer != "" {
		lines = append(lines, muted("Live activity is account-wide; totals are for the selected peer."), "")
	}
	panelWidth := width
	if width >= 124 {
		panelWidth = (width - 4) / 2
	}
	var panels []string
	for _, direction := range []struct {
		name              string
		session, lifetime stats.Totals
	}{{"Download", o.SessionTotals.Download, o.Lifetime.Download}, {"Upload", o.SessionTotals.Upload, o.Lifetime.Upload}} {
		values := make([]uint64, 0, len(o.Samples))
		for _, sample := range o.Samples {
			n := sample.Download
			if direction.name == "Upload" {
				n = sample.Upload
			}
			values = append(values, n)
		}
		rate := "—"
		if len(values) > 0 {
			rate = formatBytes(values[len(values)-1]) + "/s"
		}
		panel := []string{spread(accent(direction.name), strong(rate), panelWidth)}
		panel = append(panel, statsChart("Live B/s · up to 5m", values, panelWidth, m.cfg.Statistics.ASCIICharts)...)
		if len(o.Samples) > 0 {
			panel = append(panel, muted(spread(o.Samples[0].At.UTC().Format("15:04:05"), o.Samples[len(o.Samples)-1].At.UTC().Format("15:04:05 UTC"), panelWidth)))
		} else {
			panel = append(panel, muted("Waiting for rate samples"))
		}
		panel = append(panel, "")
		panel = append(panel, statsComparison(direction.session, direction.lifetime, "Session", "Lifetime", panelWidth)...)
		panels = append(panels, strings.Join(panel, "\n"))
	}
	body := strings.Join(panels, "\n\n")
	if width >= 124 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(panelWidth).Render(panels[0]), "    ", panels[1])
	}
	return append(lines, strings.Split(body, "\n")...)
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
	for i, tab := range tabs {
		if i == v.page {
			tabs[i] = accent("[" + tab + "]")
		} else {
			tabs[i] = muted(tab)
		}
	}
	lines := []string{strings.Join(tabs, "   "), muted("Account  ") + strong(v.overview.Account) + muted("  (a change)")}
	var filters []string
	if v.filter.Peer != "" {
		filters = append(filters, "Peer: "+v.filter.Peer)
	}
	if v.page >= 2 && v.filter.Direction != "" {
		filters = append(filters, v.filter.Direction)
	}
	if v.page == 3 && len(v.filter.Kinds) > 0 {
		filters = append(filters, strings.Join(v.filter.Kinds, ","))
	}
	if v.page != 0 {
		if !v.filter.From.IsZero() {
			filters = append(filters, "From: "+v.filter.From.Format(time.DateOnly))
		}
		if !v.filter.To.IsZero() {
			filters = append(filters, "Before: "+v.filter.To.Format(time.DateOnly))
		}
	}
	if len(filters) > 0 {
		lines = append(lines, accent(strings.Join(filters, " · ")))
	}
	lines = append(lines, "")
	if v.err != "" {
		lines = append(lines, danger(v.err))
	}
	if v.overview.Warning != "" {
		lines = append(lines, danger(v.overview.Warning))
	}
	if v.edit != "" {
		lines = append(lines, "Edit "+v.edit+": "+v.value+"▏  (enter apply, esc cancel)")
	}
	switch v.page {
	case 0:
		lines = append(lines, m.statsOverview(width)...)
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
			lines = append(lines, "", accent("Peer lifetime totals"))
			lines = append(lines, statsComparison(v.overview.Lifetime.Download, v.overview.Lifetime.Upload, "Download", "Upload", width)...)
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
	v := &m.stats
	v.request++ // Ignore an overview response that predates this dialog.
	v.loading = false
	v.prune, v.pruneConfirm, v.prunePending = true, false, false
	v.pruneGeneration++
	v.err = ""
	v.pruneRequest = ipc.PruneRequest{Logs: true, Daily: true}
	if v.pruneDays == "" {
		v.pruneDays = "30"
	}
	v.cursor = len([]rune(v.pruneDays))
}
func (m *model) pruneStats(apply bool) tea.Cmd {
	v := &m.stats
	v.prunePending = true
	v.pruneGeneration++
	v.err = ""
	request, generation := v.pruneRequest, v.pruneGeneration
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		result, err := client.PruneStatistics(ctx, request, apply)
		return statsPruneMsg{request: generation, result: result, apply: apply, err: err}
	}
}
func (m *model) statsPruneKey(k tea.KeyPressMsg) tea.Cmd {
	v := &m.stats
	s := k.String()
	if s == "esc" || s == "n" {
		if !v.prunePending || !v.pruneConfirm {
			v.prune = false
			v.pruneGeneration++
		}
		return nil
	}
	if v.prunePending {
		return nil
	}
	if v.pruneConfirm {
		if s == "enter" {
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
		today := time.Now().UTC().Truncate(24 * time.Hour)
		days, err := strconv.Atoi(v.pruneDays)
		if err != nil || days < 1 || days > int(today.Sub(time.Unix(0, 0)).Hours()/24) {
			v.err = "Enter a positive number of days (cutoff must be 1970 or later)."
			return nil
		}
		if !v.pruneRequest.Logs && !v.pruneRequest.Daily {
			v.err = "Select logs, daily rollups, or both."
			return nil
		}
		v.pruneRequest.Cutoff = today.AddDate(0, 0, -days)
		return m.pruneStats(false)
	default:
		v.pruneDays, v.cursor, _ = editText(v.pruneDays, v.cursor, k)
	}
	return nil
}
func pruneDayUnit(value string) string {
	days, err := strconv.Atoi(value)
	if err == nil && days == 1 {
		return "day"
	}
	return "days"
}
func (m model) renderStatsPrune(width, height int) string {
	v := m.stats
	lines := []string{accent("PRUNE · ALL ACCOUNTS")}
	hint := "Enter previews affected records · Esc cancels"
	if v.pruneConfirm {
		lines = append(lines,
			fmt.Sprintf("Delete log rows:   %d", v.preview.Logs),
			fmt.Sprintf("Delete daily rows: %d", v.preview.Daily),
			"Before "+v.pruneRequest.Cutoff.Format(time.DateOnly)+" UTC")
		hint = "Enter again to prune · Esc cancels"
	} else {
		lines = append(lines,
			"Older than: "+renderInput("", v.pruneDays, v.cursor, false, lipgloss.NewStyle())+" "+pruneDayUnit(v.pruneDays),
			fmt.Sprintf("l logs: %t · d daily rollups: %t", v.pruneRequest.Logs, v.pruneRequest.Daily))
	}
	if v.prunePending {
		hint = "Loading preview… · Esc cancels"
		if v.pruneConfirm {
			hint = "Pruning…"
		}
	}
	lines = append(lines, strong(hint))
	if v.err != "" {
		lines = append(lines, danger(v.err))
	}
	lines = append(lines, "", muted("Lifetime/peer totals and local files are never deleted."))
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(1, width), "…")
	}
	return strings.Join(lines[:min(len(lines), max(1, height))], "\n")
}
