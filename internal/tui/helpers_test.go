package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// must fails the test immediately when err is non-nil.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// failIf fails the test when cond is true.
func failIf(t *testing.T, cond bool, args ...any) {
	t.Helper()
	if cond {
		t.Fatal(args...)
	}
}

// failIfFmt is failIf with t.Fatalf formatting.
func failIfFmt(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if cond {
		t.Fatalf(format, args...)
	}
}

// panelText renders a workspace the way the main view frames it: the border
// title and detail followed by the body, with styling stripped.
func panelText(m model, ws workspace, width, height int) string {
	m.workspace = ws
	title, detail := m.panelTitle(width + 4)
	var body string
	switch ws {
	case workspaceSearch:
		body = m.renderSearch(width, height)
	case workspaceBrowse:
		body = m.renderBrowse(width, height)
	case workspaceTransfers:
		body = m.renderTransfers(width, height)
	default:
		body = m.mainView()
	}
	return ansi.Strip(title + " " + detail + "\n" + body)
}

// plainText strips styling from rendered lines and collapses runs of spaces,
// so assertions read "Label value" regardless of column padding.
func plainText(lines []string) string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.Join(strings.Fields(ansi.Strip(line)), " ")
	}
	return strings.Join(out, "\n")
}
