package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestCommunityRoomsOfflinePagesHistoryAndDraftIsolation(t *testing.T) {
	m, store := privateChatModel(t)
	identity := m.community.summary.CommunityIdentity
	for i := range 65 {
		summary, err := m.client.CommunitySummary(m.ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.client.CommunityRoomAction(m.ctx, daemon.CommunityRoomActionRequest{CommunityIdentity: identity, Room: fmt.Sprintf("room %02d", i), Action: "remember", RequestID: fmt.Sprintf("seed-%d", i), Revision: summary.Revision})
		if err != nil {
			t.Fatal(err)
		}
	}
	m.community.view = 1
	drainChat(t, &m, m.loadCommunitySummary())
	r := &m.community.rooms
	if len(r.rooms) != 50 || r.next == "" {
		t.Fatal("offline directory", len(r.rooms), r.listErr)
	}
	drainChat(t, &m, m.roomKey(chatPress("n")))
	if len(r.rooms) != 15 || r.cursor == "" || len(r.back) != 1 {
		t.Fatal("next page reset", len(r.rooms), r.cursor)
	}
	drainChat(t, &m, m.roomKey(chatPress("p")))
	if len(r.rooms) != 50 || r.cursor != "" {
		t.Fatal("previous page", r.cursor)
	}
	// Forced refresh cancels a pending load, but must launch its replacement.
	old := m.loadCommunityRooms(true)
	next := m.loadCommunityRooms(true)
	if old == nil || next == nil {
		t.Fatal("cancelled load wedged replacement")
	}
	drainChat(t, &m, next)
	name := "room 00"
	drainChat(t, &m, m.openCommunityChat(name, true))
	m.setChatDraft("private draft", 5)
	m.community.chats.composing = false
	drainChat(t, &m, m.openCommunityRoom(name))
	c := &m.community.chats
	if c.conversation.Kind != "room" || c.conversation.ID == 0 || r.active.Joined {
		t.Fatal("offline history implicitly joined", c.conversation, r.active)
	}
	if c.drafts[m.chatKey()].text != "" {
		t.Fatal("private draft leaked into room")
	}
	m.setChatDraft("room draft", 4)
	if m.chatDraftCount() != 2 {
		t.Fatal("draft keys collided")
	}
	_, err := store.Queries().InsertCommunityMessage(m.ctx, db.InsertCommunityMessageParams{Account: identity.Account, ConversationID: c.conversation.ID, Sender: "Alice", Direction: "incoming", State: "received", Body: "room-only history", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	drainChat(t, &m, m.loadCommunityChats(true))
	if len(c.messages) != 1 || c.messages[0].Text != "room-only history" {
		t.Fatal("offline room history", c.historyErr, c.messages)
	}
	m.community.view = 0
	if m.chatTranscriptVisible() || strings.Contains(strings.Join(m.chatContentPane(60, 20), "\n"), "room-only history") {
		t.Fatal("room transcript read/displayed in private tab")
	}
	drainChat(t, &m, m.openCommunityChat(name, false))
	if c.drafts[m.chatKey()].text != "private draft" {
		t.Fatal("private draft not restored")
	}
}

func TestCommunityRoomsNavigationPasteConfirmationAndStaleResponses(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.view = 1
	r := &m.community.rooms
	m.key(chatPress("N"))
	for _, ch := range "oto test" {
		m.key(chatPress(string(ch)))
	}
	if r.input != "oto test" || r.remember {
		t.Fatal("space changed preference instead of text", r.input, r.remember)
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if !r.remember || m.workspace != workspaceCommunity {
		t.Fatal("form Tab escaped")
	}
	m.pasteCommunityRoom("\nnot sent")
	if r.input != "oto test" || r.inputErr == "" {
		t.Fatal("multiline room name paste accepted")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl}))
	if m.community.view != 0 {
		t.Fatal("cannot leave Rooms")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModCtrl}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyF6}))
	if m.community.view != 1 || m.community.pane != 1 {
		t.Fatal("pane navigation swallowed")
	}
	m.roomAction("leave", "oto test", false)
	if r.dialog == nil || r.dialog.confirm {
		t.Fatal("destructive default")
	}
	if cmd := m.roomDialogKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); cmd != nil || r.dialog != nil {
		t.Fatal("default sent leave")
	}
	identity := m.community.summary.CommunityIdentity
	r.request = 10
	r.rooms = []daemon.CommunityRoom{{Name: "keep"}}
	r.row = 0
	m.applyRoomPage(roomPageMsg{request: 9, identity: identity, page: daemon.CommunityRoomsPage{CommunityIdentity: identity}})
	if len(r.rooms) != 1 {
		t.Fatal("stale request replaced list")
	}
	r.listRevision = 20
	m.applyRoomPage(roomPageMsg{request: 10, identity: identity, page: daemon.CommunityRoomsPage{CommunityIdentity: identity, Revision: 19}})
	if len(r.rooms) != 1 {
		t.Fatal("old revision replaced list")
	}
	r.selected = "keep"
	r.active = daemon.CommunityRoom{Name: "keep", Joined: true, State: "joined"}
	r.membersRequest = 4
	other := identity
	other.Session++
	m.applyRoomMembers(roomMembersMsg{request: 4, identity: other, room: "keep"})
	if !r.active.Joined {
		t.Fatal("old session revoked current membership")
	}
	r.cancelLoads()
	if r.loading || r.membersLoading || r.feedLoading {
		t.Fatal("detach retained loads")
	}
	m.community.chats.conversation = daemon.CommunityConversation{Kind: "room", Target: "keep"}
	m.setChatDraft("retained room draft", 4)
	summary := m.community.summary
	summary.Session++
	m.applyCommunitySummary(communitySummaryMsg{request: m.community.summaryRequest, summary: summary})
	if r.selected != "keep" || r.active.Joined || m.community.chats.drafts[m.chatKey()].text != "retained room draft" {
		t.Fatal("session resync lost selected room/draft or retained membership")
	}
}

func TestCommunityRoomsReadOnlyFeedUnknownDraftAndLayouts(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openCommunityRoom("oto test"))
	r, c := &m.community.rooms, &m.community.chats
	r.active = daemon.CommunityRoom{Name: "oto test", Joined: true, RosterFresh: true, State: "joined"}
	m.community.summary.Connected = true
	m.setChatDraft("q/?猫😀", 3)
	c.composing = true
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		view := m.View().Content
		if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
			t.Fatalf("overflow %v: %dx%d", size, lipgloss.Width(view), lipgloss.Height(view))
		}
		if c.drafts[m.chatKey()].text != "q/?猫😀" {
			t.Fatal("resize lost draft")
		}
	}
	m.width, m.height = 120, 40
	c.composing = false
	m.roomKey(chatPress("G"))
	if !r.feedView || m.chatTranscriptVisible() {
		t.Fatal("feed marks hidden transcript read")
	}
	m.roomKey(chatPress("i"))
	m.roomKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if c.composing {
		t.Fatal("feed became writable")
	}
	r.feedView = false
	key := m.chatKey()
	d := c.drafts[key]
	d.requestID = "uncertain"
	c.drafts[key] = d
	m.applyChatOperation(chatOperationMsg{operation: c.operation, identity: m.community.summary.CommunityIdentity, kind: "send", key: key, requestID: d.requestID, result: daemon.CommunitySendResult{CommunityIdentity: m.community.summary.CommunityIdentity, State: "unknown"}})
	if c.drafts[key].text == "" || c.dialog == nil || c.dialog.confirm {
		t.Fatal("unknown send discarded draft or retried")
	}
	m.chatDialogKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if c.drafts[key].requestID != "uncertain" {
		t.Fatal("cancel changed submission identity")
	}
}
