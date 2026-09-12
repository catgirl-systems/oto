package tui

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

type chatKey struct{ account, target, kind string }

func chatDraftKey(account, target, kind string) chatKey {
	if kind == "" {
		kind = "private"
	}
	return chatKey{account: account, target: target, kind: kind}
}

func (m model) communityTranscriptSelected() bool {
	c := m.community
	return c.view == 0 && c.chats.conversation.Kind != "room" || c.view == 1 && !c.rooms.feedView && c.chats.conversation.Kind == "room" && c.chats.conversation.Target == c.rooms.selected
}

type chatDraft struct {
	text, requestID string
	cursor          int
}
type chatPosition struct {
	cursor, anchor, selected, seenLatest int64
	offset                               int
	query                                string
	follow                               bool
	back                                 []int64
}
type communityChatsModel struct {
	conversations                   []daemon.CommunityConversation
	listCursor, listNext            int64
	listBack                        []int64
	listRow                         int
	listQuery                       string
	includeClosed                   bool
	listReady                       bool
	listRevision                    uint64
	listErr                         string
	conversation                    daemon.CommunityConversation
	messages                        []daemon.CommunityMessage
	historyNext, newerCount         int64
	historyReady                    bool
	historyRevision                 uint64
	historyErr                      string
	unreadThrough                   int64
	position                        chatPosition
	positions                       map[chatKey]chatPosition
	drafts                          map[chatKey]chatDraft
	composing, blurred              bool
	form, input, inputErr           string
	inputCursor                     int
	dialog                          *chatDialog
	request, operation, readID      uint64
	navigation, operationNavigation uint64
	loading, busy, reading          bool
	cancel, operationCancel         context.CancelFunc
	err                             string
}

type chatDataMsg struct {
	request             uint64
	identity            daemon.CommunityIdentity
	list                daemon.CommunityConversationsPage
	history             daemon.CommunityMessagesPage
	gotList, gotHistory bool
	listErr, historyErr error
}
type chatOperationMsg struct {
	operation    uint64
	navigation   uint64
	identity     daemon.CommunityIdentity
	kind         string
	key          chatKey
	requestID    string
	conversation daemon.CommunityConversation
	result       daemon.CommunitySendResult
	err          error
}
type chatReadMsg struct {
	request               uint64
	identity              daemon.CommunityIdentity
	conversation, through int64
	err                   error
}

func (c *communityChatsModel) cancelLoad() {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.request++
	c.loading = false
}
func (c *communityChatsModel) cancelOperation() {
	if c.operationCancel != nil {
		c.operationCancel()
		c.operationCancel = nil
	}
	c.operation++
	c.busy = false
}
func (m *model) resetCommunityChats(accountChanged bool) {
	c := &m.community.chats
	c.cancelLoad()
	c.cancelOperation()
	c.readID++
	c.reading = false
	c.dialog = nil
	c.messages, c.conversations = nil, nil
	c.listReady, c.historyReady = false, false
	c.listErr, c.historyErr = "", ""
	c.listRevision, c.historyRevision = 0, 0
	if accountChanged {
		m.saveChatPosition()
		c.conversation = daemon.CommunityConversation{}
		c.position = chatPosition{}
		c.composing = false
		c.form, c.input, c.err, c.inputErr = "", "", "", ""
		c.listCursor, c.listNext, c.listRow, c.listQuery, c.listBack = 0, 0, 0, "", nil
	}
}
func (m model) chatKey() chatKey {
	kind := m.community.chats.conversation.Kind
	if kind == "" {
		kind = "private"
	}
	return chatDraftKey(m.community.summary.Account, m.community.chats.conversation.Target, kind)
}
func (m *model) saveChatPosition() {
	c := &m.community.chats
	if c.conversation.Target == "" {
		return
	}
	if c.positions == nil {
		c.positions = make(map[chatKey]chatPosition)
	}
	c.positions[m.chatKey()] = c.position
}

// One cancellable refresh per frontend; history remains a single bounded page.
func (m *model) loadCommunityChats(force bool) tea.Cmd {
	c := &m.community.chats
	if m.client == nil || m.workspace != workspaceCommunity || (m.community.view != 0 && m.community.view != 1) || m.community.view == 0 && !m.community.supports("private-chat") || m.community.view == 1 && !m.community.supports("public-rooms") {
		return nil
	}
	if force {
		c.cancelLoad()
	}
	if c.loading {
		return nil
	}
	private := m.community.view == 0
	list := private && (force || !c.listReady || c.listRevision != m.community.summary.Revision)
	history := m.communityTranscriptSelected() && c.conversation.ID > 0 && (force || !c.historyReady || c.historyRevision != m.community.summary.Revision)
	if !list && !history {
		return m.readCommunityChat()
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	c.cancel, c.loading = cancel, true
	c.request++
	request, identity, client := c.request, m.community.summary.CommunityIdentity, m.client
	lr := daemon.CommunityConversationsRequest{CommunityIdentity: identity, Kind: "private", Cursor: c.listCursor, Query: c.listQuery, IncludeClosed: c.includeClosed}
	hr := daemon.CommunityMessagesRequest{CommunityIdentity: identity, ConversationID: c.conversation.ID, Cursor: c.position.cursor, Query: c.position.query, NewerThan: c.position.seenLatest}
	return func() tea.Msg {
		defer cancel()
		x := chatDataMsg{request: request, identity: identity, gotList: list, gotHistory: history}
		if list {
			x.list, x.listErr = client.CommunityConversations(ctx, lr)
		}
		if history {
			x.history, x.historyErr = client.CommunityMessages(ctx, hr)
		}
		return x
	}
}
func (m *model) applyChatData(x chatDataMsg) tea.Cmd {
	c := &m.community.chats
	if x.request != c.request || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	c.loading = false
	if x.gotList {
		if x.listErr == nil && x.list.CommunityIdentity != x.identity {
			x.listErr = daemon.ErrCommunitySession
		}
		c.listErr = errText(x.listErr)
		if x.listErr == nil {
			selected := int64(0)
			if c.listRow < len(c.conversations) {
				selected = c.conversations[c.listRow].ID
			}
			c.conversations, c.listNext, c.listRevision, c.listReady = x.list.Conversations, x.list.NextCursor, x.list.Revision, true
			c.listRow = max(0, min(c.listRow, len(c.conversations)-1))
			for i, conversation := range c.conversations {
				if conversation.ID == selected {
					c.listRow = i
					break
				}
			}
		}
	}
	if x.gotHistory {
		if x.historyErr == nil && (x.history.CommunityIdentity != x.identity || x.history.Conversation.ID != c.conversation.ID || x.history.Conversation.Target != c.conversation.Target) {
			x.historyErr = daemon.ErrCommunitySession
		}
		c.historyErr = errText(x.historyErr)
		if x.historyErr == nil {
			c.conversation, c.messages = x.history.Conversation, x.history.Messages
			c.historyNext, c.newerCount, c.historyRevision, c.historyReady = x.history.NextCursor, x.history.NewerCount, x.history.Revision, true
			if c.position.follow {
				c.position.seenLatest, c.newerCount = c.conversation.LatestID, 0
				if c.position.selected == 0 && len(c.messages) > 0 {
					c.position.selected = c.messages[0].ID
				}
			}
		}
	}
	return m.readCommunityChat()
}

func (m *model) beginChatOperation() (context.Context, context.CancelFunc, uint64) {
	c := &m.community.chats
	c.busy, c.err = true, ""
	c.operation++
	c.operationNavigation = c.navigation
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	c.operationCancel = cancel
	return ctx, cancel, c.operation
}
func (m *model) openCommunityChat(username string, compose bool) tea.Cmd {
	c := &m.community.chats
	if err := soulseek.ValidateUsername(username); err != nil {
		c.err = err.Error()
		return nil
	}
	if m.client == nil || !m.community.supports("private-chat") {
		m.setNotice("Private messaging unavailable; refresh Community or restart daemon")
		return nil
	}
	if c.busy {
		m.setNotice("Wait for the current chat action to finish")
		return nil
	}
	m.saveChatPosition()
	c.cancelLoad()
	m.switchWorkspace(workspaceCommunity)
	m.community.view, m.community.pane = 0, 1
	c.conversation = daemon.CommunityConversation{Kind: "private", Target: username}
	position, ok := c.positions[m.chatKey()]
	if !ok {
		position.follow = true
	}
	c.position, c.composing = position, compose
	c.messages, c.historyReady, c.historyErr, c.form = nil, false, "", ""
	c.unreadThrough = 0
	m.community.resetUser()
	m.community.target = username
	ctx, cancel, op := m.beginChatOperation()
	identity, client, key := m.community.summary.CommunityIdentity, m.client, m.chatKey()
	return func() tea.Msg {
		defer cancel()
		conversation, err := client.OpenCommunityConversation(ctx, daemon.CommunityOpenConversationRequest{CommunityIdentity: identity, Username: username})
		return chatOperationMsg{operation: op, identity: identity, key: key, kind: "open", conversation: conversation, err: err}
	}
}
func (m *model) applyChatOperation(x chatOperationMsg) tea.Cmd {
	c := &m.community.chats
	if x.operation != c.operation || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	c.busy, c.err = false, errText(x.err)
	if x.err != nil {
		return nil
	}
	if x.kind == "unread" {
		if x.navigation != c.navigation {
			return nil
		}
		if x.conversation.ID == 0 {
			m.setNotice("No unread conversations")
			return nil
		}
		var cmd tea.Cmd
		if x.conversation.Kind == "room" {
			cmd = m.openCommunityRoom(x.conversation.Target)
		} else {
			cmd = m.openCommunityChat(x.conversation.Target, false)
		}
		c.position = chatPosition{follow: true}
		return cmd
	}
	c.dialog = nil
	switch x.kind {
	case "open":
		if x.conversation.Target != c.conversation.Target || x.conversation.Kind != c.conversation.Kind {
			c.err = "Conversation changed; reopen chat"
			return nil
		}
		c.conversation, c.unreadThrough = x.conversation, x.conversation.ReadThrough
	case "send":
		if x.key.kind == "room" && x.result.State != "sent" {
			c.err = "Room send " + x.result.State + "; draft kept. Explicit retry may duplicate an earlier write."
			c.dialog = &chatDialog{kind: "room-retry", label: c.err, identity: x.identity}
			return nil
		}
		if x.result.CommunityIdentity != x.identity {
			c.err = daemon.ErrCommunitySession.Error()
			return nil
		}
		if c.conversation.ID == x.result.ConversationID && c.operationNavigation == c.navigation {
			c.position = chatPosition{follow: true, selected: x.result.MessageID}
		}
		if d := c.drafts[x.key]; d.requestID == x.requestID {
			delete(c.drafts, x.key)
		}
		m.setNotice("Message " + x.result.State + "; sent means written to server, not delivered/read")
	case "close":
		m.saveChatPosition()
		c.conversation, c.messages, c.composing = daemon.CommunityConversation{}, nil, false
		if c.operationNavigation == c.navigation {
			m.community.pane = 0
		}
	case "clear":
		if c.operationNavigation == c.navigation {
			c.position = chatPosition{follow: true}
		}
	case "export":
		if c.operationNavigation == c.navigation {
			c.form = ""
		}
		m.setNotice("History exported privately; existing files were not overwritten")
	}
	return tea.Batch(m.loadCommunitySummary(), m.loadCommunityChats(true))
}
func (m *model) sendCommunityChat() tea.Cmd {
	c := &m.community.chats
	if c.busy || m.client == nil || c.conversation.ID == 0 {
		return nil
	}
	if c.conversation.Kind == "room" && !m.community.rooms.selectedRoomJoined() {
		c.err = "Room send requires confirmed membership"
		return nil
	}
	key := m.chatKey()
	d := c.drafts[key]
	if err := validateChatDraft(d.text, false); err != nil {
		c.err = err.Error()
		return nil
	}
	if d.requestID == "" {
		d.requestID = rand.Text()
		c.drafts[key] = d
	}
	ctx, cancel, op := m.beginChatOperation()
	identity, client := m.community.summary.CommunityIdentity, m.client
	if c.conversation.Kind == "room" {
		req := daemon.CommunityRoomSendRequest{CommunityIdentity: identity, Room: key.target, Text: d.text, RequestID: d.requestID}
		return func() tea.Msg {
			defer cancel()
			result, err := client.SendCommunityRoom(ctx, req)
			return chatOperationMsg{operation: op, identity: identity, kind: "send", key: key, requestID: req.RequestID, result: result, err: err}
		}
	}
	req := daemon.CommunitySendRequest{CommunityIdentity: identity, Username: key.target, Text: d.text, RequestID: d.requestID}
	return func() tea.Msg {
		defer cancel()
		result, err := client.SendCommunityPrivate(ctx, req)
		return chatOperationMsg{operation: op, identity: identity, kind: "send", key: key, requestID: req.RequestID, result: result, err: err}
	}
}
func (m *model) readCommunityChat() tea.Cmd {
	c := &m.community.chats
	if m.client == nil || c.reading || !c.historyReady || c.historyErr != "" || c.conversation.Kind != "room" && !m.community.supports("private-chat") || !m.chatTranscriptVisible() || !c.position.follow || c.position.query != "" || c.conversation.Unread == 0 || len(c.messages) == 0 || c.messages[0].ID != c.conversation.LatestID {
		return nil
	}
	if c.messages[0].ID <= c.conversation.ReadThrough {
		return nil
	}
	c.readID++
	c.reading = true
	request, identity, client := c.readID, m.community.summary.CommunityIdentity, m.client
	conversation, through := c.conversation.ID, c.messages[0].ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		err := client.CommunityConversationAction(ctx, daemon.CommunityConversationActionRequest{CommunityIdentity: identity, ConversationID: conversation, Action: "read", ThroughID: through})
		return chatReadMsg{request, identity, conversation, through, err}
	}
}
func (m *model) applyChatRead(x chatReadMsg) tea.Cmd {
	c := &m.community.chats
	if x.request != c.readID || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	c.reading = false
	if x.err != nil {
		c.err = "Read state not saved: " + x.err.Error()
		return nil
	}
	if x.conversation == c.conversation.ID {
		c.conversation.ReadThrough = max(c.conversation.ReadThrough, x.through)
	}
	return m.loadCommunitySummary()
}

func (m *model) nextUnreadChat() tea.Cmd {
	c := &m.community.chats
	if c.busy || m.client == nil || !m.community.supports("private-chat") {
		return nil
	}
	ctx, cancel, op := m.beginChatOperation()
	identity, client, current := m.community.summary.CommunityIdentity, m.client, c.conversation.ID
	navigation := c.navigation
	return func() tea.Msg {
		defer cancel()
		x := chatOperationMsg{operation: op, navigation: navigation, identity: identity, kind: "unread"}
		var first daemon.CommunityConversation
		req := daemon.CommunityConversationsRequest{CommunityIdentity: identity, IncludeClosed: true}
		for {
			page, err := client.CommunityConversations(ctx, req)
			if err != nil {
				x.err = err
				return x
			}
			if page.CommunityIdentity != identity {
				x.err = daemon.ErrCommunitySession
				return x
			}
			for _, conversation := range page.Conversations {
				if conversation.Unread == 0 {
					continue
				}
				if first.ID == 0 {
					first = conversation
				}
				if conversation.ID > current {
					x.conversation = conversation
					return x
				}
			}
			if page.NextCursor == 0 {
				break
			}
			if page.NextCursor <= req.Cursor {
				x.err = errors.New("community: invalid conversation cursor")
				return x
			}
			req.Cursor = page.NextCursor
		}
		x.conversation = first
		return x
	}
}
