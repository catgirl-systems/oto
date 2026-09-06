package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Browse failures belong to the tab, not the transient global status message.
func (tab browseTab) failure() (heading, detail string) {
	if tab.err != "" {
		return "! Browse failed — " + browseErrorText(tab.user), browseErrorText(tab.err)
	}
	var latest browsePageState
	for _, page := range tab.pages {
		if page.err != "" && (latest.err == "" || page.request > latest.request) {
			latest = page
		}
	}
	if latest.err == "" {
		return "", ""
	}
	context := latest.folder
	if context == "" {
		context = "/"
	}
	if latest.query != "" {
		context += " · search: " + latest.query
	}
	return "! Could not load browse page", browseErrorText(context) + ": " + browseErrorText(latest.err)
}

func (m model) browseFailure() (heading, detail string) {
	if m.browseTabIndex < 0 || m.browseTabIndex >= len(m.browseTabs) {
		return "", ""
	}
	return m.browseTabs[m.browseTabIndex].failure()
}

func browseErrorText(text string) string {
	return strings.Join(strings.FieldsFunc(ansi.Strip(text), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
}

func browseErrorLines(heading, detail string, width, height int) []string {
	if width < 1 || height < 1 {
		return nil
	}
	lines := []string{danger(ansi.Truncate(heading, width, "…"))}
	if height == 1 {
		return lines
	}
	if height > 2 {
		details := strings.Split(ansi.Wrap(detail, width, ""), "\n")
		limit := min(len(details), height-2)
		if limit < len(details) {
			details[limit-1] = ansi.Truncate(details[limit-1], max(0, width-1), "") + "…"
		}
		lines = append(lines, details[:limit]...)
	}
	return append(lines, muted(ansi.Truncate("r retry browse · / browse another user", width, "…")))
}

// Evict file rows without forgetting an outstanding failure for this snapshot.
func evictBrowsePage(pages map[string]browsePageState, key string) {
	page := pages[key]
	if page.err == "" {
		delete(pages, key)
		return
	}
	pages[key] = browsePageState{folder: page.folder, query: page.query, err: page.err, request: page.request}
}
