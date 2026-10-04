package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
)

type chatLine struct {
	id     int64
	offset int
	text   string
}

func (m model) chatTranscriptVisible() bool {
	c := m.community
	if c.rooms.private.editing() || c.rooms.private.dialog != nil {
		return false
	}
	if m.workspace != workspaceCommunity || !m.communityTranscriptSelected() || c.chats.blurred || m.width < 36 || m.height < 8 || c.inspectEditing || c.chats.form != "" || c.rooms.form != "" || c.chats.dialog != nil || c.rooms.dialog != nil || m.setup || m.help || m.details || m.confirm || m.userActions != nil || m.searchScope != nil || m.downloadAs != nil || m.passwordForm || m.folderMenu || m.statusMenu || m.uploadStatusMenu || m.uploadConfirm {
		return false
	}
	if m.width < 80 && c.pane != 1 || m.width < 110 && c.pane == 2 {
		return false
	}
	_, height := m.chatContentSize()
	return m.chatTranscriptHeight(height) > 0
}
func (m model) chatHistoryStart(lines []chatLine, height int) int {
	p := m.community.chats.position
	if p.follow {
		return max(0, len(lines)-height)
	}
	for i, line := range lines {
		if line.id == p.anchor && line.offset >= p.offset {
			return min(i, max(0, len(lines)-height))
		}
	}
	return 0
}
func (m *model) pinChatHistory() {
	c := &m.community.chats
	if c.position.follow || c.position.cursor == 0 {
		if len(c.messages) > 0 {
			c.position.cursor = c.messages[0].ID + 1
		}
	}
	c.position.follow = false
}

func (m *model) selectCommunityChat(older bool) tea.Cmd {
	c := &m.community.chats
	if len(c.messages) == 0 {
		return nil
	}
	index := 0
	for i, message := range c.messages {
		if message.ID == c.position.selected {
			index = i
			break
		}
	}
	if older {
		index = min(len(c.messages)-1, index+1)
	} else {
		index = max(0, index-1)
	}
	width, height := m.chatContentSize()
	height = max(1, m.chatTranscriptHeight(height))
	lines := m.chatHistoryLines(width)
	start := m.chatHistoryStart(lines, height)
	m.pinChatHistory()
	c.position.selected = c.messages[index].ID
	if len(lines) > 0 {
		c.position.anchor, c.position.offset = lines[start].id, lines[start].offset
		for i, line := range lines {
			if line.id == c.position.selected && line.offset == 0 {
				if i < start || i >= start+height {
					c.position.anchor, c.position.offset = line.id, 0
				}
				break
			}
		}
	}
	c.cancelLoad()
	c.historyRevision = 0
	return nil
}
func (m *model) scrollCommunityChat(key string) tea.Cmd {
	c := &m.community.chats
	width, height := m.chatContentSize()
	height = m.chatTranscriptHeight(height)
	lines := m.chatHistoryLines(width)
	if len(lines) == 0 || height == 0 {
		return nil
	}
	start := m.chatHistoryStart(lines, height)
	delta := 1
	switch key {
	case "up", "k":
		delta = -1
	case "pgup":
		delta = -height
	case "pgdown":
		delta = height
	case "home":
		delta = -len(lines)
	}
	start = max(0, min(len(lines)-height, start+delta))
	m.pinChatHistory()
	c.position.anchor, c.position.offset, c.position.selected = lines[start].id, lines[start].offset, lines[start].id
	c.cancelLoad()
	c.historyRevision = 0
	if delta > 0 && start+height >= len(lines) && c.position.query == "" && c.position.seenLatest >= c.conversation.LatestID && len(c.position.back) == 0 {
		c.position.follow, c.position.cursor = true, 0
		c.position.selected = c.messages[0].ID
		return m.readCommunityChat()
	}
	return nil
}

func (m model) chatDialogView() string {
	c, d := m.community.chats, m.community.chats.dialog
	width, height := max(1, m.width), max(1, m.height)
	if width >= 40 {
		width -= 4
	}
	if height >= 8 {
		height -= 2
	}
	cardWidth := max(1, min(64, width))
	bodyWidth := max(1, cardWidth-4)
	rows := max(0, height-4)
	if c.err != "" && rows > 0 {
		rows--
	}
	body := []string{strong(d.label)}
	if d.kind == "paste" {
		body = append(body, strings.ReplaceAll(c.drafts[m.chatKey()].text, "\n", " "))
	}
	lines := communityPane(body, bodyWidth, rows, d.scroll)
	for len(lines) < rows {
		lines = append(lines, "")
	}
	if c.err != "" {
		lines = append(lines, ansi.Truncate(danger("! "+browseErrorText(c.err)), bodyWidth, "…"))
	}
	choices := "[Cancel] Confirm"
	if d.confirm {
		choices = "Cancel [Confirm]"
	}
	if c.busy {
		choices = "Saving…"
	}
	lines = append(lines, ansi.Truncate(accent(choices), bodyWidth, "…"))
	lines = append(lines, ansi.Truncate(muted("←→ choose · Enter/Esc · ↑↓ preview"), bodyWidth, "…"))
	card := panelStyle().Width(cardWidth).Padding(0, 1).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}
