package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestCommunityBuddiesEditorWorkflow(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.view = 2
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	m.key(chatPress("a"))
	m.key(chatPress("Alice"))
	press(tea.KeyTab)
	for _, ch := range "q/?猫😀" {
		m.key(chatPress(string(ch)))
	}
	press(tea.KeyLeft)
	m.key(chatPress(" "))
	updated, cmd := m.Update(tea.PasteMsg{Content: "\r\nline\t👋"})
	m = updated.(model)
	drainActivity(t, &m, cmd)
	failIf(t, m.help || m.workspace != workspaceCommunity, "editor input became global action")
	want := "q/?猫 \nline\t👋😀"
	failIf(t, m.community.buddies.editor.note != want, "Unicode cursor/paste", m.community.buddies.editor.note)
	before := m.community.buddies.editor.note
	m.pasteCommunityBuddy("\x1b[2J")
	failIf(t, m.community.buddies.editor.note != before || m.community.buddies.editor.err == "", "unsafe paste changed note")
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		screen := m.View().Content
		failIfFmt(t, lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height, "editor overflow %v", size)
	}
	m.width, m.height = 120, 40
	for range 3 {
		press(tea.KeyTab)
		m.key(chatPress(" "))
	}
	press(tea.KeyEnter)
	b := &m.community.buddies
	failIf(t, b.err != "" || b.editor != nil || len(b.buddies) != 1 || m.chatDraftCount() != 0, "save did not complete", b.err)
	buddy := b.buddies[0]
	failIf(t, buddy.Note != want || !buddy.NotifyOnline || !buddy.Trusted || !buddy.Priority, "note/flags not saved", buddy)
	// A separately attached frontend sees persisted metadata, not this draft.
	other := newModel(m.ctx, m.client, "", false, m.cfg)
	other.workspace, other.community.view = workspaceCommunity, 2
	drainChat(t, &other, other.loadCommunitySummary())
	failIf(t, len(other.community.buddies.buddies) != 1 || other.community.buddies.buddies[0].Note != want, "second frontend missed save")
	drainChat(t, &m, m.key(chatPress("e")))
	m.key(chatPress("local draft"))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	failIf(t, m.chatDraftCount() != 1 || !m.confirmChatDraftQuit() || m.community.chats.dialog.confirm, "buddy draft bypassed quit confirmation")
	m.community.chats.dialog = nil
	// Removing a buddy cannot erase history or an unrelated frontend-local draft.
	m.key(chatPress("D"))
	failIf(t, b.dialog == nil || b.dialog.confirm || b.dialog.username != "Alice", "missing Cancel-default exact-user removal")
	press(tea.KeyEnter)
	failIf(t, len(b.buddies) != 1, "cancel removed buddy")
	m.key(chatPress("D"))
	press(tea.KeyRight)
	press(tea.KeyEnter)
	failIf(t, len(b.buddies) != 0 || m.chatDraftCount() != 1, "removal failed or erased local draft", b.err)
	failIf(t, len(other.community.buddies.drafts) != 0, "frontend drafts shared")
}

func TestCommunityBuddiesConflictsPagingAndContext(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.view = 2
	id := m.community.summary.CommunityIdentity
	for i := range 53 {
		_, err := m.client.SetCommunityBuddy(m.ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: fmt.Sprintf("buddy%02d", i), Note: "original"})
		must(t, err)
	}
	drainChat(t, &m, m.loadCommunitySummary())
	b := &m.community.buddies
	failIf(t, len(b.buddies) != 50 || b.next == "", "unbounded list")
	b.row = 20
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	selected := b.selected
	m.community.pane = 0
	drainChat(t, &m, m.key(chatPress("n")))
	failIf(t, len(b.buddies) != 3 || b.selected != selected || b.active.Username != selected, "paging stole selected detail")
	m.community.pane = 1
	drainChat(t, &m, m.key(chatPress("e")))
	e := b.editor
	m.key(chatPress(" local"))
	draft := e.note
	// Presence/page refresh never replaces the loaded editor's note or revision.
	old := e.base
	_, err := m.client.SetCommunityBuddy(m.ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: old.Username, Note: "other frontend", Trusted: true, Revision: &old.Revision})
	must(t, err)
	drainChat(t, &m, m.loadCommunitySummary())
	failIf(t, b.editor != e || e.note != draft, "poll overwrote editor")
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, b.editor == nil || !strings.Contains(b.editor.err, "buddy changed") || b.editor.note != draft, "stale write silently overwrote metadata", b.err)
	m.key(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	failIf(t, b.dialog == nil || b.dialog.confirm, "reload not Cancel-default")
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, b.editor.note != draft, "cancel discarded draft")
	m.key(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, b.editor.note != "other frontend" || !b.editor.trusted || m.chatDraftCount() != 0, "reload failed")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m.community.pane = 0
	m.key(chatPress("f"))
	m.key(chatPress("buddy52"))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, len(b.buddies) != 1 || b.buddies[0].Username != "buddy52" || b.selected != selected, "filter lost exact detail anchor")
	drainChat(t, &m, m.key(chatPress("s")))
	failIf(t, b.sort != "status", "sort not applied")
	m.key(chatPress("U"))
	failIf(t, m.userActions == nil || m.userActions.username != "buddy52", "wrong list context")
	m.userActions.row = 4
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, b.editor == nil || b.editor.username != "buddy52" || b.editor.base.Username != "buddy52", "shared buddy action unavailable")
}

func TestCommunityBuddiesFencingNotificationsAndAccountDrafts(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.view = 2
	m.newBuddyEditor()
	m.key(chatPress("Alice"))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m.key(chatPress("private note"))
	b := &m.community.buddies
	e := b.editor
	key := m.buddyDraftKey("")
	old := m.community.summary
	next := old
	next.BuddyNotification = daemon.DownloadNotification{SessionID: "notice", Sequence: 1, Message: "Alice is online"}
	m.applyCommunitySummary(communitySummaryMsg{request: m.community.summaryRequest, summary: next})
	failIf(t, m.notice != "Alice is online" || b.editor != e, "notification missing or stole focus")
	m.notice = "sentinel"
	m.applyCommunitySummary(communitySummaryMsg{request: m.community.summaryRequest, summary: next})
	failIf(t, m.notice != "sentinel", "duplicate notification")
	next.Session++
	next.BuddyNotification.Sequence++
	request, operation, openRequest := b.request, b.operation, b.openRequest
	m.applyCommunitySummary(communitySummaryMsg{request: m.community.summaryRequest, summary: next})
	failIf(t, m.notice != "sentinel" || b.editor != e || b.drafts[key].note != "private note", "reconnect replayed notification or lost draft")
	m.applyBuddyPage(buddyPageMsg{request: request, identity: old.CommunityIdentity, page: daemon.CommunityBuddiesPage{CommunityIdentity: old.CommunityIdentity, Buddies: []daemon.CommunityBuddy{{Note: "late"}}}})
	m.applyBuddyAction(buddyActionMsg{operation: operation, identity: old.CommunityIdentity, editing: true, key: key})
	m.applyBuddyOpen(buddyOpenMsg{request: openRequest, identity: old.CommunityIdentity})
	failIf(t, b.editor != e || len(b.buddies) != 0 || len(b.drafts) != 1, "stale response changed editor/data")
	next.Account = "other"
	next.Session++
	m.applyCommunitySummary(communitySummaryMsg{request: m.community.summaryRequest, summary: next})
	failIf(t, b.editor != nil || len(b.drafts) != 1 || strings.Contains(m.View().Content, "private note"), "account switch leaked/discarded note")
	m.newBuddyEditor()
	failIf(t, b.editor.note != "", "draft crossed accounts")
	m.community.summary.Capabilities = nil
	b.editor = nil
	m.key(chatPress("a"))
	failIf(t, b.editor != nil, "missing capability allowed add")
}

func TestCommunityBuddiesResponseOrderingAndViews(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.view = 2
	id := m.community.summary.CommunityIdentity
	b := &m.community.buddies
	newer := daemon.CommunityBuddy{CommunityUser: daemon.CommunityUser{Username: "Alice", Country: "FR", StatusFresh: true, Exists: true}, Note: strings.Repeat("猫😀", 400), Trusted: true, Revision: 20}
	older := newer
	older.Note, older.Trusted, older.Revision = "old", false, 10
	b.active, b.selected, b.activeRevision = &newer, "Alice", 100
	m.applyBuddyAction(buddyActionMsg{operation: b.operation, identity: id, username: "Alice", editing: true, result: daemon.CommunityBuddyResult{CommunityIdentity: id, Buddy: older}})
	failIf(t, b.active != &newer, "unversioned mutation response rolled back newer metadata")
	m.applyBuddyPage(buddyPageMsg{request: b.request, identity: id, selected: "Alice", page: daemon.CommunityBuddiesPage{CommunityIdentity: id, Revision: 90}, detail: daemon.CommunityBuddiesPage{CommunityIdentity: id, Revision: 90, Buddies: []daemon.CommunityBuddy{older}}})
	failIf(t, b.active != &newer, "older detail response rolled back metadata")
	b.buddies = []daemon.CommunityBuddy{newer}
	b.err = "resource unavailable"
	m.community.summary.Connected = true
	if text := strings.Join(m.buddyDetailPane(120, 40), "\n"); !strings.Contains(text, "unknown/stale") || !strings.Contains(text, "FR (stale)") {
		t.Fatal("failed resource looks fresh")
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		for pane := range 3 {
			m.community.pane = pane
			screen := m.View().Content
			failIfFmt(t, lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height, "buddy pane %d overflow %v", pane, size)
		}
	}
	// Initial attachment observes the notification baseline without replaying it.
	m.community.ready = false
	m.notice = "baseline"
	next := m.community.summary
	next.BuddyNotification = daemon.DownloadNotification{SessionID: "first", Sequence: 8, Message: "old alert"}
	m.applyCommunitySummary(communitySummaryMsg{request: m.community.summaryRequest, summary: next})
	failIf(t, m.notice != "baseline", "attachment replayed old notification")
	// Leaving during an outstanding editor lookup must not open a hidden form.
	b.loading = false
	cmd := m.openBuddyEditor("Alice", false)
	m.switchWorkspace(workspaceSearch)
	drainChat(t, &m, cmd)
	failIf(t, b.editor != nil || b.opening, "cancelled lookup opened hidden editor")
}
