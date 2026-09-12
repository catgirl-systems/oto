package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
)

type chatLine struct {
	id     int64
	offset int
	text   string
}

func (m model) communityColumnSizes(width int) []int {
	if m.width < 80 || m.width < 110 && m.community.pane == 2 {
		return []int{width}
	}
	if m.width >= 110 && (m.community.target != "" || m.community.view == 1 && m.community.rooms.selected != "") || m.community.pane == 2 {
		return []int{24, width - 57, 27}
	}
	return []int{min(24, width/3), width - min(24, width/3) - 3}
}
func (m model) chatContentSize() (int, int) {
	width := max(10, m.width-4)
	if sizes := m.communityColumnSizes(width); len(sizes) > 1 {
		width = sizes[1]
	}
	return max(1, width), max(0, m.height-9)
}
func (m model) chatTranscriptHeight(height int) int {
	rows := 3 // Two header rows and the action hint.
	if m.community.chats.composing {
		rows += 2
	}
	if m.community.chats.err != "" {
		rows++
	}
	return max(0, height-rows)
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
func (m model) chatHistoryLines(width int) []chatLine {
	c := m.community.chats
	var lines []chatLine
	unread := false
	for i := len(c.messages) - 1; i >= 0; i-- {
		message := c.messages[i]
		if !unread && message.Direction == "incoming" && message.ID > c.unreadThrough {
			lines = append(lines, chatLine{id: message.ID, offset: -1, text: "── Unread ──"})
			unread = true
		}
		stamp := message.CreatedAt
		if message.ServerTime != nil {
			stamp = *message.ServerTime
		}
		state := ""
		if message.Direction == "outgoing" {
			state = " [" + message.State + "]"
		}
		if message.Direction == "system" || message.Direction == "incoming" && message.Sender == "server" {
			state += " [system]"
		}
		if message.Mention {
			state += " [mention]"
		}
		marker := " "
		if message.ID == c.position.selected {
			marker = ">"
		}
		text := fmt.Sprintf("%s[%s] #%d %s%s\n%s", marker, stamp.Local().Format("01-02 15:04:05"), message.ID, message.Sender, state, strings.ReplaceAll(message.Text, "\t", "    "))
		if message.Error != "" {
			text += "\n! " + browseErrorText(message.Error)
		}
		offset := 0
		for _, part := range strings.Split(ansi.Wrap(text, max(1, width), ""), "\n") {
			lines = append(lines, chatLine{id: message.ID, offset: offset, text: part})
			offset += len(part) + 1
		}
	}
	return lines
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

func (m model) chatPane(pane, width, height int) []string {
	if pane == 0 {
		return m.chatListPane(width, height)
	}
	if pane == 1 {
		return m.chatContentPane(width, height)
	}
	return m.community.inspectorLines()
}
func (m model) chatListPane(width, height int) []string {
	c := m.community.chats
	lines := []string{"N new chat · f filter"}
	label := "Open chats · h all history"
	if c.includeClosed {
		label = "All history · h open chats"
	}
	if c.listQuery != "" {
		label = "Find: " + c.listQuery
	}
	lines = append(lines, ansi.Truncate(label, width, "…"))
	if c.listErr != "" {
		lines = append(lines, "! "+browseErrorText(c.listErr), "r retry (retained list is stale)")
	}
	if !c.listReady && c.loading {
		lines = append(lines, "Loading chats…")
	}
	rows := max(0, height-len(lines)-1)
	if len(c.conversations) == 0 {
		lines = append(lines, "No matching chats.")
	}
	start := max(0, c.listRow-rows+1)
	for i := start; i < min(len(c.conversations), start+rows); i++ {
		conversation := c.conversations[i]
		marker := " "
		if i == c.listRow {
			marker = ">"
		}
		flags := ""
		if conversation.Unread > 0 {
			flags += fmt.Sprintf(" %d*", conversation.Unread)
		}
		if conversation.Mentions > 0 {
			flags += fmt.Sprintf(" @%d", conversation.Mentions)
		}
		if conversation.Closed {
			flags += " closed"
		}
		if m.community.chats.drafts[chatDraftKey(m.community.summary.Account, conversation.Target, conversation.Kind)].text != "" {
			flags += " draft"
		}
		name := ansi.Truncate(conversation.Target, max(1, width-len(flags)-1), "…")
		lines = append(lines, ansi.Truncate(marker+name+flags, width, "…"))
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, "p/n pages · Enter open")
	return lines[:min(len(lines), max(0, height))]
}
func (m model) chatContentPane(width, height int) []string {
	c := m.community.chats
	if c.conversation.Target == "" || !m.communityTranscriptSelected() {
		return communityPane([]string{"Select a chat or N new chat.", "", "Messages are stored by the daemon.", "Offline sends stay queued; Unknown sends need an explicit retry."}, width, height, 0)
	}
	label := c.conversation.Target
	if c.conversation.Mentions > 0 {
		label += fmt.Sprintf(" · @%d mentions", c.conversation.Mentions)
	}
	state := "End latest · p older / n newer"
	if c.position.query != "" {
		state = "Find: " + c.position.query + " · End clears"
	}
	if !c.position.follow {
		state = fmt.Sprintf("%d new messages · End latest", c.newerCount)
	}
	if !c.historyReady {
		state = "Loading history…"
	}
	if c.conversation.Closed {
		state = "Closed by another frontend · history kept"
	}
	if c.historyErr != "" {
		state = "! Stale: " + browseErrorText(c.historyErr) + " · r retry"
	}
	lines := []string{ansi.Truncate(label, width, "…"), ansi.Truncate(state, width, "…")}
	rows := m.chatTranscriptHeight(height)
	history := m.chatHistoryLines(width)
	start := m.chatHistoryStart(history, rows)
	for _, line := range history[start:min(len(history), start+rows)] {
		lines = append(lines, line.text)
	}
	if len(history) == 0 && rows > 0 {
		lines = append(lines, "No messages in this page.")
	}
	for len(lines) < rows+2 {
		lines = append(lines, "")
	}
	if c.err != "" {
		lines = append(lines, ansi.Truncate("! "+browseErrorText(c.err), width, "…"))
	}
	if c.composing {
		d := c.drafts[m.chatKey()]
		label := fmt.Sprintf("Compose · %d/%d bytes", len(d.text), 64<<10)
		if strings.Contains(d.text, "\n") {
			label = "Multiline · Enter previews before send"
		}
		if c.busy {
			label = "Submitting… draft kept until accepted"
		}
		if d.requestID != "" && !c.busy {
			label = "Enter reconciles pending submission"
		}
		lines = append(lines, ansi.Truncate(label, width, "…"), renderInputWindow(strings.ReplaceAll(strings.ReplaceAll(d.text, "\n", "↵"), "\t", "⇥"), d.cursor, width), ansi.Truncate("Enter send · Tab complete · Esc navigate", width, "…"))
	} else {
		lines = append(lines, ansi.Truncate(fmt.Sprintf("i compose · #%d selected · y copy", c.position.selected), width, "…"))
	}
	return lines[:min(len(lines), max(0, height))]
}
func (m model) chatFormView(width, height int) []string {
	c := m.community.chats
	label := map[string]string{"new": "New chat · exact username", "find": "Find in history · literal, case-sensitive", "filter": "Filter chat names · literal, case-sensitive", "text": "Export text · new file path", "json": "Export JSON · new file path"}[c.form]
	return communityPane([]string{label, renderInputWindow(c.input, c.inputCursor, width), c.inputErr, c.err, "Enter submit · Esc back · paste never submits"}, width, height, 0)
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
	// Pin choices instead of letting a long label/preview push them offscreen.
	rows := max(0, height-2)
	if c.err != "" && rows > 0 {
		rows--
	}
	body := []string{d.label}
	if d.kind == "paste" {
		body = append(body, strings.ReplaceAll(c.drafts[m.chatKey()].text, "\n", " "))
	}
	lines := communityPane(body, width, rows, d.scroll)
	for len(lines) < rows {
		lines = append(lines, "")
	}
	if c.err != "" && height > 2 {
		lines = append(lines, ansi.Truncate("! "+browseErrorText(c.err), width, "…"))
	}
	choices := "[Cancel] Confirm"
	if d.confirm {
		choices = "Cancel [Confirm]"
	}
	if c.busy {
		choices = "Saving…"
	}
	lines = append(lines, ansi.Truncate(choices, width, "…"))
	if height > 1 {
		lines = append(lines, ansi.Truncate("←→ choose · Enter/Esc · ↑↓ preview", width, "…"))
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}
