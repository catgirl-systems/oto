package tui

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

type chatDialog struct {
	kind, label, requestID         string
	identity                       daemon.CommunityIdentity
	conversation, message, through int64
	confirm                        bool
	scroll                         int
}

func validateChatDraft(text string, editing bool) error {
	if !utf8.ValidString(text) || len(text) > soulseek.MaxChatBytes {
		return fmt.Errorf("Chat text must be valid UTF-8 within %d bytes; nothing was truncated", soulseek.MaxChatBytes)
	}
	if strings.IndexFunc(text, func(r rune) bool {
		return r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r))
	}) >= 0 {
		return errors.New("Chat text contains terminal controls; nothing was pasted")
	}
	if !editing {
		return soulseek.ValidateChatText(text)
	}
	return nil
}
func (m *model) setChatDraft(text string, cursor int) {
	c := &m.community.chats
	if err := validateChatDraft(text, true); err != nil {
		c.err = err.Error()
		return
	}
	if c.drafts == nil {
		c.drafts = make(map[chatKey]chatDraft)
	}
	c.drafts[m.chatKey()] = chatDraft{text: text, cursor: cursor}
	c.err = ""
}
func (m *model) pasteCommunityChat(text string) {
	c := &m.community.chats
	if c.dialog != nil || c.busy {
		return
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !utf8.ValidString(text) {
		c.err, c.inputErr = "Paste must be valid UTF-8", "Paste must be valid UTF-8"
		return
	}
	if c.form != "" {
		value, cursor := insertText(c.input, text, c.inputCursor)
		if !utf8.ValidString(value) || len(value) > 1024 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			c.inputErr = "Use a single line within 1024 bytes"
			return
		}
		c.input, c.inputCursor, c.inputErr = value, cursor, ""
		return
	}
	if !c.composing {
		return
	}
	d := c.drafts[m.chatKey()]
	if d.requestID != "" {
		c.err = "Submission may already be stored. Enter reconciles it safely before editing."
		return
	}
	// Terminals (including tmux) may paste line breaks as bare CR.
	text = strings.ReplaceAll(text, "\r", "\n")
	value, cursor := insertText(d.text, text, d.cursor)
	m.setChatDraft(value, cursor)
}
func (m *model) chatComposerKey(k tea.KeyPressMsg) tea.Cmd {
	c := &m.community.chats
	d := c.drafts[m.chatKey()]
	if k.String() == "esc" {
		c.composing = false
		return nil
	}
	if c.busy {
		return nil
	}
	if k.String() == "enter" {
		if strings.Contains(d.text, "\n") && d.requestID == "" {
			if err := validateChatDraft(d.text, false); err != nil {
				c.err = err.Error()
				return nil
			}
			c.dialog = &chatDialog{kind: "paste", label: "Send to " + c.conversation.Target + "? Pasted line breaks will be sent as spaces.", identity: m.community.summary.CommunityIdentity}
			return nil
		}
		return m.sendCommunityChat()
	}
	if d.requestID != "" {
		c.err = "Submission may already be stored. Enter reconciles it safely before editing."
		return nil
	}
	if k.String() == "tab" || k.String() == "shift+tab" {
		runes := []rune(d.text)
		cursor := max(0, min(d.cursor, len(runes)))
		start := cursor
		for start > 0 && !unicode.IsSpace(runes[start-1]) {
			start--
		}
		prefix, target := string(runes[start:cursor]), c.conversation.Target
		if prefix != "" && strings.HasPrefix(strings.ToLower(target), strings.ToLower(prefix)) {
			text := string(runes[:start]) + target + string(runes[cursor:])
			m.setChatDraft(text, start+utf8.RuneCountInString(target))
		}
		return nil
	}
	text, cursor, _ := editText(d.text, d.cursor, k)
	m.setChatDraft(text, cursor)
	return nil
}
func (m *model) chatFormKey(k tea.KeyPressMsg) tea.Cmd {
	c := &m.community.chats
	switch k.String() {
	case "esc":
		c.form = ""
	case "enter":
		switch c.form {
		case "new":
			if err := soulseek.ValidateUsername(c.input); err != nil {
				c.inputErr = err.Error()
				return nil
			}
			return m.openCommunityChat(c.input, true)
		case "find":
			c.position = chatPosition{query: c.input, follow: c.input == "", seenLatest: c.position.seenLatest}
			c.form = ""
			return m.loadCommunityChats(true)
		case "filter":
			c.listQuery, c.listCursor, c.listBack, c.listRow, c.form = c.input, 0, nil, 0, ""
			return m.loadCommunityChats(true)
		case "text", "json":
			return m.exportCommunityChat(c.form, c.input)
		}
	default:
		value, cursor, _ := editText(c.input, c.inputCursor, k)
		if len(value) <= 1024 && strings.IndexFunc(value, unicode.IsControl) < 0 {
			c.input, c.inputCursor, c.inputErr = value, cursor, ""
		}
	}
	return nil
}
func (m *model) beginChatForm(kind string) {
	c := &m.community.chats
	c.form, c.input, c.inputCursor, c.inputErr = kind, "", 0, ""
	if kind == "find" {
		c.input = c.position.query
	}
	if kind == "filter" {
		c.input = c.listQuery
	}
	c.inputCursor = utf8.RuneCountInString(c.input)
}
func (m *model) chatKeyPress(k tea.KeyPressMsg) (bool, tea.Cmd) {
	c := &m.community.chats
	if c.composing {
		return true, m.chatComposerKey(k)
	}
	if c.form != "" {
		return true, m.chatFormKey(k)
	}
	room := m.community.view == 1 && m.communityTranscriptSelected()
	if !room && !m.community.supports("private-chat") {
		return false, nil
	}
	if k.String() == "ctrl+n" {
		if room {
			return false, nil
		}
		return true, m.nextUnreadChat()
	}
	if m.community.view != 0 && !room || m.community.view == 0 && m.community.pane != 0 && !m.communityTranscriptSelected() {
		return false, nil
	}
	switch k.String() {
	case "N":
		m.beginChatForm("new")
	case "f":
		if m.community.pane == 0 {
			m.beginChatForm("filter")
		} else if c.conversation.ID > 0 {
			m.beginChatForm("find")
		}
	case "h":
		if m.community.pane != 0 {
			return false, nil
		}
		c.includeClosed = !c.includeClosed
		c.listCursor, c.listBack, c.listRow = 0, nil, 0
		return true, m.loadCommunityChats(true)
	case "r":
		if c.conversation.Target != "" && c.conversation.ID == 0 {
			return true, m.openCommunityChat(c.conversation.Target, c.composing)
		}
		return true, tea.Batch(m.loadCommunitySummary(), m.loadCommunityChats(true))
	case "enter", "right":
		if room && m.community.pane == 0 {
			return false, nil
		}
		if m.community.pane == 0 && c.listRow < len(c.conversations) {
			return true, m.openCommunityChat(c.conversations[c.listRow].Target, false)
		}
		if m.community.pane == 1 && c.conversation.ID > 0 {
			if room && !m.community.rooms.selectedRoomJoined() {
				c.err = "Join the room and wait for confirmed membership before sending"
				return true, nil
			}
			c.composing = true
			return true, nil
		}
		return false, nil
	case "i":
		if c.conversation.ID > 0 {
			if room && !m.community.rooms.selectedRoomJoined() {
				c.err = "Join the room and wait for confirmed membership before sending"
				return true, nil
			}
			m.community.pane, c.composing = 1, true
		}
	case "up", "k", "down", "j", "pgup", "pgdown", "home", "end":
		if m.community.pane == 2 {
			return false, nil
		}
		if m.community.pane == 0 {
			delta := 1
			if k.String() == "up" || k.String() == "k" {
				delta = -1
			}
			if k.String() == "pgup" {
				delta = -m.pageRows()
			}
			if k.String() == "pgdown" {
				delta = m.pageRows()
			}
			if k.String() == "home" {
				delta = -len(c.conversations)
			}
			if k.String() == "end" {
				delta = len(c.conversations)
			}
			c.listRow = max(0, min(len(c.conversations)-1, c.listRow+delta))
			return true, nil
		}
		if k.String() == "end" {
			c.position = chatPosition{follow: true}
			return true, m.loadCommunityChats(true)
		}
		if k.String() == "up" || k.String() == "k" {
			return true, m.selectCommunityChat(true)
		}
		if k.String() == "down" || k.String() == "j" {
			return true, m.selectCommunityChat(false)
		}
		return true, m.scrollCommunityChat(k.String())
	case "p", "n":
		if m.community.pane == 2 {
			return false, nil
		}
		if m.community.pane == 0 {
			if k.String() == "n" && c.listNext != 0 {
				c.listBack = append(c.listBack, c.listCursor)
				c.listCursor = c.listNext
			} else if k.String() == "p" && len(c.listBack) > 0 {
				c.listCursor = c.listBack[len(c.listBack)-1]
				c.listBack = c.listBack[:len(c.listBack)-1]
			} else {
				return true, nil
			}
			c.listRow = 0
		} else {
			m.pinChatHistory()
			if k.String() == "p" && c.historyNext != 0 {
				c.position.back = append(c.position.back, c.position.cursor)
				c.position.cursor = c.historyNext
			} else if k.String() == "n" && len(c.position.back) > 0 {
				c.position.cursor = c.position.back[len(c.position.back)-1]
				c.position.back = c.position.back[:len(c.position.back)-1]
			} else {
				return true, nil
			}
			c.position.anchor, c.position.offset = 0, 0
		}
		return true, m.loadCommunityChats(true)
	case "y":
		if message, ok := m.selectedChatMessage(); ok {
			m.setNotice("Copy requested using OSC52; use export if your terminal does not support it")
			return true, tea.SetClipboard(message.Text)
		}
	case "e", "E":
		if c.conversation.ID > 0 {
			format := "text"
			if k.String() == "E" {
				format = "json"
			}
			m.beginChatForm(format)
		}
	case "ctrl+w":
		if c.conversation.ID > 0 && !c.busy {
			return true, m.chatConversationAction("close", c.conversation.ID, 0)
		}
	case "C", "R", "X":
		if c.busy || c.conversation.ID == 0 {
			return true, nil
		}
		d := &chatDialog{identity: m.community.summary.CommunityIdentity, conversation: c.conversation.ID, through: c.conversation.LatestID, requestID: rand.Text()}
		if k.String() == "C" {
			d.kind, d.label = "clear", fmt.Sprintf("Clear history with %s through #%d? Unresolved outgoing messages and replay receipts stay.", c.conversation.Target, d.through)
		} else if message, ok := m.selectedChatMessage(); ok && message.Direction == "outgoing" {
			d.message = message.ID
			if k.String() == "R" {
				if message.State != "unknown" && message.State != "failed" && message.State != "cancelled" {
					c.err = "Only unknown, failed or cancelled messages can be retried"
					return true, nil
				}
				d.kind, d.label = "retry", fmt.Sprintf("Retry %s message #%d to %s? An earlier attempt may already have reached the server.", message.State, message.ID, c.conversation.Target)
			} else {
				if message.State == "sent" || message.State == "sending" {
					c.err = "Sent or sending messages cannot be cancelled"
					return true, nil
				}
				d.kind, d.label = "cancel", fmt.Sprintf("Cancel message #%d to %s locally? This cannot retract a previous write.", message.ID, c.conversation.Target)
			}
		} else {
			c.err = "Scroll to an outgoing message first"
			return true, nil
		}
		c.dialog = d
	default:
		return false, nil
	}
	return true, nil
}
func (m model) selectedChatMessage() (daemon.CommunityMessage, bool) {
	c := m.community.chats
	for _, message := range c.messages {
		if message.ID == c.position.selected {
			return message, true
		}
	}
	return daemon.CommunityMessage{}, false
}
func (m *model) chatDialogKey(k tea.KeyPressMsg) tea.Cmd {
	c, d := &m.community.chats, m.community.chats.dialog
	if k.String() == "esc" {
		c.dialog = nil
		return nil
	}
	if c.busy {
		return nil
	}
	switch k.String() {
	case "left", "right", "tab", "shift+tab":
		d.confirm = !d.confirm
	case "up", "pgup":
		d.scroll = max(0, d.scroll-max(1, m.height/2))
	case "down", "pgdown":
		d.scroll += max(1, m.height/2)
	case "enter":
		if !d.confirm {
			c.dialog = nil
			return nil
		}
		if d.kind == "quit" {
			c.dialog = nil
			if m.transient && m.active() {
				m.confirm = true
				return nil
			}
			return tea.Quit
		}
		if d.identity != m.community.summary.CommunityIdentity {
			c.err = daemon.ErrCommunitySession.Error()
			return nil
		}
		switch d.kind {
		case "room-retry":
			key := m.chatKey()
			draft := c.drafts[key]
			draft.requestID = rand.Text()
			c.drafts[key] = draft
			c.dialog = nil
			return m.sendCommunityChat()
		case "paste":
			c.dialog = nil
			return m.sendCommunityChat()
		case "clear":
			return m.chatConversationAction("clear", d.conversation, d.through)
		case "retry", "cancel":
			ctx, cancel, op := m.beginChatOperation()
			identity, client, kind := d.identity, m.client, d.kind
			req := daemon.CommunityMessageActionRequest{CommunityIdentity: identity, MessageID: d.message, Action: kind, RequestID: d.requestID, Confirm: true}
			return func() tea.Msg {
				defer cancel()
				err := client.CommunityMessageAction(ctx, req)
				return chatOperationMsg{operation: op, identity: identity, kind: kind, err: err}
			}
		}
	}
	return nil
}
func (m *model) chatConversationAction(kind string, conversation, through int64) tea.Cmd {
	if m.client == nil || m.community.chats.busy {
		return nil
	}
	ctx, cancel, op := m.beginChatOperation()
	identity, client := m.community.summary.CommunityIdentity, m.client
	req := daemon.CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation, Action: kind, ThroughID: through, Confirm: kind == "clear"}
	return func() tea.Msg {
		defer cancel()
		err := client.CommunityConversationAction(ctx, req)
		return chatOperationMsg{operation: op, identity: identity, kind: kind, err: err}
	}
}
func (m model) chatDraftCount() int {
	count := 0
	for _, d := range m.community.chats.drafts {
		if d.text != "" {
			count++
		}
	}
	return count
}
func (m *model) confirmChatDraftQuit() bool {
	if count := m.chatDraftCount(); count > 0 {
		m.community.chats.dialog = &chatDialog{kind: "quit", label: fmt.Sprintf("Quit and discard %d local draft(s), including other accounts? Stored messages remain in the daemon.", count)}
		return true
	}
	return false
}
