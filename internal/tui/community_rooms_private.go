package tui

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/charmbracelet/x/ansi"
)

type privateRoomsModel struct {
	view, roleForm, roleInput, err string
	roleCursor                     int
	dialog                         *privateRoomDialog
	busy                           bool
	operation                      uint64
	actionCancel                   context.CancelFunc
	roleRequests                   map[chatKey]daemon.CommunityRoomRoleRequest
	wall                           daemon.CommunityRoomWallPage
	wallCursor                     string
	wallBack                       []string
	wallRequest                    uint64
	wallLoading, wallReady         bool
	wallCancel                     context.CancelFunc
	wallScroll                     int
	wallForm                       bool
	wallInput                      string
	wallInputCursor                int
	wallDrafts                     map[chatKey]chatDraft
}
type privateRoomDialog struct {
	identity                                     daemon.CommunityIdentity
	room, username, text, label, kind, requestID string
	action                                       soulseek.RoomRoleAction
	revision                                     uint64
	enabled, confirm                             bool
	scroll                                       int
}
type roomWallMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	room     string
	page     daemon.CommunityRoomWallPage
	err      error
}
type privateRoomActionMsg struct {
	operation  uint64
	identity   daemon.CommunityIdentity
	room, kind string
	role       daemon.CommunityRoomRoleResult
	err        error
}

func (p *privateRoomsModel) cancelLoad() {
	if p.wallCancel != nil {
		p.wallCancel()
	}
	p.wallCancel, p.wallLoading = nil, false
	p.wallRequest++
}
func (p *privateRoomsModel) reset() {
	p.cancelLoad()
	if p.actionCancel != nil {
		p.actionCancel()
	}
	*p = privateRoomsModel{wallRequest: p.wallRequest, operation: p.operation + 1, wallDrafts: p.wallDrafts, roleRequests: p.roleRequests}
}
func (p privateRoomsModel) editing() bool { return p.roleForm != "" || p.wallForm }
func (p privateRoomsModel) available(c communityModel) bool {
	return c.supports("public-rooms") && c.supports("private-rooms")
}
func (m model) wallDraftKey() chatKey {
	return chatDraftKey(m.community.summary.Account, m.community.rooms.selected, "wall")
}
func (m model) privateRoomRevision() uint64 {
	return max(m.community.summary.Revision, m.community.rooms.listRevision, m.community.rooms.membersRevision, m.community.rooms.private.wall.Revision)
}

func (m *model) privateRoomOpen(view string) tea.Cmd {
	r, p := &m.community.rooms, &m.community.rooms.private
	if !p.available(m.community) || r.selected == "" || p.busy || r.busy {
		return nil
	}
	m.saveChatPosition()
	m.community.chats.composing = false
	m.community.pane = 1
	m.community.resetUser()
	m.community.target = ""
	p.view, p.roleForm, p.wallForm, p.err = view, "", false, ""
	r.feedView = false
	r.memberCursor, r.memberNext, r.memberBack, r.memberRow = "", "", nil, 0
	return tea.Batch(m.loadCommunityMembers(true), m.loadCommunityWall(true))
}
func (m *model) loadCommunityWall(force bool) tea.Cmd {
	r, p := &m.community.rooms, &m.community.rooms.private
	if m.client == nil || m.workspace != workspaceCommunity || m.community.view != 1 || !p.available(m.community) || p.view != "wall" || r.selected == "" {
		return nil
	}
	if force {
		p.cancelLoad()
	}
	if p.wallLoading || !force && p.wallReady && p.wall.Revision == m.community.summary.Revision {
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	p.wallCancel, p.wallLoading = cancel, true
	p.wallRequest++
	request, id, room, client := p.wallRequest, m.community.summary.CommunityIdentity, r.selected, m.client
	req := daemon.CommunityRoomMembersRequest{CommunityIdentity: id, Room: room, Cursor: p.wallCursor, Limit: 50}
	return func() tea.Msg {
		defer cancel()
		page, err := client.CommunityRoomWall(ctx, req)
		return roomWallMsg{request, id, room, page, err}
	}
}
func (m *model) applyCommunityWall(x roomWallMsg) tea.Cmd {
	r, p := &m.community.rooms, &m.community.rooms.private
	if x.request != p.wallRequest || x.identity != m.community.summary.CommunityIdentity || x.room != r.selected || p.view != "wall" {
		return nil
	}
	p.wallLoading, p.wallCancel = false, nil
	if x.err == nil && (x.page.CommunityIdentity != x.identity || x.page.Room.Name != x.room) {
		x.err = daemon.ErrCommunitySession
	}
	if x.err != nil {
		p.err = errText(x.err)
		return nil
	}
	if x.page.Revision < p.wall.Revision {
		return nil
	}
	p.wall, p.wallReady = x.page, true
	if x.page.Revision >= r.activeRevision {
		r.active, r.activeRevision = x.page.Room, x.page.Revision
	}
	return nil // Polling never replaces an editor or a draft.
}
func (m *model) roomWallDraftSave() {
	p := &m.community.rooms.private
	if p.wallDrafts == nil {
		p.wallDrafts = map[chatKey]chatDraft{}
	}
	p.wallDrafts[m.wallDraftKey()] = chatDraft{text: p.wallInput, cursor: p.wallInputCursor}
}
func privateRoomTextOK(text string, multiline bool) bool {
	return utf8.ValidString(text) && strings.IndexFunc(text, func(r rune) bool {
		return unicode.Is(unicode.Bidi_Control, r) || unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t'))
	}) < 0
}
func (m *model) roleActionAllowed(action soulseek.RoomRoleAction) bool {
	r := m.community.rooms
	if !r.private.available(m.community) || !m.community.summary.Connected || !r.active.Private || !r.active.RoleFresh {
		return false
	}
	switch action {
	case soulseek.RoomAddOperator, soulseek.RoomRemoveOperator, soulseek.RoomCancelOwnership:
		return r.active.Role == "owner"
	case soulseek.RoomAddMember, soulseek.RoomRemoveMember:
		return r.active.Role == "owner" || r.active.Role == "operator"
	case soulseek.RoomCancelMembership:
		return r.active.Role == "member" || r.active.Role == "operator"
	}
	return false
}
func (m *model) confirmRoomRole(action soulseek.RoomRoleAction, username string) tea.Cmd {
	r, p := &m.community.rooms, &m.community.rooms.private
	if p.busy || r.busy {
		return nil
	}
	if !m.roleActionAllowed(action) {
		p.err = "Your confirmed role does not permit this action; join/refresh if stale."
		return nil
	}
	if action != soulseek.RoomCancelMembership && action != soulseek.RoomCancelOwnership {
		if err := soulseek.ValidateUsername(username); err != nil {
			p.err = err.Error()
			return nil
		}
	}
	label := fmt.Sprintf("%s in %q", action, r.selected)
	if username != "" {
		label += fmt.Sprintf(" for exact user %q", username)
	}
	label += "? Local history is retained."
	if r.active.LastRoleAction != nil && r.active.LastRoleAction.State == "unknown" {
		label += " Previous outcome unknown: a new action may repeat it. Use r to reconcile the prior request instead."
	}
	p.dialog = &privateRoomDialog{identity: m.community.summary.CommunityIdentity, room: r.selected, username: username, kind: "role", action: action, requestID: rand.Text(), revision: m.privateRoomRevision(), label: label}
	return nil
}
func (m *model) sendPrivateRoomAction(d privateRoomDialog) tea.Cmd {
	r, p := &m.community.rooms, &m.community.rooms.private
	if m.client == nil || p.busy || r.busy || !p.available(m.community) {
		return nil
	}
	if d.identity != m.community.summary.CommunityIdentity || d.kind != "invitations" && d.room != r.selected {
		p.err = daemon.ErrCommunitySession.Error()
		return nil
	}
	if d.kind == "role" {
		if p.roleRequests == nil {
			p.roleRequests = map[chatKey]daemon.CommunityRoomRoleRequest{}
		}
		p.roleRequests[m.wallDraftKey()] = daemon.CommunityRoomRoleRequest{CommunityIdentity: d.identity, Room: d.room, Action: d.action, Username: d.username, RequestID: d.requestID, Revision: d.revision, Confirm: true}
	}
	p.operation++
	p.busy, p.err = true, ""
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	p.actionCancel = cancel
	op, client := p.operation, m.client
	return func() tea.Msg {
		defer cancel()
		x := privateRoomActionMsg{operation: op, identity: d.identity, room: d.room, kind: d.kind}
		switch d.kind {
		case "role":
			x.role, x.err = client.ChangeCommunityRoomRole(ctx, daemon.CommunityRoomRoleRequest{CommunityIdentity: d.identity, Room: d.room, Action: d.action, Username: d.username, RequestID: d.requestID, Revision: d.revision, Confirm: true})
		case "invitations":
			x.err = client.SetCommunityRoomInvitations(ctx, daemon.CommunityRoomInvitationsRequest{CommunityIdentity: d.identity, Revision: d.revision, Enabled: d.enabled})
		case "wall":
			x.err = client.SetCommunityRoomWall(ctx, daemon.CommunityRoomWallRequest{CommunityIdentity: d.identity, Room: d.room, Revision: d.revision, Text: d.text, Confirm: d.text == ""})
		}
		return x
	}
}
func (m *model) applyPrivateRoomAction(x privateRoomActionMsg) tea.Cmd {
	r, p := &m.community.rooms, &m.community.rooms.private
	if x.operation != p.operation || x.identity != m.community.summary.CommunityIdentity || x.kind != "invitations" && x.room != r.selected {
		return nil
	}
	p.busy, p.actionCancel = false, nil
	if x.err == nil && x.kind == "role" && (x.role.CommunityIdentity != x.identity || x.role.Room != x.room) {
		x.err = daemon.ErrCommunitySession
	}
	if x.err != nil {
		p.err = errText(x.err)
		return tea.Batch(m.loadCommunitySummary(), m.loadCommunityRooms(true), m.loadCommunityMembers(true), m.loadCommunityWall(true))
	}
	if x.kind == "role" {
		r.active.LastRoleAction = &x.role
		p.roleForm = ""
	}
	if x.kind == "wall" && p.wallForm {
		delete(p.wallDrafts, m.wallDraftKey())
		p.wallInput, p.wallInputCursor, p.wallForm = "", 0, false
	}
	return tea.Batch(m.loadCommunitySummary(), m.loadCommunityRooms(true), m.loadCommunityMembers(true), m.loadCommunityWall(true))
}
func (m *model) privateRoomDialogKey(k tea.KeyPressMsg) tea.Cmd {
	p := &m.community.rooms.private
	d := p.dialog
	if d == nil {
		return nil
	}
	switch k.String() {
	case "up", "pgup":
		d.scroll = max(0, d.scroll-max(1, m.height/2))
	case "down", "pgdown":
		d.scroll += max(1, m.height/2)
	case "home":
		d.scroll = 0
	case "end":
		d.scroll = len(d.label)
	case "esc":
		p.dialog = nil
	case "left", "right", "tab", "shift+tab":
		d.confirm = !d.confirm
	case "enter":
		if !d.confirm {
			p.dialog = nil
			return nil
		}
		if d.identity != m.community.summary.CommunityIdentity || d.kind != "invitations" && d.room != m.community.rooms.selected {
			p.err = daemon.ErrCommunitySession.Error()
			p.dialog = nil
			return nil
		}
		p.dialog = nil
		return m.sendPrivateRoomAction(*d)
	}
	return nil
}
func (m *model) privateRoomFormKey(k tea.KeyPressMsg) tea.Cmd {
	p := &m.community.rooms.private
	if p.busy {
		return nil
	}
	if k.String() == "esc" {
		p.roleForm, p.wallForm = "", false
		return nil
	}
	if k.String() == "enter" {
		if p.roleForm != "" {
			return m.confirmRoomRole(soulseek.RoomRoleAction(p.roleForm), p.roleInput)
		}
		text := strings.ReplaceAll(p.wallInput, "\n", " ")
		if _, err := soulseek.EncodeMessage(soulseek.RoomWallRequest{Room: m.community.rooms.selected, Text: text}); err != nil {
			p.err = err.Error()
			return nil
		}
		d := privateRoomDialog{kind: "wall", identity: m.community.summary.CommunityIdentity, room: m.community.rooms.selected, text: text, revision: m.privateRoomRevision()}
		if text == "" || text != p.wallInput {
			d.label = fmt.Sprintf("Set your wall in %q to %q?", d.room, text)
			p.dialog = &d
			return nil
		}
		return m.sendPrivateRoomAction(d)
	}
	if p.roleForm != "" {
		text, cursor, _ := editText(p.roleInput, p.roleCursor, k)
		if len(text) <= 1024 && privateRoomTextOK(text, false) {
			p.roleInput, p.roleCursor, p.err = text, cursor, ""
		}
	} else {
		text, cursor, _ := editText(p.wallInput, p.wallInputCursor, k)
		if len(text) <= soulseek.MaxChatBytes && privateRoomTextOK(text, true) {
			p.wallInput, p.wallInputCursor, p.err = text, cursor, ""
			m.roomWallDraftSave()
		}
	}
	return nil
}
func (m *model) pasteCommunityPrivate(text string) {
	p := &m.community.rooms.private
	if p.busy || p.dialog != nil {
		return
	}
	if p.roleForm != "" {
		value, cursor := insertText(p.roleInput, text, p.roleCursor)
		if len(value) > 1024 || !privateRoomTextOK(value, false) {
			p.err = "Invalid username paste; nothing was pasted"
			return
		}
		p.roleInput, p.roleCursor, p.err = value, cursor, ""
	} else if p.wallForm {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
		value, cursor := insertText(p.wallInput, text, p.wallInputCursor)
		if len(value) > soulseek.MaxChatBytes || !privateRoomTextOK(value, true) {
			p.err = "Invalid or oversized wall paste; nothing was pasted"
			return
		}
		p.wallInput, p.wallInputCursor, p.err = value, cursor, ""
		m.roomWallDraftSave()
	}
}
func (m *model) privateRoomKey(k tea.KeyPressMsg) tea.Cmd {
	r, p := &m.community.rooms, &m.community.rooms.private
	if p.dialog != nil {
		return m.privateRoomDialogKey(k)
	}
	if p.editing() {
		return m.privateRoomFormKey(k)
	}
	if p.busy {
		return nil
	}
	switch k.String() {
	case "esc", "left":
		p.view = ""
		p.cancelLoad()
		r.memberCursor, r.memberNext, r.memberBack = "", "", nil
		return tea.Batch(m.loadCommunityMembers(true), m.loadCommunityChats(true))
	case "r":
		if p.view == "wall" {
			return m.loadCommunityWall(true)
		}
		if req, ok := p.roleRequests[m.wallDraftKey()]; ok {
			return m.sendPrivateRoomAction(privateRoomDialog{kind: "role", identity: m.community.summary.CommunityIdentity, room: req.Room, action: req.Action, username: req.Username, requestID: req.RequestID, revision: req.Revision})
		}
		return tea.Batch(m.refreshCommunityDirectory(), m.loadCommunityMembers(true))
	}
	if p.view == "wall" {
		switch k.String() {
		case "i", "enter":
			if !p.wallReady {
				p.err = "Load the wall before editing"
				return nil
			}
			p.wallForm = true
			p.wallInput, p.wallInputCursor = p.wall.OwnText, utf8.RuneCountInString(p.wall.OwnText)
			if d, ok := p.wallDrafts[m.wallDraftKey()]; ok {
				p.wallInput, p.wallInputCursor = d.text, d.cursor
			}
			m.roomWallDraftSave()
		case "C":
			p.dialog = &privateRoomDialog{kind: "wall", room: r.selected, identity: m.community.summary.CommunityIdentity, revision: m.privateRoomRevision(), label: fmt.Sprintf("Clear only your wall in %q? Other users' tickers and history remain.", r.selected)}
		case "up", "k":
			p.wallScroll = max(0, p.wallScroll-1)
		case "down", "j":
			p.wallScroll++
		case "pgup":
			p.wallScroll = max(0, p.wallScroll-m.pageRows())
		case "pgdown":
			p.wallScroll += m.pageRows()
		case "home":
			p.wallScroll = 0
		case "p":
			if len(p.wallBack) > 0 {
				p.wallCursor = p.wallBack[len(p.wallBack)-1]
				p.wallBack = p.wallBack[:len(p.wallBack)-1]
				p.wallScroll = 0
				return m.loadCommunityWall(true)
			}
		case "n":
			if p.wall.NextCursor != "" {
				p.wallBack = append(p.wallBack, p.wallCursor)
				p.wallCursor = p.wall.NextCursor
				p.wallScroll = 0
				return m.loadCommunityWall(true)
			}
		}
		return nil
	}
	switch k.String() {
	case "up", "k":
		r.memberRow = max(0, r.memberRow-1)
	case "down", "j":
		r.memberRow = min(max(0, len(r.members)-1), r.memberRow+1)
	case "pgup":
		r.memberRow = max(0, r.memberRow-m.pageRows())
	case "pgdown":
		r.memberRow = min(max(0, len(r.members)-1), r.memberRow+m.pageRows())
	case "home":
		r.memberRow = 0
	case "end":
		r.memberRow = max(0, len(r.members)-1)
	case "p":
		if len(r.memberBack) > 0 {
			r.memberCursor = r.memberBack[len(r.memberBack)-1]
			r.memberBack = r.memberBack[:len(r.memberBack)-1]
			return m.loadCommunityMembers(true)
		}
	case "n":
		if r.memberNext != "" {
			r.memberBack = append(r.memberBack, r.memberCursor)
			r.memberCursor = r.memberNext
			return m.loadCommunityMembers(true)
		}
	case "a", "A":
		action := soulseek.RoomAddMember
		if k.String() == "A" {
			action = soulseek.RoomAddOperator
		}
		if !m.roleActionAllowed(action) {
			p.err = "Your confirmed role does not permit this action"
			return nil
		}
		p.roleForm, p.roleInput, p.roleCursor, p.err = string(action), "", 0, ""
	case "o", "O", "d":
		if r.memberRow < len(r.members) {
			action := soulseek.RoomAddOperator
			if k.String() == "O" {
				action = soulseek.RoomRemoveOperator
			}
			if k.String() == "d" {
				action = soulseek.RoomRemoveMember
			}
			return m.confirmRoomRole(action, r.members[r.memberRow].Username)
		}
	case "c":
		return m.confirmRoomRole(soulseek.RoomCancelMembership, "")
	case "C":
		return m.confirmRoomRole(soulseek.RoomCancelOwnership, "")
	case "U", "enter":
		if r.memberRow < len(r.members) {
			cmd := m.openUserInspector(r.members[r.memberRow].Username)
			if k.String() == "U" {
				m.openUserActions()
			}
			return cmd
		}
	}
	return nil
}
func (m model) roomRolesPane(width, height int) []string {
	r, p := m.community.rooms, m.community.rooms.private
	fresh := "stale/unknown"
	if r.active.RoleFresh && m.community.summary.Connected && m.community.err == "" {
		fresh = "fresh"
	}
	lines := []string{"Private roles · " + r.selected, "Role: " + r.active.Role + " (" + fresh + ") · owner: " + r.active.Owner, "a add member · A add operator", "o/O grant/revoke operator · d remove", "c/C relinquish membership/ownership", "p/n pages · r reconcile · Esc back"}
	if width < 60 {
		lines = []string{"Private roles · " + r.selected, "Role: " + r.active.Role + " (" + fresh + ")", "a/A add · o/O operator · d remove", "c/C relinquish · p/n pages", "r reconcile · Esc back"}
	}
	if r.active.Error != "" {
		lines = append(lines, "! "+browseErrorText(r.active.Error))
	}
	if !r.membersFresh {
		lines = append(lines, "Membership list unavailable/stale.")
	}
	if r.membersLoading {
		lines = append(lines, "Loading members…")
	}
	if p.busy {
		lines = append(lines, "Submitting; not yet confirmed")
	}
	if r.active.LastRoleAction != nil {
		a := r.active.LastRoleAction
		lines = append(lines, fmt.Sprintf("Last %s: %s", a.Action, a.State))
	}
	if p.err != "" {
		lines = append(lines, "! "+browseErrorText(p.err))
	}
	if r.membersErr != "" {
		lines = append(lines, "! "+browseErrorText(r.membersErr))
	}
	lines = communityPane(lines, width, max(0, height-1), 0)
	rows := max(0, height-len(lines))
	start := max(0, r.memberRow-rows+1)
	for i := start; i < min(len(r.members), start+rows); i++ {
		mark := " "
		if i == r.memberRow {
			mark = ">"
		}
		lines = append(lines, ansi.Truncate(mark+r.members[i].Username+" · "+r.members[i].Role, max(0, width), "…"))
	}
	return communityPane(lines, width, height, 0)
}
func (m model) roomWallPane(width, height int) []string {
	p := m.community.rooms.private
	fresh := "stale/unknown"
	if p.wall.Fresh && m.community.rooms.active.Joined && m.community.summary.Connected && m.community.err == "" {
		fresh = "fresh"
	}
	header := []string{"Room wall · " + fresh + " · " + p.wall.State, "i edit · C clear · p/n pages · Esc back"}
	body := []string{"Own desired text: " + p.wall.OwnText}
	if m.community.rooms.active.Error != "" {
		body = append(body, "! "+browseErrorText(m.community.rooms.active.Error))
	}
	if p.wallLoading {
		body = append(body, "Loading wall…")
	}
	if p.err != "" {
		body = append(body, "! "+browseErrorText(p.err))
	}
	if p.busy {
		body = append(body, "Saving…")
	}
	if len(p.wall.Entries) == 0 {
		body = append(body, "No wall entries.")
	}
	for _, entry := range p.wall.Entries {
		body = append(body, entry.Username+": "+strings.ReplaceAll(entry.Text, "\t", "    "))
	}
	return append(communityPane(header, width, min(2, height), 0), communityPane(body, width, max(0, height-2), p.wallScroll)...)
}
func (m model) privateRoomFormView(width, height int) []string {
	p := m.community.rooms.private
	label, value, cursor := "Edit your room wall · local draft", p.wallInput, p.wallInputCursor
	if p.roleForm != "" {
		label, value, cursor = p.roleForm+" · exact username", p.roleInput, p.roleCursor
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\n", "↵"), "\t", "⇥")
	return communityPane([]string{label, renderInputWindow(value, cursor, width), p.err, "Enter submit/preview · Esc keep wall draft · paste never submits"}, width, height, 0)
}
func (m model) privateRoomDialogView() string {
	d := m.community.rooms.private.dialog
	return communityConfirmationView(d.label, d.confirm, d.scroll, m.width, m.height)
}

func communityConfirmationView(label string, confirm bool, scroll, width, height int) string {
	return confirmationCard(label, confirm, scroll, "↑↓ preview · ←→ choose · Enter/Esc", width, height)
}

func confirmationCard(label string, confirm bool, scroll int, hint string, width, height int) string {
	width, height = max(1, width), max(1, height)
	if width >= 40 {
		width -= 4
	}
	if height >= 8 {
		height -= 2
	}
	cardWidth := max(1, min(64, width))
	bodyWidth := max(1, cardWidth-4)
	rows := max(0, height-4)
	lines := communityPane([]string{strong(label)}, bodyWidth, rows, scroll)
	for len(lines) < rows {
		lines = append(lines, "")
	}
	choices := "[Cancel] Confirm"
	if confirm {
		choices = "Cancel [Confirm]"
	}
	lines = append(lines, ansi.Truncate(accent(choices), bodyWidth, "…"), ansi.Truncate(muted(hint), bodyWidth, "…"))
	card := panelStyle().Width(cardWidth).Padding(0, 1).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(max(1, width), max(1, height), lipgloss.Center, lipgloss.Center, card)
}
