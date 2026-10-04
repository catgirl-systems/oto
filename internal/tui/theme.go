package tui

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// palette holds the semantic colours every screen draws with. Screens never
// name a hex value directly, so the whole TUI follows the terminal between the
// dark and light variants.
type palette struct {
	accent    color.Color // brand, focus, active tabs
	onAccent  color.Color // text drawn on an accent background
	text      color.Color // primary text
	subtext   color.Color // secondary text and labels
	muted     color.Color // hints, placeholders, inactive items
	faint     color.Color // decorative glyphs that should recede
	border    color.Color // panel and separator lines
	surface   color.Color // cursor row and active tab background
	highlight color.Color // cursor row text
	success   color.Color
	warning   color.Color
	danger    color.Color
	download  color.Color
	upload    color.Color
	mention   color.Color
}

// Catppuccin Mocha and Latte.
var (
	darkPalette = palette{
		accent: lipgloss.Color("#CBA6F7"), onAccent: lipgloss.Color("#1E1E2E"),
		text: lipgloss.Color("#CDD6F4"), subtext: lipgloss.Color("#A6ADC8"), muted: lipgloss.Color("#7F849C"), faint: lipgloss.Color("#585B70"),
		border: lipgloss.Color("#45475A"), surface: lipgloss.Color("#313244"), highlight: lipgloss.Color("#F5E0DC"),
		success: lipgloss.Color("#A6E3A1"), warning: lipgloss.Color("#F9E2AF"), danger: lipgloss.Color("#F38BA8"),
		download: lipgloss.Color("#89B4FA"), upload: lipgloss.Color("#FAB387"), mention: lipgloss.Color("#F5C2E7"),
	}
	lightPalette = palette{
		accent: lipgloss.Color("#8839EF"), onAccent: lipgloss.Color("#EFF1F5"),
		text: lipgloss.Color("#4C4F69"), subtext: lipgloss.Color("#5C5F77"), muted: lipgloss.Color("#8C8FA1"), faint: lipgloss.Color("#ACB0BE"),
		border: lipgloss.Color("#BCC0CC"), surface: lipgloss.Color("#DCE0E8"), highlight: lipgloss.Color("#DC8A78"),
		success: lipgloss.Color("#40A02B"), warning: lipgloss.Color("#DF8E1D"), danger: lipgloss.Color("#D20F39"),
		download: lipgloss.Color("#1E66F5"), upload: lipgloss.Color("#FE640B"), mention: lipgloss.Color("#EA76CB"),
	}
	theme = darkPalette
)

// setDarkBackground picks the palette for the terminal background. Bubble Tea
// renders and updates on one goroutine, so swapping the package palette here
// is safe.
func setDarkBackground(dark bool) {
	if dark {
		theme = darkPalette
	} else {
		theme = lightPalette
	}
}

func colorsEnabled() bool { return os.Getenv("NO_COLOR") == "" }

func styled(s string, style lipgloss.Style) string {
	if !colorsEnabled() {
		return s
	}
	return style.Render(s)
}

func fg(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

func accent(s string) string  { return styled(s, fg(theme.accent).Bold(true)) }
func strong(s string) string  { return styled(s, fg(theme.text).Bold(true)) }
func subtle(s string) string  { return styled(s, fg(theme.subtext)) }
func muted(s string) string   { return styled(s, fg(theme.muted)) }
func faint(s string) string   { return styled(s, fg(theme.faint)) }
func danger(s string) string  { return styled(s, fg(theme.danger).Bold(true)) }
func success(s string) string { return styled(s, fg(theme.success)) }
func warning(s string) string { return styled(s, fg(theme.warning)) }

// pill renders a label on a solid background; without colour it falls back to
// brackets so the active item stays identifiable.
func pill(label string, background, foreground color.Color) string {
	if !colorsEnabled() {
		return "[" + label + "]"
	}
	return lipgloss.NewStyle().Bold(true).Background(background).Foreground(foreground).Render(" " + label + " ")
}

func panelStyle() lipgloss.Style {
	s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true)
	if colorsEnabled() {
		s = s.BorderForeground(theme.border)
	}
	return s
}

func borderLine(s string) string { return styled(s, fg(theme.border)) }

// frame draws a rounded box exactly width x height cells. title sits inside
// the top border on the left and detail on the right; body lines are clipped
// and padded to the inner area, which is width-4 by height-2.
func frame(title, detail, body string, width, height int) string {
	width, height = max(4, width), max(2, height)
	inner := width - 4
	top := frameTop(title, detail, width)
	lines := []string{top}
	rows := strings.Split(body, "\n")
	if body == "" {
		rows = nil
	}
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(rows) {
			line = ansi.Truncate(rows[i], inner, "…")
		}
		lines = append(lines, borderLine("│")+" "+line+strings.Repeat(" ", max(0, inner-lipgloss.Width(line)))+" "+borderLine("│"))
	}
	lines = append(lines, borderLine("╰"+strings.Repeat("─", width-2)+"╯"))
	return strings.Join(lines, "\n")
}

func frameTop(title, detail string, width int) string {
	if title == "" {
		return borderLine("╭" + strings.Repeat("─", width-2) + "╮")
	}
	lead := " "
	if strings.HasPrefix(ansi.Strip(title), " ") {
		lead = ""
	}
	left := borderLine("╭─") + lead + title + " "
	right := borderLine("─╮")
	if detail != "" {
		right = " " + detail + " " + borderLine("─╮")
	}
	fill := width - lipgloss.Width(left) - lipgloss.Width(right)
	if fill < 1 && detail != "" {
		right = borderLine("─╮")
		fill = width - lipgloss.Width(left) - lipgloss.Width(right)
	}
	if fill < 1 {
		left = borderLine("╭─") + " " + ansi.Truncate(title, max(0, width-7), "…") + " "
		fill = width - lipgloss.Width(left) - lipgloss.Width(right)
	}
	return left + borderLine(strings.Repeat("─", max(0, fill))) + right
}

// span is one styled cell group of a list row.
type span struct {
	text  string
	style lipgloss.Style
}

func plain(text string) span                   { return span{text: text} }
func tinted(text string, c color.Color) span   { return span{text: text, style: fg(c)} }
func boldSpan(text string, c color.Color) span { return span{text: text, style: fg(c).Bold(true)} }
func gap(n int) span                           { return span{text: strings.Repeat(" ", max(0, n))} }

func joinSpanText(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.text)
	}
	return b.String()
}

func markColor(mark string) color.Color {
	if mark == "○" {
		return theme.faint
	}
	return theme.accent
}

func markSpan(mark string) span { return tinted(mark, markColor(mark)) }

// listRow renders a primary list row across exactly width cells. The cursor row
// gets a "›" marker and a full-width highlight bar; marked rows are tinted with
// the accent. Span text is plain, so truncation never splits an escape code.
func listRow(spans []span, width int, current, marked bool) string {
	width = max(1, width)
	if !colorsEnabled() {
		prefix := "  "
		if current {
			prefix = "› "
		}
		row := ansi.Truncate(prefix+joinSpanText(spans), width, "…")
		return row + strings.Repeat(" ", max(0, width-ansi.StringWidth(row)))
	}
	base := lipgloss.NewStyle()
	if current {
		base = base.Background(theme.surface).Bold(true)
	}
	var b strings.Builder
	prefix := "  "
	if current {
		prefix = "› "
	}
	b.WriteString(base.Foreground(theme.accent).Render(prefix))
	used := 2
	for _, s := range spans {
		if used >= width {
			break
		}
		text := s.text
		room := width - used
		if ansi.StringWidth(text) > room {
			text = ansi.Truncate(text, room, "…")
		}
		style := s.style.Inherit(base)
		if current {
			style = style.Background(theme.surface).Bold(true)
			if s.style.GetForeground() == nil {
				style = style.Foreground(theme.highlight)
			}
		} else if marked && s.style.GetForeground() == nil {
			style = style.Foreground(theme.accent)
		}
		b.WriteString(style.Render(text))
		used += ansi.StringWidth(text)
	}
	if used < width {
		b.WriteString(base.Render(strings.Repeat(" ", width-used)))
	}
	return b.String()
}

// selectedRow marks the cursor row of a secondary list such as a dialog menu
// or a Community pane.
func selectedRow(s string, selected bool) string {
	if !selected {
		return "  " + s
	}
	return styled("› "+ansi.Strip(s), fg(theme.highlight).Bold(true))
}

// columnHeader renders muted column titles aligned with listRow content.
func columnHeader(text string, width int) string {
	return muted(ansi.Truncate("  "+text, max(1, width), "…"))
}

// hint is one "key action" pair in the footer.
func renderHint(h string) string {
	k, label, ok := strings.Cut(h, " ")
	if !ok {
		return muted(h)
	}
	return accent(k) + " " + subtle(label)
}

// hintBar fits as many hints as width allows, keeping the right-hand hints
// (help) always visible.
func hintBar(hints, right []string, width int) string {
	const sep = "   "
	r := make([]string, len(right))
	for i, h := range right {
		r[i] = renderHint(h)
	}
	rightText := strings.Join(r, sep)
	budget := width - lipgloss.Width(rightText) - len(sep)
	var parts []string
	used := 0
	for _, h := range hints {
		rendered := renderHint(h)
		w := lipgloss.Width(rendered)
		if len(parts) > 0 {
			w += len(sep)
		}
		if used+w > budget {
			break
		}
		parts = append(parts, rendered)
		used += w
	}
	return spread(strings.Join(parts, sep), rightText, width)
}

// overlay draws the bordered card found in modal (a full-screen string from
// lipgloss.Place) over a dimmed copy of background. It returns modal unchanged
// when no bordered card is present.
func overlay(background, modal string, width, height int) string {
	fgLines := strings.Split(modal, "\n")
	top, bottom, left, right := -1, -1, width, 0
	for i, line := range fgLines {
		stripped := strings.TrimRight(ansi.Strip(line), " ")
		if stripped == "" {
			continue
		}
		if top < 0 {
			if !strings.HasPrefix(strings.TrimLeft(stripped, " "), "╭") {
				return modal
			}
			top = i
		}
		bottom = i
		left = min(left, ansi.StringWidth(stripped)-ansi.StringWidth(strings.TrimLeft(stripped, " ")))
		right = max(right, ansi.StringWidth(stripped))
	}
	if top < 0 {
		return modal
	}
	bgLines := strings.Split(background, "\n")
	out := make([]string, 0, height)
	dim := fg(theme.faint)
	for y := 0; y < height; y++ {
		bg := ""
		if y < len(bgLines) {
			bg = ansi.Strip(bgLines[y])
		}
		bg += strings.Repeat(" ", max(0, width-ansi.StringWidth(bg)))
		if y < top || y > bottom || y >= len(fgLines) {
			out = append(out, dim.Render(ansi.Truncate(bg, width, "")))
			continue
		}
		out = append(out, dim.Render(ansi.Cut(bg, 0, left))+ansi.Cut(fgLines[y], left, right)+dim.Render(ansi.Cut(bg, right, width)))
	}
	return strings.Join(out, "\n")
}
