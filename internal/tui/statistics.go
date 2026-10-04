package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/diagnostics"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/stats"
	"github.com/charmbracelet/x/ansi"
)

type statsViewState struct {
	page, rangeIndex      int
	request               uint64
	loading               bool
	overview              daemon.StatsOverview
	downloads, uploads    []stats.Daily
	peers                 stats.PeerPage
	log                   stats.LogPage
	filter                stats.Filter
	edit                  string
	value                 string
	cursor                int
	detail                *stats.Event
	detailCursor          int
	logs                  []diagnostics.Record
	logsLevel, logsSearch string
	sortBytes             bool
	prune                 bool
	pruneConfirm          bool
	pruneDays             string
	prunePending          bool
	pruneGeneration       uint64
	pruneRequest          ipc.PruneRequest
	preview               stats.PruneResult
	err                   string
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

var statsPages = []string{"Overview", "History", "Peers", "Log", "Diagnostics"}
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
			case 4:
				view.logs, err = m.client.Logs(m.ctx, 1000)
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
			case "logs":
				v.logsSearch = strings.TrimSpace(v.value)
				v.edit = ""
				return nil
			}
			v.edit = ""
			v.filter.Cursor = ""
			return m.refreshStats()
		}
		v.value, v.cursor, _ = editText(v.value, v.cursor, k)
		return nil
	}
	switch s {
	case "[", "]", "ctrl+pgup", "ctrl+pgdown":
		step := 1
		if s == "[" || s == "ctrl+pgup" {
			step = len(statsPages) - 1
		}
		v.page = (v.page + step) % len(statsPages)
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
	case "home", "g":
		m.cursor = 0
	case "r":
		if v.page == 4 {
			return m.refreshStats()
		}
		v.rangeIndex = (v.rangeIndex + 1) % len(statsRanges)
		v.filter.From, v.filter.To = time.Time{}, time.Time{}
		v.filter.Cursor = ""
		return m.refreshStats()
	case "a":
		accounts := v.overview.Accounts
		if len(accounts) > 0 {
			i := slices.Index(accounts, v.overview.Account)
			v.filter.Account = accounts[(i+1)%len(accounts)]
			if err := m.saveStatsAccount(v.filter.Account); err != nil {
				v.err = err.Error()
			}
		}
		v.filter.Cursor = ""
		return m.refreshStats()
	case "/":
		if v.page == 4 {
			v.edit, v.value = "logs", v.logsSearch
			v.cursor = len([]rune(v.value))
			return nil
		}
		v.edit = "peer"
		v.value = v.filter.Peer
		v.cursor = len([]rune(v.value))
	case "f":
		if v.page == 4 {
			levels := []string{"", "WARN", "ERROR"}
			v.logsLevel = levels[(slices.Index(levels, v.logsLevel)+1)%len(levels)]
			return nil
		}
	case "<":
		v.edit = "from"
		v.value = ""
		v.cursor = 0
	case ">":
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
		if v.page == 4 {
			v.logsLevel, v.logsSearch = "", ""
			return nil
		}
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

var logLevelRank = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}

// filterLogRecords keeps records at or above level containing the search text.
func filterLogRecords(records []diagnostics.Record, level, search string) []diagnostics.Record {
	search = strings.ToLower(search)
	out := make([]diagnostics.Record, 0, len(records))
	for _, r := range records {
		if level != "" && logLevelRank[r.Level] < logLevelRank[level] {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(r.Msg+" "+r.Text), search) {
			continue
		}
		out = append(out, r)
	}
	return out
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
