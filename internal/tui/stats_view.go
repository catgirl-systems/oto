package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/stats"
	"github.com/charmbracelet/x/ansi"
)

// The Stats workspace reads top to bottom as labelled sections: headline
// tiles, live charts, then compact tables whose columns are sized to their
// values instead of stretched across the panel.

// statTile is one headline figure: a small label above a bold value.
type statTile struct {
	label, value string
	tone         color.Color
}

// statTiles lays tiles out in rows that wrap to width, balancing the count
// per row so no tile is left alone; each tile is as wide as its longest line
// plus a gutter.
func statTiles(tiles []statTile, width int) []string {
	tileWidth := func(t statTile) int { return max(ansi.StringWidth(t.label), ansi.StringWidth(t.value)) + 4 }
	rows, used := 1, 0
	for _, t := range tiles {
		if used > 0 && used+tileWidth(t) > width {
			rows, used = rows+1, 0
		}
		used += tileWidth(t)
	}
	perRow := (len(tiles) + rows - 1) / rows
	var lines []string
	for start := 0; start < len(tiles); start += perRow {
		var labels, values strings.Builder
		for _, t := range tiles[start:min(len(tiles), start+perRow)] {
			w := tileWidth(t)
			labels.WriteString(muted(searchTextColumn(t.label, w)))
			values.WriteString(styled(searchTextColumn(t.value, w), fg(t.tone).Bold(true)))
		}
		if start > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, labels.String(), values.String())
	}
	return lines
}

// statsPlot draws values as rows of block glyphs exactly width wide, averaging
// long series into the available columns.
func statsPlot(values []uint64, width, rows int, ascii bool) []string {
	width, rows = max(1, width), max(1, rows)
	var peak uint64
	for _, n := range values {
		peak = max(peak, n)
	}
	glyphs := []rune(" ▁▂▃▄▅▆▇█")
	if ascii {
		glyphs = []rune(" .:-=+*#@")
	}
	heights := make([]float64, width)
	if peak > 0 && len(values) > 0 {
		for i := range heights {
			start := i * len(values) / width
			end := min(len(values), max(start+1, (i+1)*len(values)/width))
			for _, n := range values[start:end] {
				heights[i] += float64(n) / float64(end-start) / float64(peak) * float64(rows)
			}
		}
	}
	lines := make([]string, 0, rows)
	for row := rows - 1; row >= 0; row-- {
		var line strings.Builder
		for _, h := range heights {
			level := max(0, min(8, int((h-float64(row))*8)))
			line.WriteRune(glyphs[level])
		}
		lines = append(lines, line.String())
	}
	return lines
}

// statsChart is a four-row plot with a title and a baseline. Charts need no
// colour to read.
func statsChart(label string, values []uint64, width int, ascii bool) []string {
	return statsChartTone(label, values, width, 4, ascii, theme.accent)
}

func statsChartTone(label string, values []uint64, width, rows int, ascii bool, tone color.Color) []string {
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
	lines := []string{muted(ansi.Truncate(title, width, "…"))}
	for _, row := range statsPlot(values, width, rows, ascii) {
		lines = append(lines, styled(row, fg(tone)))
	}
	axis := "─"
	if ascii {
		axis = "-"
	}
	return append(lines, faint("0"+strings.Repeat(axis, width-1)))
}

// statsRow is one metric of a totals table; group starts a labelled block.
type statsRow struct {
	group, label string
	values       []string
	alert        bool // tint non-zero values as a problem
}

func statsRows(totals ...stats.Totals) []statsRow {
	metric := func(group, label string, alert bool, value func(stats.Totals) string) statsRow {
		row := statsRow{group: group, label: label, alert: alert}
		for _, t := range totals {
			row.values = append(row.values, value(t))
		}
		return row
	}
	count := func(f func(stats.Totals) uint64) func(stats.Totals) string {
		return func(t stats.Totals) string { return fmt.Sprint(f(t)) }
	}
	return []statsRow{
		metric("Volume", "Payload", false, func(t stats.Totals) string { return formatBytes(t.Bytes) }),
		metric("", "Completed files", false, count(func(t stats.Totals) uint64 { return t.CompletedFiles })),
		metric("", "Completed bytes", false, func(t stats.Totals) string { return formatBytes(t.CompletedBytes) }),
		metric("Attempts", "Started", false, count(func(t stats.Totals) uint64 { return t.AttemptsStarted })),
		metric("", "Successful", false, count(func(t stats.Totals) uint64 { return t.AttemptsCompleted })),
		metric("", "Failed", true, count(func(t stats.Totals) uint64 { return t.AttemptsFailed })),
		metric("", "Cancelled", false, count(func(t stats.Totals) uint64 { return t.AttemptsCancelled })),
		metric("", "Interrupted", true, count(func(t stats.Totals) uint64 { return t.AttemptsInterrupted })),
		metric("", "Retries / resumes", false, func(t stats.Totals) string { return fmt.Sprintf("%d / %d", t.Retries, t.Resumes) }),
		metric("", "Filtered / forced", false, func(t stats.Totals) string { return fmt.Sprintf("%d / %d", t.Filtered, t.Forced) }),
		metric("", "Rejected", true, count(func(t stats.Totals) uint64 { return t.Rejected })),
		metric("Timing", "Cumul. stream time", false, func(t stats.Totals) string { return formatDuration(t.ActiveMillis / 1000) }),
		metric("", "Queue wait", false, func(t stats.Totals) string { return formatDuration(t.WaitMillis / 1000) }),
		metric("", "Average rate", false, statsAverage),
		metric("", "Peak rate", false, func(t stats.Totals) string { return formatBytes(t.Peak) + "/s" }),
		metric("Activity", "Unique peers", false, count(func(t stats.Totals) uint64 { return t.UniquePeers })),
		metric("", "First (UTC)", false, func(t stats.Totals) string { return statsShortDate(t.First) }),
		metric("", "Last (UTC)", false, func(t stats.Totals) string { return statsShortDate(t.Last) }),
	}
}

// statsColumn is a value column heading, optionally under a group heading
// that spans several columns.
type statsColumn struct {
	group, title string
	tone         color.Color
}

// statsTable renders rows under right-aligned columns sized to their content.
// It returns nil when the table cannot fit width.
func statsTable(columns []statsColumn, rows []statsRow, width int) []string {
	const labelWidth = 21
	widths := make([]int, len(columns))
	for i, c := range columns {
		widths[i] = max(8, ansi.StringWidth(c.title))
		for _, r := range rows {
			widths[i] = max(widths[i], ansi.StringWidth(r.values[i]))
		}
	}
	total := labelWidth
	for _, w := range widths {
		total += w + 3
	}
	if total > width {
		return nil
	}
	var lines []string
	if columns[0].group != "" {
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", labelWidth))
		for i := 0; i < len(columns); {
			j, span := i, 0
			for j < len(columns) && columns[j].group == columns[i].group {
				span += widths[j] + 3
				j++
			}
			b.WriteString(styled(searchColumn(columns[i].group, span), fg(columns[i].tone).Bold(true)))
			i = j
		}
		lines = append(lines, b.String())
	}
	var head strings.Builder
	head.WriteString(strings.Repeat(" ", labelWidth))
	for i, c := range columns {
		title := subtle(searchColumn(c.title, widths[i]+3))
		if columns[0].group == "" {
			title = styled(searchColumn(c.title, widths[i]+3), fg(c.tone).Bold(true))
		}
		head.WriteString(title)
	}
	lines = append(lines, head.String(), faint(strings.Repeat("─", total)))
	for _, r := range rows {
		if r.group != "" && len(lines) > 3 {
			lines = append(lines, "")
		}
		if r.group != "" {
			lines = append(lines, faint(strings.ToUpper(r.group)))
		}
		var b strings.Builder
		b.WriteString(subtle(searchTextColumn("  "+r.label, labelWidth)))
		for i, v := range r.values {
			cell := searchColumn(v, widths[i]+3)
			switch {
			case v == "—" || v == "0" || v == "0 / 0" || v == "0:00" || v == "0 B" || v == "0 B/s":
				cell = faint(cell)
			case r.alert:
				cell = danger(cell)
			default:
				cell = styled(cell, fg(theme.text))
			}
			b.WriteString(cell)
		}
		lines = append(lines, b.String())
	}
	return lines
}

// statsList is the fallback for widths too narrow for any table: one metric
// per block with each column on its own line.
func statsList(columns []statsColumn, rows []statsRow) []string {
	var lines []string
	for _, r := range rows {
		lines = append(lines, subtle(r.label))
		for i, c := range columns {
			name := strings.TrimSpace(c.group + " " + c.title)
			lines = append(lines, "  "+muted(name+": ")+r.values[i])
		}
	}
	return lines
}

// statsComparison renders two totals side by side under left and right.
func statsComparison(a, b stats.Totals, left, right string, width int) []string {
	columns := []statsColumn{{title: left, tone: theme.download}, {title: right, tone: theme.upload}}
	rows := statsRows(a, b)
	if table := statsTable(columns, rows, width); table != nil {
		return table
	}
	return statsList(columns, rows)
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

func statsDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

// statsShortDate is statsDate for table cells: month, day and time within the
// current year, otherwise the date alone.
func statsShortDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	if t.UTC().Year() == time.Now().UTC().Year() {
		return t.UTC().Format("Jan 02 15:04")
	}
	return t.UTC().Format(time.DateOnly)
}

// liveChart is one direction's live-rate chart with a title and time axis.
func (m model) liveChart(name, arrow string, values []uint64, tone color.Color, width int) []string {
	o := m.stats.overview
	var peak uint64
	for _, n := range values {
		peak = max(peak, n)
	}
	right := muted("no samples")
	if len(values) > 0 {
		right = muted("peak ") + subtle(formatBytes(peak)+"/s")
	}
	lines := []string{spread(styled(arrow+" "+name, fg(tone).Bold(true)), right, width)}
	for _, row := range statsPlot(values, width, 4, m.cfg.Statistics.ASCIICharts) {
		lines = append(lines, styled(row, fg(tone)))
	}
	if len(o.Samples) > 0 {
		lines = append(lines, faint(spread(o.Samples[0].At.UTC().Format("15:04:05"), o.Samples[len(o.Samples)-1].At.UTC().Format("15:04:05 UTC"), width)))
	} else {
		lines = append(lines, faint("Waiting for rate samples"))
	}
	return lines
}

// sideBySide joins two blocks of lines with a gap, padding the left block to
// leftWidth.
func sideBySide(left, right []string, leftWidth, gap int) []string {
	out := make([]string, max(len(left), len(right)))
	for i := range out {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = l + strings.Repeat(" ", max(0, leftWidth-lipgloss.Width(l))+gap) + r
	}
	return out
}

func (m model) statsOverview(width int) []string {
	o := m.stats.overview
	last := func(direction string) string {
		if len(o.Samples) == 0 {
			return "—"
		}
		s := o.Samples[len(o.Samples)-1]
		if direction == "upload" {
			return formatBytes(s.Upload) + "/s"
		}
		return formatBytes(s.Download) + "/s"
	}
	lines := []string{sectionRule("Now", "", width), ""}
	lines = append(lines, statTiles([]statTile{
		{"↓ DOWNLOAD", last("download"), theme.download},
		{"↑ UPLOAD", last("upload"), theme.upload},
		{"ACTIVE", fmt.Sprintf("%d · %s", o.ActiveFiles, formatBytes(o.ActiveBytes)), theme.text},
		{"QUEUED", fmt.Sprintf("%d · %s", o.QueuedFiles, formatBytes(o.QueuedBytes)), theme.text},
		{"SESSION RATIO", statsRatio(o.SessionTotals.Upload.Bytes, o.SessionTotals.Download.Bytes), theme.accent},
		{"LIFETIME RATIO", statsRatio(o.Lifetime.Upload.Bytes, o.Lifetime.Download.Bytes), theme.accent},
		{"UPTIME", formatDuration(o.UptimeSeconds), theme.text},
		{"ONLINE", formatDuration(o.OnlineSeconds), theme.success},
		{"RECONNECTS", fmt.Sprint(o.Reconnects), theme.text},
	}, width)...)
	lines = append(lines, "")

	downloads := make([]uint64, 0, len(o.Samples))
	uploads := make([]uint64, 0, len(o.Samples))
	for _, s := range o.Samples {
		downloads, uploads = append(downloads, s.Download), append(uploads, s.Upload)
	}
	live := func(width int) []string {
		lines := []string{sectionRule("Live rate", muted("up to 5 min"), width), ""}
		if width >= 70 {
			half := (width - 3) / 2
			return append(lines, sideBySide(m.liveChart("Download", "↓", downloads, theme.download, half), m.liveChart("Upload", "↑", uploads, theme.upload, half), half, 3)...)
		}
		lines = append(lines, m.liveChart("Download", "↓", downloads, theme.download, width)...)
		lines = append(lines, "")
		return append(lines, m.liveChart("Upload", "↑", uploads, theme.upload, width)...)
	}
	totals := func(table []string, width int) []string {
		lines := []string{sectionRule("Totals", "", width)}
		if m.stats.filter.Peer != "" {
			lines = append(lines, muted("  Live activity is account-wide; totals are for the selected peer."))
		}
		return append(append(lines, ""), table...)
	}
	wide := []statsColumn{{"↓ Download", "Session", theme.download}, {"↓ Download", "Lifetime", theme.download}, {"↑ Upload", "Session", theme.upload}, {"↑ Upload", "Lifetime", theme.upload}}
	table := statsTable(wide, statsRows(o.SessionTotals.Download, o.Lifetime.Download, o.SessionTotals.Upload, o.Lifetime.Upload), width)
	switch {
	case table != nil:
		lines = append(lines, live(width)...)
		lines = append(lines, "")
		lines = append(lines, totals(table, width)...)
	default:
		lines = append(lines, live(width)...)
		lines = append(lines, "")
		lines = append(lines, totals(nil, width)...)
		for i, direction := range []struct {
			name, arrow       string
			tone              color.Color
			session, lifetime stats.Totals
		}{{"Download totals", "↓", theme.download, o.SessionTotals.Download, o.Lifetime.Download}, {"Upload totals", "↑", theme.upload, o.SessionTotals.Upload, o.Lifetime.Upload}} {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, styled(direction.arrow+" "+direction.name, fg(direction.tone).Bold(true)))
			columns := []statsColumn{{title: "Session", tone: theme.subtext}, {title: "Lifetime", tone: theme.subtext}}
			rows := statsRows(direction.session, direction.lifetime)
			if table := statsTable(columns, rows, width); table != nil {
				lines = append(lines, table...)
			} else {
				lines = append(lines, statsList(columns, rows)...)
			}
		}
	}

	if logs := o.Logging; logs != nil {
		lines = append(lines, "", sectionRule("Diagnostic logs", muted("daemon-wide"), width), "")
		lines = append(lines, statsKV("Level", logs.Level), statsKV("Stored", fmt.Sprintf("%s stored · %d files · %d dropped", formatBytes(logs.StoredBytes), logs.FileCount, logs.DroppedRecords)), statsKV("Rotation", fmt.Sprintf("Rotate at %s · retain %d closed segments", formatBytes(logs.RotationBytes), logs.MaxArchives)), statsKV("Directory", logs.Directory))
		if logs.Warning != "" {
			lines = append(lines, warning("  Logging warning: "+logs.Warning))
		}
	}
	return lines
}

// statsKV renders an indented label / value pair.
func statsKV(label, value string) string {
	return "  " + muted(searchTextColumn(label, 11)) + subtle(value)
}

// statsFilterChips summarises the active filters.
func (m model) statsFilterChips() string {
	v := m.stats
	var chips []string
	chip := func(label, value string) { chips = append(chips, muted(label+" ")+accent(value)) }
	if v.filter.Peer != "" {
		chip("Peer:", v.filter.Peer)
	}
	if v.page >= 2 && v.filter.Direction != "" {
		chip("direction", v.filter.Direction)
	}
	if v.page == 3 && len(v.filter.Kinds) > 0 {
		chip("outcome", strings.Join(v.filter.Kinds, ","))
	}
	if v.page != 0 {
		if !v.filter.From.IsZero() {
			chip("from", v.filter.From.Format(time.DateOnly))
		}
		if !v.filter.To.IsZero() {
			chip("before", v.filter.To.Format(time.DateOnly))
		}
	}
	return strings.Join(chips, faint("  ·  "))
}

func (m model) statsHistory(width, height int) []string {
	v := m.stats
	var chips []string
	for i, n := range statsRanges {
		label := "all"
		if n > 0 {
			label = fmt.Sprintf("%dd", n)
		}
		if i == v.rangeIndex {
			chips = append(chips, pill(label, theme.surface, theme.accent))
		} else {
			chips = append(chips, " "+muted(label)+" ")
		}
	}
	lines := []string{sectionRule("Daily traffic", muted("UTC  r ")+strings.Join(chips, ""), width), ""}
	rows := max(3, min(8, (height-12)/2))
	for i, series := range []struct {
		label, arrow string
		tone         color.Color
		rows         []stats.Daily
	}{{"Downloads", "↓", theme.download, v.downloads}, {"Uploads", "↑", theme.upload, v.uploads}} {
		if i > 0 {
			lines = append(lines, "")
		}
		values := []uint64{}
		var count, bytes, peak uint64
		for _, d := range series.rows {
			values = append(values, d.Bytes)
			count += d.CompletedFiles
			bytes += d.Bytes
			peak = max(peak, d.Bytes)
		}
		summary := subtle(formatBytes(bytes)) + muted(fmt.Sprintf(" · %d completed files · peak %s/day", count, formatBytes(peak)))
		if len(series.rows) == 0 {
			summary = muted("no data yet")
		}
		lines = append(lines, spread(styled(series.arrow+" "+series.label, fg(series.tone).Bold(true)), summary, width))
		for _, row := range statsPlot(values, width, rows, m.cfg.Statistics.ASCIICharts) {
			lines = append(lines, styled(row, fg(series.tone)))
		}
		if len(series.rows) > 0 {
			lines = append(lines, faint(spread(series.rows[0].Day, series.rows[len(series.rows)-1].Day, width)))
		} else {
			lines = append(lines, faint(strings.Repeat("─", width)))
		}
	}
	if v.filter.Peer != "" {
		lines = append(lines, "", sectionRule("Peer lifetime totals", accent(v.filter.Peer), width), "")
		lines = append(lines, statsComparison(v.overview.Lifetime.Download, v.overview.Lifetime.Upload, "Download", "Upload", width)...)
	}
	return lines
}

func statsKindTone(kind string) color.Color {
	switch kind {
	case "completed":
		return theme.success
	case "failed", "rejected":
		return theme.danger
	case "cancelled", "interrupted":
		return theme.warning
	case "forced":
		return theme.accent
	}
	return theme.muted
}

func logLevelTone(level string) color.Color {
	switch level {
	case "ERROR":
		return theme.danger
	case "WARN":
		return theme.warning
	case "INFO":
		return theme.subtext
	}
	return theme.faint
}

func (m model) statsPeers(width, height, used int) []string {
	v := m.stats
	sort, direction := "peer name", "all"
	if v.sortBytes {
		sort = "bytes"
	}
	if v.filter.Direction != "" {
		direction = v.filter.Direction
	}
	more := ""
	if v.peers.NextCursor != "" {
		more = muted("  n more")
	}
	lines := []string{sectionRule("Peers", muted("s sort ")+accent(sort)+muted("  d direction ")+accent(direction)+more, width)}
	peers := v.peers.Peers
	if len(peers) == 0 {
		return append(lines, "", "  "+muted("No peer statistics for this filter yet."))
	}
	nameWidth := min(24, max(8, width/4))
	barWidth := min(40, max(0, width-2-nameWidth-2-10-2-10-2))
	lines = append(lines, columnHeader(searchTextColumn("PEER", nameWidth)+"  "+searchColumn("BYTES", 10)+"  "+searchColumn("FILES", 10)+"  SHARE", width))
	var peak uint64
	for _, p := range peers {
		peak = max(peak, p.Bytes)
	}
	glyph, rest := "━", "─"
	if m.cfg.Statistics.ASCIICharts {
		glyph, rest = "#", "-"
	}
	start, end := visibleRange(len(peers), m.cursor, max(1, height-used-len(lines)))
	for i := start; i < end; i++ {
		p := peers[i]
		filled := 0
		if peak > 0 {
			filled = int(float64(p.Bytes) / float64(peak) * float64(barWidth))
		}
		spans := []span{boldSpan(searchTextColumn(p.Peer, nameWidth), theme.text), gap(2), tinted(searchColumn(formatBytes(p.Bytes), 10), theme.subtext), gap(2), tinted(searchColumn(fmt.Sprintf("%d files", p.CompletedFiles), 10), theme.muted), gap(2), tinted(strings.Repeat(glyph, filled), theme.accent), tinted(strings.Repeat(rest, max(0, barWidth-filled)), theme.faint)}
		lines = append(lines, listRow(spans, width, i == m.cursor, false))
	}
	return lines
}

func (m model) statsLog(width, height, used int) []string {
	v := m.stats
	more := ""
	if v.log.NextCursor != "" {
		more = muted("  n more")
	}
	outcome := "all"
	if len(v.filter.Kinds) > 0 {
		outcome = v.filter.Kinds[0]
	}
	lines := []string{sectionRule("Transfer log", muted("e outcome ")+accent(outcome)+more, width)}
	if len(v.log.Entries) == 0 {
		return append(lines, "", "  "+muted("No transfer events for this filter yet."))
	}
	peerWidth := min(16, max(6, width/6))
	fileWidth := max(4, width-2-11-2-11-2-1-2-peerWidth-2-9-2)
	lines = append(lines, columnHeader(searchTextColumn("TIME (UTC)", 11)+"  "+searchTextColumn("OUTCOME", 11)+"  ↕  "+searchTextColumn("PEER", peerWidth)+"  "+searchColumn("SIZE", 9)+"  FILE", width))
	start, end := visibleRange(len(v.log.Entries), m.cursor, max(1, height-used-len(lines)))
	for i := start; i < end; i++ {
		e := v.log.Entries[i]
		arrow, tone := "↓", theme.download
		if e.Direction == "upload" {
			arrow, tone = "↑", theme.upload
		}
		spans := []span{tinted(e.At.UTC().Format("01-02 15:04"), theme.muted), gap(2), tinted(searchTextColumn(e.Kind, 11), statsKindTone(e.Kind)), gap(2), tinted(arrow, tone), gap(2), tinted(searchTextColumn(e.Peer, peerWidth), theme.accent), gap(2), tinted(searchColumn(formatBytes(e.Bytes), 9), theme.subtext), gap(2), plain(searchTextColumn(e.Filename, fileWidth))}
		lines = append(lines, listRow(spans, width, i == m.cursor, false))
	}
	return lines
}

func (m model) statsDiagnostics(width, height, used int) []string {
	v := m.stats
	records := filterLogRecords(v.logs, v.logsLevel, v.logsSearch)
	level := "all"
	if v.logsLevel != "" {
		level = "≥ " + v.logsLevel
	}
	detail := muted("f level ") + accent(level)
	if v.logsSearch != "" {
		detail += muted("  find ") + accent(v.logsSearch)
	}
	lines := []string{sectionRule("Daemon log", detail, width), "  " + muted(fmt.Sprintf("%d of %s", len(records), countLabel(len(v.logs), "record")))}
	if len(records) == 0 {
		return append(lines, "", "  "+muted("No log records match."))
	}
	start, end := visibleRange(len(records), m.cursor, max(1, height-used-len(lines)))
	for i := start; i < end; i++ {
		r := records[i]
		spans := []span{tinted(r.Time.Local().Format("01-02 15:04:05"), theme.faint), gap(2), boldSpan(searchTextColumn(r.Level, 5), logLevelTone(r.Level)), gap(2), tinted(r.Msg, theme.text)}
		if text := strings.TrimSpace(r.Text); text != "" {
			spans = append(spans, gap(2), tinted(text, theme.muted))
		}
		lines = append(lines, listRow(spans, width, i == m.cursor, false))
	}
	return lines
}

func (m model) statsEventDetail(width, height int) string {
	e := m.stats.detail
	kv := func(label, value string) string { return muted(searchTextColumn(label, 13)) + value }
	lines := []string{
		sectionRule("Transfer event", muted("↑/↓ scroll · esc close"), width), "",
		kv("When", e.At.Format(time.RFC3339Nano)),
		kv("Outcome", styled(e.Direction+" · "+e.Kind, fg(statsKindTone(e.Kind)).Bold(true))),
		kv("Account", fmt.Sprintf("%q", e.Account)),
		kv("Peer", fmt.Sprintf("%q", e.Peer)),
		kv("File", fmt.Sprintf("%q", e.Filename)),
		kv("Destination", fmt.Sprintf("%q", e.Destination)),
		kv("Payload", formatBytes(e.Bytes)),
		kv("Completed", formatBytes(e.CompletedBytes)),
		kv("Error", fmt.Sprintf("%q", e.Error)),
	}
	wrapped := strings.Split(ansi.Hardwrap(strings.Join(lines, "\n"), max(1, width), false), "\n")
	start := min(max(0, m.cursor), max(0, len(wrapped)-height))
	return strings.Join(wrapped[start:min(len(wrapped), start+max(1, height))], "\n")
}

func (m model) renderStats(width, height int) string {
	v := m.stats
	if v.prune {
		return m.renderStatsPrune(width, height)
	}
	if v.detail != nil {
		return m.statsEventDetail(width, height)
	}
	var lines []string
	if chips := m.statsFilterChips(); chips != "" {
		lines = append(lines, muted("Filters  ")+chips+faint("   esc clear"), "")
	}
	if v.err != "" {
		lines = append(lines, danger("✗ "+v.err))
	}
	if v.overview.Warning != "" {
		lines = append(lines, warning("! "+v.overview.Warning))
	}
	if v.edit != "" {
		label := map[string]string{"peer": "peer", "from": "from date (YYYY-MM-DD)", "to": "before date (YYYY-MM-DD)", "logs": "search"}[v.edit]
		lines = append(lines, promptLine("/", "", "", true, v.value, v.cursor, label+" · enter apply · esc cancel"), "")
	}
	switch v.page {
	case 0:
		lines = append(lines, m.statsOverview(width)...)
	case 1:
		lines = append(lines, m.statsHistory(width, height)...)
	case 2:
		lines = append(lines, m.statsPeers(width, height, len(lines))...)
	case 3:
		lines = append(lines, m.statsLog(width, height, len(lines))...)
	case 4:
		lines = append(lines, m.statsDiagnostics(width, height, len(lines))...)
	}
	if v.page < 2 {
		lines = strings.Split(ansi.Hardwrap(strings.Join(lines, "\n"), max(1, width), false), "\n")
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(1, width), "…")
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
