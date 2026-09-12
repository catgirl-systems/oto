package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCommunityPrivateRoomsFormsDraftsAndLayouts(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.view = 1
	m.key(chatPress("N"))
	m.key(tea.KeyPressMsg(tea.Key{Code: 'p', Mod: tea.ModCtrl}))
	if !m.community.rooms.createPrivate {
		t.Fatal("private creation toggle unavailable")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	drainChat(t, &m, m.openCommunityRoom("oto test"))
	drainChat(t, &m, m.privateRoomOpen("wall"))
	m.key(chatPress("i"))
	for _, r := range "q/?猫😀" {
		m.key(chatPress(string(r)))
	}
	p := &m.community.rooms.private
	if !p.wallForm || p.wallInput != "q/?猫😀" || m.help || m.community.chats.composing {
		t.Fatal("wall input escaped into global/chat keys", p.wallInput)
	}
	updated, cmd := m.Update(tea.PasteMsg{Content: "\r\nline"})
	m = updated.(model)
	if cmd != nil || p.wallInput != "q/?猫😀\nline" {
		t.Fatal("paste sent or failed to preserve text", p.wallInput)
	}
	before := p.wallInput
	m.pasteCommunityPrivate("\x1b[2J")
	if p.wallInput != before || p.err == "" {
		t.Fatal("terminal control paste accepted")
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		view := m.View().Content
		if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
			t.Fatalf("wall form overflow %v: %dx%d", size, lipgloss.Width(view), lipgloss.Height(view))
		}
		if p.wallInput != before {
			t.Fatal("resize lost draft")
		}
	}
	m.width, m.height = 120, 40
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.chatDraftCount() != 1 || !m.confirmChatDraftQuit() || m.community.chats.dialog.confirm {
		t.Fatal("wall draft bypasses Cancel-default quit")
	}
	m.community.chats.dialog = nil
	summary := m.community.summary
	summary.Session++
	m.applyCommunitySummary(communitySummaryMsg{request: m.community.summaryRequest, summary: summary})
	if p.wallDrafts[m.wallDraftKey()].text != before {
		t.Fatal("reconnect lost draft")
	}
	p.wallReady = true
	m.key(chatPress("i"))
	if p.wallInput != before {
		t.Fatal("reopening wall lost draft")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	summary.Account = "other account"
	summary.Session++
	m.applyCommunitySummary(communitySummaryMsg{request: m.community.summaryRequest, summary: summary})
	if p.wallForm || p.wallInput != "" || len(p.wallDrafts) != 1 {
		t.Fatal("account switch exposed or destroyed private draft")
	}
}

func TestCommunityPrivateRoomsRolesConfirmationAndVisibility(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openCommunityRoom("oto test"))
	r, p := &m.community.rooms, &m.community.rooms.private
	m.community.summary.Connected = true
	r.active = daemon.CommunityRoom{Name: r.selected, Private: true, Role: "owner", RoleFresh: true, Owner: "local", Joined: true}
	r.members = []daemon.CommunityRoomMember{{CommunityUser: daemon.CommunityUser{Username: "Alice"}, Role: "member"}, {CommunityUser: daemon.CommunityUser{Username: "Bob"}, Role: "operator"}}
	r.membersFresh = true
	m.key(chatPress("M"))
	if p.view != "roles" || m.communityTranscriptSelected() || m.chatTranscriptVisible() {
		t.Fatal("roles marks hidden history read")
	}
	m.key(chatPress("a"))
	for _, ch := range "q/?" {
		m.key(chatPress(string(ch)))
	}
	if p.roleInput != "q/?" || m.help {
		t.Fatal("role form intercepted literal keys")
	}
	p.roleInput = "Bob"
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if p.dialog == nil || p.dialog.confirm || !strings.Contains(p.dialog.label, "Bob") {
		t.Fatal("missing exact-user Cancel-default confirmation")
	}
	if cmd := m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); cmd != nil || p.dialog != nil || p.roleForm == "" {
		t.Fatal("Cancel submitted role action or lost form")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m.key(chatPress("o"))
	if p.dialog == nil || m.statusMenu {
		t.Fatal("grant operator opened global presence menu")
	}
	p.dialog = nil
	r.active.Role = "operator"
	if m.roleActionAllowed(soulseek.RoomAddOperator) || !m.roleActionAllowed(soulseek.RoomAddMember) {
		t.Fatal("operator UI permission matrix")
	}
	r.active.Role = "member"
	if m.roleActionAllowed(soulseek.RoomAddMember) || !m.roleActionAllowed(soulseek.RoomCancelMembership) {
		t.Fatal("member UI permission matrix")
	}
	r.active.RoleFresh = false
	if m.roleActionAllowed(soulseek.RoomCancelMembership) {
		t.Fatal("stale role authorized action")
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		for _, view := range []string{"roles", "wall"} {
			p.view = view
			screen := m.View().Content
			if lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height {
				t.Fatalf("%s overflow %v", view, size)
			}
		}
	}
	m.community.summary.Capabilities = []string{"public-rooms"}
	p.view = ""
	m.key(chatPress("M"))
	m.key(chatPress("W"))
	m.key(chatPress("I"))
	if p.view != "" || p.dialog != nil {
		t.Fatal("private controls available without capability")
	}
}

func TestCommunityPrivateRoomsStaleResourcesAndDraftSafeResults(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openCommunityRoom("oto test"))
	r, p := &m.community.rooms, &m.community.rooms.private
	id := m.community.summary.CommunityIdentity
	p.view = "wall"
	p.wallRequest = 20
	r.active = daemon.CommunityRoom{Name: r.selected, Private: true, Role: "revoked"}
	r.activeRevision = 100
	stale := daemon.CommunityRoom{Name: r.selected, Private: true, Role: "owner", RoleFresh: true, Joined: true}
	m.applyCommunityWall(roomWallMsg{request: 20, identity: id, room: r.selected, page: daemon.CommunityRoomWallPage{CommunityIdentity: id, Revision: 90, Room: stale}})
	m.applyRoomPage(roomPageMsg{request: r.request, identity: id, page: daemon.CommunityRoomsPage{CommunityIdentity: id, Revision: 91, Rooms: []daemon.CommunityRoom{stale}}})
	m.applyRoomMembers(roomMembersMsg{request: r.membersRequest, identity: id, room: r.selected, page: daemon.CommunityRoomMembersPage{CommunityIdentity: id, Revision: 92, Room: stale}})
	if r.active.Role != "revoked" {
		t.Fatal("older cross-resource response restored revoked authority", r.active)
	}
	request, op := p.wallRequest, p.operation
	p.wallForm = true
	p.wallInput = "local draft"
	m.roomWallDraftSave()
	p.reset()
	p.view = "wall"
	m.applyCommunityWall(roomWallMsg{request: request, identity: id, room: r.selected, page: daemon.CommunityRoomWallPage{CommunityIdentity: id, Revision: 200, Room: stale}})
	m.applyPrivateRoomAction(privateRoomActionMsg{operation: op, identity: id, room: r.selected, kind: "wall"})
	if p.wallReady || p.wallDrafts[m.wallDraftKey()].text != "local draft" {
		t.Fatal("canceled response modified fresh view/draft")
	}
	p.wallForm = true
	p.wallInput = "a\nb"
	m.privateRoomFormKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if p.dialog == nil || p.dialog.confirm || p.dialog.text != "a b" {
		t.Fatal("multiline wall did not require flattened preview")
	}
	p.dialog = nil
	p.wallForm = false
	m.privateRoomKey(chatPress("C"))
	if p.dialog == nil || p.dialog.confirm {
		t.Fatal("clear not Cancel-default")
	}
	if cmd := m.privateRoomDialogKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); cmd != nil || p.wallDrafts[m.wallDraftKey()].text != "local draft" {
		t.Fatal("cancel clear submitted or destroyed draft")
	}
}

func TestCommunityPrivateRoomsReviewRegressions(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openCommunityRoom("oto test"))
	r, p := &m.community.rooms, &m.community.rooms.private
	id := m.community.summary.CommunityIdentity
	r.active = daemon.CommunityRoom{Name: r.selected, Private: true, Role: "none", Error: "Membership revoked"}
	r.activeRevision = 100
	m.applyRoomAction(roomActionMsg{operation: r.operation, identity: id, room: r.selected, action: "join", result: daemon.CommunityRoomActionResult{CommunityIdentity: id, Room: daemon.CommunityRoom{Name: r.selected, Role: "owner", RoleFresh: true}}})
	if r.active.Role != "none" || r.activeRevision != 100 {
		t.Fatal("unversioned action reply restored authority")
	}
	p.view = "roles"
	m.community.pane = 0
	r.rooms = []daemon.CommunityRoom{{Name: "oto test"}, {Name: "other room"}}
	r.members = []daemon.CommunityRoomMember{{CommunityUser: daemon.CommunityUser{Username: "Alice"}}, {CommunityUser: daemon.CommunityUser{Username: "Bob"}}}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if r.row != 1 || r.memberRow != 0 {
		t.Fatal("focused directory navigation changed hidden role selection")
	}
	m.community.pane = 2
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.community.target != "Alice" || p.roleForm != "" {
		t.Fatal("focused roster Enter operated on hidden role pane")
	}
	m.community.target = ""
	m.community.pane = 1
	p.err = strings.Repeat("long error ", 20)
	r.members[1].Username = "Selected-" + strings.Repeat("猫", 200)
	r.memberRow = 1
	view := strings.Join(m.roomRolesPane(36, 10), "\n")
	if !strings.Contains(view, ">Selected-") || lipgloss.Width(view) > 36 || lipgloss.Height(view) > 10 {
		t.Fatal("selected member hidden by wrapped header/row", view)
	}
	m.width, m.height = 40, 16
	p.dialog = &privateRoomDialog{identity: id, room: r.selected, label: strings.Repeat("long preview ", 500) + "\nTAIL-OF-PREVIEW"}
	if strings.Contains(m.privateRoomDialogView(), "TAIL-OF-PREVIEW") {
		t.Fatal("preview fixture not long enough")
	}
	m.privateRoomDialogKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	if !strings.Contains(m.privateRoomDialogView(), "TAIL-OF-PREVIEW") || p.dialog.confirm {
		t.Fatal("cannot inspect complete preview safely")
	}
	m.privateRoomDialogKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	if p.dialog.scroll != 0 {
		t.Fatal("cannot return to preview start")
	}
	p.dialog = nil
	p.wallForm = false
	p.wallInput = "unsent draft"
	m.roomWallDraftSave()
	m.applyPrivateRoomAction(privateRoomActionMsg{operation: p.operation, identity: id, room: r.selected, kind: "wall"})
	if p.wallDrafts[m.wallDraftKey()].text != "unsent draft" {
		t.Fatal("clearing server wall discarded unrelated local draft")
	}
	m.community.summary.Connected = true
	p.wall.Fresh = true
	if !strings.Contains(strings.Join(m.roomWallPane(80, 24), "\n"), "stale/unknown") {
		t.Fatal("revoked room advertised fresh wall")
	}
	r.active.Joined = true
	m.community.err = "daemon unavailable"
	if !strings.Contains(strings.Join(m.roomWallPane(80, 24), "\n"), "stale/unknown") {
		t.Fatal("unavailable daemon advertised fresh wall")
	}
}
