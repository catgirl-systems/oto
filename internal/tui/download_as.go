package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

type downloadAsForm struct {
	user, filename, name, err string
	size, revision            uint64
	entryID, cursor           int
	pending                   bool
}

type downloadAsMsg struct{ err error }

func (m *model) openDownloadAs() {
	_, node := m.currentTree().node(m.cursor)
	if node == nil || node.kind != treeFile || node.source < 0 {
		m.setNotice("Download as: highlight one file, not a folder")
		return
	}
	selectionID := node.source
	if m.workspace == workspaceBrowse && m.browsePaged {
		selectionID = remoteNodeIDValue(node.id)
	}
	for id, selected := range m.selected {
		if selected && id != selectionID {
			m.setNotice("Download as: clear other selections first")
			return
		}
	}
	form := &downloadAsForm{}
	if m.workspace == workspaceSearch {
		if node.source >= len(m.results) {
			return
		}
		x := m.results[node.source]
		form.user, form.filename, form.size = x.user, x.path, x.size
	} else {
		if node.source >= len(m.entries) {
			return
		}
		x := m.entries[node.source]
		form.user, form.filename, form.size = m.browseUser, x.name, x.size
		if m.browsePaged {
			form.revision, form.entryID = m.browseRevision, selectionID
		}
	}
	form.name = browseEntryLabel(form.filename)
	if strings.IndexFunc(form.name, unicode.IsControl) >= 0 {
		form.name, form.err = "", "Enter a local filename without control characters"
	}
	form.cursor = len([]rune(form.name))
	m.downloadAs = form
}

func (m *model) downloadAsKey(k tea.KeyPressMsg) tea.Cmd {
	form := m.downloadAs
	if k.String() == "ctrl+c" {
		return tea.Quit
	}
	if form.pending {
		return nil
	}
	switch k.String() {
	case "esc":
		m.downloadAs = nil
	case "enter":
		destination, err := daemon.DownloadAsDestination(form.user, form.filename, form.name)
		if err != nil {
			form.err = err.Error()
			return nil
		}
		form.pending, form.err = true, ""
		request := *form // Polling must not change the file/revision being confirmed.
		return func() tea.Msg {
			var err error
			if request.entryID != 0 {
				var result daemon.BrowseDownloadResult
				result, err = m.client.QueueBrowse(m.ctx, daemon.BrowseDownloadRequest{Username: request.user, Revision: request.revision, Selection: map[int]bool{request.entryID: true}, Destination: destination})
				if err == nil && result.Queued == 0 {
					err = fmt.Errorf("file is already in download history; nothing queued")
				}
			} else {
				_, err = m.client.QueueDownloads(m.ctx, []daemon.DownloadRequest{{Username: request.user, Files: []daemon.DownloadItem{{Filename: request.filename, Size: request.size, Destination: destination}}}})
			}
			return downloadAsMsg{err: err}
		}
	default:
		form.name, form.cursor, _ = editText(form.name, form.cursor, k)
		form.err = ""
	}
	return nil
}

func (m model) downloadAsView() string {
	form := m.downloadAs
	width := max(1, min(76, m.width-6))
	bodyWidth := max(1, width-4)
	runes := []rune(form.name)
	cursor := max(0, min(form.cursor, len(runes)))
	start := max(0, lipgloss.Width(string(runes[:cursor]))-bodyWidth+1)
	input := ansi.Cut(renderInput("", form.name, cursor, false, lipgloss.NewStyle()), start, start+bodyWidth)
	body := []string{strong("Download as…"), "", fmt.Sprintf("Peer: %q", form.user), fmt.Sprintf("Remote: %q", form.filename), "", "Save as:", input, "", "Keeps the normal download folder.", form.err, "enter download · esc cancel"}
	if form.pending {
		body[len(body)-1] = "Queueing download…"
	}
	for i := range body {
		body[i] = trunc(body[i], bodyWidth)
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panelStyle().Width(width).Padding(1, 1).Render(strings.Join(body, "\n")))
}

func (m *model) pasteDownloadAs(text string) {
	form := m.downloadAs
	if form.pending {
		return
	}
	if strings.IndexFunc(text, unicode.IsControl) >= 0 {
		form.err = "Filename cannot contain control characters"
		return
	}
	form.name, form.cursor = insertText(form.name, text, form.cursor)
	form.err = ""
}
