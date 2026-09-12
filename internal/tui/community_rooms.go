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
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/charmbracelet/x/ansi"
)

type communityRoomsModel struct {
	rooms              []daemon.CommunityRoom
	selected           string // Open room, independent of the directory cursor.
	active             daemon.CommunityRoom
	row                int
	cursor, next       string
	back               []string
	query, mode        string
	listReady, loading bool
	listRevision       uint64
	listErr            string
	request            uint64
	cancel             context.CancelFunc

	form, input, inputErr string
	inputCursor           int
	remember              bool
	dialog                *roomDialog
	busy                  bool
	operation             uint64
	actionCancel          context.CancelFunc
	actionErr             string

	members                         []daemon.CommunityRoomMember
	memberRow                       int
	memberCursor, memberNext        string
	memberBack                      []string
	membersReady, membersLoading    bool
	membersRevision, membersRequest uint64
	membersCancel                   context.CancelFunc
	membersErr                      string

	feed                                                      []daemon.CommunityFeedMessage
	feedCursor, feedNext                                      int64
	feedBack                                                  []int64
	feedReady, feedLoading, feedWanted, feedWritten, feedView bool
	feedRequest, feedRevision                                 uint64
	feedCancel                                                context.CancelFunc
	feedErr                                                   string
	feedScroll                                                int
}

type roomDialog struct {
	action, room, label string
	identity            daemon.CommunityIdentity
	confirm             bool
}
type roomPageMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	page     daemon.CommunityRoomsPage
	err      error
}
type roomActionMsg struct {
	operation    uint64
	identity     daemon.CommunityIdentity
	action, room string
	result       daemon.CommunityRoomActionResult
	err          error
}
type roomMembersMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	room     string
	page     daemon.CommunityRoomMembersPage
	err      error
}
type roomFeedMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	page     daemon.CommunityFeedPage
	err      error
}
type roomFeedActionMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	enabled  bool
	err      error
}

func (r *communityRoomsModel) cancelLoads() {
	for _, cancel := range []context.CancelFunc{r.cancel, r.membersCancel, r.feedCancel} {
		if cancel != nil {
			cancel()
		}
	}
	r.cancel, r.membersCancel, r.feedCancel = nil, nil, nil
	r.request++
	r.membersRequest++
	r.feedRequest++
	r.loading, r.membersLoading, r.feedLoading = false, false, false
}
func (r *communityRoomsModel) reset() {
	r.cancelLoads()
	if r.actionCancel != nil {
		r.actionCancel()
	}
	*r = communityRoomsModel{request: r.request, membersRequest: r.membersRequest, feedRequest: r.feedRequest, operation: r.operation + 1}
}
func (r communityRoomsModel) available(c communityModel) bool { return c.supports("public-rooms") }
func (r communityRoomsModel) selectedRoom() (daemon.CommunityRoom, bool) {
	if r.active.Name == r.selected && r.selected != "" {
		return r.active, true
	}
	return daemon.CommunityRoom{}, false
}
func (r communityRoomsModel) selectedRoomJoined() bool {
	room, ok := r.selectedRoom()
	return ok && room.Joined && room.State == "joined"
}

func (m *model) loadCommunityRooms(force bool) tea.Cmd {
	r := &m.community.rooms
	if m.client == nil || m.workspace != workspaceCommunity || m.community.view != 1 || !r.available(m.community) {
		return nil
	}
	if force {
		if r.cancel != nil {
			r.cancel()
		}
		r.request++
		r.loading = false
	}
	if r.loading || !force && r.listReady && r.listRevision == m.community.summary.Revision {
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	r.cancel, r.loading = cancel, true
	r.request++
	request, identity, client := r.request, m.community.summary.CommunityIdentity, m.client
	req := daemon.CommunityRoomsRequest{CommunityIdentity: identity, Cursor: r.cursor, Query: r.query, Mode: r.mode, Limit: 50}
	return func() tea.Msg {
		defer cancel()
		page, err := client.CommunityRooms(ctx, req)
		return roomPageMsg{request, identity, page, err}
	}
}
func (m *model) applyRoomPage(x roomPageMsg) tea.Cmd {
	r := &m.community.rooms
	if x.request != r.request || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	r.loading, r.cancel = false, nil
	if x.err == nil && x.page.CommunityIdentity != x.identity {
		x.err = daemon.ErrCommunitySession
	}
	r.listErr = errText(x.err)
	if x.err != nil || x.page.Revision < r.listRevision {
		return nil
	}
	selected := ""
	if r.row < len(r.rooms) {
		selected = r.rooms[r.row].Name
	}
	r.rooms, r.next, r.listRevision, r.listReady = x.page.Rooms, x.page.NextCursor, x.page.Revision, true
	r.row = max(0, min(r.row, len(r.rooms)-1))
	for i, room := range r.rooms {
		if room.Name == selected {
			r.row = i
		}
		if room.Name == r.selected {
			r.active = room
		}
	}
	return m.loadCommunityMembers(false)
}
func (m *model) refreshCommunityDirectory() tea.Cmd {
	r := &m.community.rooms
	if m.client == nil || r.busy {
		return nil
	}
	r.operation++
	r.busy = true
	operation, identity, client := r.operation, m.community.summary.CommunityIdentity, m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		err := client.RefreshCommunityRooms(ctx, identity)
		return roomActionMsg{operation: operation, identity: identity, action: "refresh", err: err}
	}
}
func (m *model) openCommunityRoom(name string) tea.Cmd {
	r, c := &m.community.rooms, &m.community.chats
	if err := soulseek.ValidateRoomName(name); err != nil {
		r.actionErr = err.Error()
		return nil
	}
	if m.client == nil || !r.available(m.community) || c.busy {
		return nil
	}
	m.saveChatPosition()
	c.cancelLoad()
	r.cancelLoads()
	m.switchWorkspace(workspaceCommunity)
	m.community.view, m.community.pane = 1, 1
	r.selected, r.feedView = name, false
	r.active = daemon.CommunityRoom{Name: name, State: "loading"}
	for _, room := range r.rooms {
		if room.Name == name {
			r.active = room
		}
	}
	m.community.resetUser()
	m.community.target = ""
	c.conversation = daemon.CommunityConversation{Kind: "room", Target: name}
	position, ok := c.positions[m.chatKey()]
	if !ok {
		position.follow = true
	}
	c.position, c.composing = position, false
	c.messages, c.historyReady, c.historyErr, c.form = nil, false, "", ""
	c.unreadThrough, c.historyRevision = 0, 0
	r.members, r.memberRow, r.membersReady = nil, 0, false
	r.memberCursor, r.memberNext, r.memberBack = "", "", nil
	ctx, cancel, op := m.beginChatOperation()
	identity, client := m.community.summary.CommunityIdentity, m.client
	return func() tea.Msg {
		defer cancel()
		conversation, err := client.OpenCommunityConversation(ctx, daemon.CommunityOpenConversationRequest{CommunityIdentity: identity, Room: name})
		return chatOperationMsg{operation: op, identity: identity, kind: "open", conversation: conversation, err: err}
	}
}
func (m *model) roomAction(action, name string, remember bool) tea.Cmd {
	r := &m.community.rooms
	if m.client == nil || r.busy {
		return nil
	}
	if err := soulseek.ValidateRoomName(name); err != nil {
		r.actionErr = err.Error()
		return nil
	}
	if action == "leave" || action == "forget" {
		label := fmt.Sprintf("Leave %q now? History and autojoin preference remain.", name)
		if action == "forget" {
			label = fmt.Sprintf("Forget autojoin for %q? Current membership and history remain.", name)
		}
		r.dialog = &roomDialog{action: action, room: name, identity: m.community.summary.CommunityIdentity, label: label}
		return nil
	}
	return m.sendRoomAction(action, name, remember)
}
func (m *model) sendRoomAction(action, name string, remember bool) tea.Cmd {
	r := &m.community.rooms
	if r.busy || m.client == nil {
		return nil
	}
	r.dialog, r.actionErr = nil, ""
	r.busy, r.operation = true, r.operation+1
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	r.actionCancel = cancel
	operation, identity, client := r.operation, m.community.summary.CommunityIdentity, m.client
	req := daemon.CommunityRoomActionRequest{CommunityIdentity: identity, Room: name, Action: action, Remember: remember, Revision: m.community.summary.Revision, RequestID: rand.Text()}
	return func() tea.Msg {
		defer cancel()
		result, err := client.CommunityRoomAction(ctx, req)
		return roomActionMsg{operation, identity, action, name, result, err}
	}
}
func (m *model) applyRoomAction(x roomActionMsg) tea.Cmd {
	r := &m.community.rooms
	if x.operation != r.operation || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	r.busy, r.actionCancel = false, nil
	if x.err == nil && x.action != "refresh" && x.result.CommunityIdentity != x.identity {
		x.err = daemon.ErrCommunitySession
	}
	r.actionErr = errText(x.err)
	if x.err != nil {
		return nil
	}
	r.form = ""
	if x.room == r.selected {
		r.active = x.result.Room
	}
	m.setNotice("Room " + x.room + ": " + x.action + " requested; membership is confirmed by the server")
	return tea.Batch(m.loadCommunitySummary(), m.loadCommunityRooms(true), m.loadCommunityMembers(true))
}
func (m *model) loadCommunityMembers(force bool) tea.Cmd {
	r := &m.community.rooms
	if m.client == nil || m.workspace != workspaceCommunity || m.community.view != 1 || r.selected == "" || !r.available(m.community) {
		return nil
	}
	if force {
		if r.membersCancel != nil {
			r.membersCancel()
		}
		r.membersRequest++
		r.membersLoading = false
	}
	if r.membersLoading || !force && r.membersReady && r.membersRevision == m.community.summary.Revision {
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	r.membersCancel, r.membersLoading = cancel, true
	r.membersRequest++
	request, identity, name, client := r.membersRequest, m.community.summary.CommunityIdentity, r.selected, m.client
	req := daemon.CommunityRoomMembersRequest{CommunityIdentity: identity, Room: name, Cursor: r.memberCursor, Limit: 50}
	return func() tea.Msg {
		defer cancel()
		page, err := client.CommunityRoomMembers(ctx, req)
		return roomMembersMsg{request, identity, name, page, err}
	}
}
func (m *model) applyRoomMembers(x roomMembersMsg) tea.Cmd {
	r := &m.community.rooms
	if x.request != r.membersRequest || x.identity != m.community.summary.CommunityIdentity || x.room != r.selected {
		return nil
	}
	r.membersLoading, r.membersCancel = false, nil
	if x.err == nil && (x.page.CommunityIdentity != x.identity || x.page.Room.Name != x.room) {
		x.err = daemon.ErrCommunitySession
	}
	r.membersErr = errText(x.err)
	if x.err != nil || x.page.Revision < r.membersRevision {
		return nil
	}
	selected := ""
	if r.memberRow < len(r.members) {
		selected = r.members[r.memberRow].Username
	}
	r.members, r.memberNext, r.membersRevision, r.membersReady, r.active = x.page.Members, x.page.NextCursor, x.page.Revision, true, x.page.Room
	r.memberRow = max(0, min(r.memberRow, len(r.members)-1))
	for i, member := range r.members {
		if member.Username == selected {
			r.memberRow = i
		}
	}
	return nil
}
func (m *model) loadCommunityFeed(force bool) tea.Cmd {
	r := &m.community.rooms
	if m.client == nil || m.workspace != workspaceCommunity || m.community.view != 1 || !r.feedView || !r.available(m.community) {
		return nil
	}
	if force {
		if r.feedCancel != nil {
			r.feedCancel()
		}
		r.feedRequest++
		r.feedLoading = false
	}
	if r.feedLoading || !force && r.feedReady && r.feedRevision == m.community.summary.Revision {
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	r.feedCancel, r.feedLoading = cancel, true
	r.feedRequest++
	request, identity, client := r.feedRequest, m.community.summary.CommunityIdentity, m.client
	req := daemon.CommunityFeedRequest{CommunityIdentity: identity, Cursor: r.feedCursor, Limit: 100}
	return func() tea.Msg {
		defer cancel()
		page, err := client.CommunityFeed(ctx, req)
		return roomFeedMsg{request, identity, page, err}
	}
}
func (m *model) applyRoomFeed(x roomFeedMsg) tea.Cmd {
	r := &m.community.rooms
	if x.request != r.feedRequest || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	r.feedLoading, r.feedCancel = false, nil
	if x.err == nil && x.page.CommunityIdentity != x.identity {
		x.err = daemon.ErrCommunitySession
	}
	r.feedErr = errText(x.err)
	if x.err != nil {
		return nil
	}
	r.feed, r.feedNext, r.feedWanted, r.feedWritten, r.feedReady = x.page.Messages, x.page.NextCursor, x.page.Requested, x.page.RequestWritten, true
	r.feedRevision = x.page.Revision
	return nil
}
func (m *model) setCommunityFeed(enabled bool) tea.Cmd {
	r := &m.community.rooms
	if r.busy || m.client == nil {
		return nil
	}
	r.operation++
	r.busy = true
	request, identity, client := r.operation, m.community.summary.CommunityIdentity, m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		err := client.SetCommunityFeed(ctx, daemon.CommunityFeedSubscription{CommunityIdentity: identity, Enabled: enabled})
		return roomFeedActionMsg{request, identity, enabled, err}
	}
}
func (m *model) applyRoomFeedAction(x roomFeedActionMsg) tea.Cmd {
	r := &m.community.rooms
	if x.request != r.operation || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	r.busy, r.actionErr = false, errText(x.err)
	if x.err != nil {
		return nil
	}
	r.feedWanted = x.enabled
	return m.loadCommunityFeed(true)
}
func (m *model) roomFormKey(k tea.KeyPressMsg) tea.Cmd {
	r := &m.community.rooms
	if r.busy {
		return nil
	}
	switch k.String() {
	case "esc":
		r.form = ""
	case "tab", "shift+tab":
		if r.form == "join" {
			r.remember = !r.remember
		}
	case "enter":
		if r.form == "filter" {
			r.query, r.cursor, r.next, r.back, r.row, r.form = r.input, "", "", nil, 0, ""
			return m.loadCommunityRooms(true)
		}
		if err := soulseek.ValidateRoomName(r.input); err != nil {
			r.inputErr = err.Error()
			return nil
		}
		return m.roomAction("join", r.input, r.remember)
	default:
		value, cursor, _ := editText(r.input, r.inputCursor, k)
		if len(value) <= 1024 && strings.IndexFunc(value, unicode.IsControl) < 0 {
			r.input, r.inputCursor, r.inputErr = value, cursor, ""
		}
	}
	return nil
}
func (m *model) roomDialogKey(k tea.KeyPressMsg) tea.Cmd {
	r, d := &m.community.rooms, m.community.rooms.dialog
	switch k.String() {
	case "esc":
		r.dialog = nil
	case "left", "right", "tab", "shift+tab":
		d.confirm = !d.confirm
	case "enter":
		if !d.confirm {
			r.dialog = nil
			return nil
		}
		if d.identity != m.community.summary.CommunityIdentity {
			r.actionErr = daemon.ErrCommunitySession.Error()
			return nil
		}
		return m.sendRoomAction(d.action, d.room, false)
	}
	return nil
}
func (m *model) roomKey(k tea.KeyPressMsg) tea.Cmd {
	r := &m.community.rooms
	if r.dialog != nil {
		return m.roomDialogKey(k)
	}
	if r.form != "" {
		return m.roomFormKey(k)
	}
	if r.feedView {
		switch k.String() {
		case "esc", "left":
			r.feedView = false
		case "g":
			return m.setCommunityFeed(!r.feedWanted)
		case "up", "k":
			r.feedScroll = max(0, r.feedScroll-1)
		case "down", "j":
			r.feedScroll++
		case "pgup":
			r.feedScroll = max(0, r.feedScroll-m.pageRows())
		case "pgdown":
			r.feedScroll += m.pageRows()
		case "p":
			if len(r.feedBack) > 0 {
				r.feedCursor = r.feedBack[len(r.feedBack)-1]
				r.feedBack = r.feedBack[:len(r.feedBack)-1]
				r.feedScroll = 0
				return m.loadCommunityFeed(true)
			}
		case "n":
			if r.feedNext != 0 {
				r.feedBack = append(r.feedBack, r.feedCursor)
				r.feedCursor = r.feedNext
				r.feedScroll = 0
				return m.loadCommunityFeed(true)
			}
		case "r", "home":
			r.feedCursor, r.feedBack, r.feedScroll = 0, nil, 0
			return m.loadCommunityFeed(true)
		}
		return nil
	}
	if k.String() == "G" {
		r.feedView = true
		m.community.pane = 1
		return m.loadCommunityFeed(true)
	}
	if m.community.pane == 2 {
		if m.community.target != "" {
			switch k.String() {
			case "esc", "left":
				m.community.target = ""
				m.community.resetUser()
			case "U":
				m.openUserActions()
			case "up", "k":
				m.community.inspectorScroll = max(0, m.community.inspectorScroll-1)
			case "down", "j":
				m.community.inspectorScroll = min(m.communityInspectorEnd(), m.community.inspectorScroll+1)
			case "r":
				return m.loadCommunityUser()
			}
			return nil
		}
		switch k.String() {
		case "esc", "left":
			m.community.pane = 1
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
	name := r.selected
	if m.community.pane == 0 && r.row < len(r.rooms) {
		name = r.rooms[r.row].Name
	}
	switch k.String() {
	case "J":
		return m.roomAction("join", name, false)
	case "L":
		return m.roomAction("leave", name, false)
	case "R":
		return m.roomAction("remember", name, false)
	case "F":
		return m.roomAction("forget", name, false)
	case "esc", "left":
		m.community.pane = max(0, m.community.pane-1)
		return nil
	}
	if m.community.pane == 1 {
		if k.String() == "r" && m.community.chats.conversation.ID == 0 && r.selected != "" {
			return m.openCommunityRoom(r.selected)
		}
		_, cmd := m.chatKeyPress(k)
		return cmd
	}
	switch k.String() {
	case "N":
		r.form, r.input, r.inputCursor, r.inputErr, r.remember = "join", "", 0, "", false
	case "/", "f":
		r.form, r.input, r.inputCursor, r.inputErr = "filter", r.query, utf8.RuneCountInString(r.query), ""
	case "m":
		modes := []string{"", "remembered", "joined", "history"}
		for i, mode := range modes {
			if mode == r.mode {
				r.mode = modes[(i+1)%len(modes)]
				break
			}
		}
		r.cursor, r.next, r.back, r.row = "", "", nil, 0
		return m.loadCommunityRooms(true)
	case "r":
		return tea.Batch(m.refreshCommunityDirectory(), m.loadCommunityRooms(true))
	case "enter", "right":
		if r.row < len(r.rooms) {
			return m.openCommunityRoom(r.rooms[r.row].Name)
		}
	case "up", "k":
		r.row = max(0, r.row-1)
	case "down", "j":
		r.row = min(max(0, len(r.rooms)-1), r.row+1)
	case "pgup":
		r.row = max(0, r.row-m.pageRows())
	case "pgdown":
		r.row = min(max(0, len(r.rooms)-1), r.row+m.pageRows())
	case "home":
		r.row = 0
	case "end":
		r.row = max(0, len(r.rooms)-1)
	case "p":
		if len(r.back) > 0 {
			r.cursor = r.back[len(r.back)-1]
			r.back = r.back[:len(r.back)-1]
			r.row = 0
			return m.loadCommunityRooms(true)
		}
	case "n":
		if r.next != "" {
			r.back = append(r.back, r.cursor)
			r.cursor = r.next
			r.row = 0
			return m.loadCommunityRooms(true)
		}
	}
	return nil
}
func (m *model) pasteCommunityRoom(text string) {
	r := &m.community.rooms
	if r.form == "" || r.busy {
		return
	}
	value, cursor := insertText(r.input, text, r.inputCursor)
	if len(value) > 1024 || strings.IndexFunc(value, unicode.IsControl) >= 0 || !utf8.ValidString(value) {
		r.inputErr = "Use a single line within 1024 bytes; nothing was pasted"
		return
	}
	r.input, r.inputCursor, r.inputErr = value, cursor, ""
}
func (m model) roomListPane(width, height int) []string {
	r := m.community.rooms
	mode := r.mode
	if mode == "" {
		mode = "all"
	}
	lines := []string{"N join/create · f filter", mode + " · m change mode"}
	if r.query != "" {
		lines = append(lines, ansi.Truncate("Find: "+r.query, width, "…"))
	}
	if r.listErr != "" {
		lines = append(lines, "! "+browseErrorText(r.listErr))
	}
	if r.actionErr != "" {
		lines = append(lines, "! "+browseErrorText(r.actionErr))
	}
	if r.loading && !r.listReady {
		lines = append(lines, "Loading directory…")
	}
	rows := max(0, height-len(lines)-2)
	if len(r.rooms) == 0 {
		lines = append(lines, "No matching rooms.")
	}
	start := max(0, r.row-rows+1)
	for i := start; i < min(len(r.rooms), start+rows); i++ {
		room := r.rooms[i]
		marker := " "
		if i == r.row {
			marker = ">"
		}
		pop := "?"
		if room.PopulationKnown {
			pop = fmt.Sprint(room.Population)
		}
		flags := " (" + pop + ")"
		if room.Joined {
			flags += " J"
		}
		if room.Remembered {
			flags += " A"
		}
		if room.Private {
			flags += " P"
		}
		lines = append(lines, marker+ansi.Truncate(room.Name, max(1, width-len(flags)-1), "…")+flags)
	}
	for len(lines) < height-2 {
		lines = append(lines, "")
	}
	lines = append(lines, "p/n pages · Enter open", "G public feed · r refresh")
	return lines[:min(len(lines), max(0, height))]
}
func (m model) roomRosterPane(width, height int) []string {
	if m.community.target != "" {
		return communityPane(m.community.inspectorLines(), width, height, m.community.inspectorScroll)
	}
	r := m.community.rooms
	lines := []string{"Members · U actions", "Enter inspect · p/n pages"}
	if !r.active.RosterFresh || !m.community.summary.Connected {
		lines = append(lines, "Roster is stale/unknown")
	}
	if r.membersErr != "" {
		lines = append(lines, "! "+browseErrorText(r.membersErr))
	}
	if r.membersLoading && !r.membersReady {
		lines = append(lines, "Loading roster…")
	}
	rows := max(0, height-len(lines))
	start := max(0, r.memberRow-rows+1)
	for i := start; i < min(len(r.members), start+rows); i++ {
		marker := " "
		if i == r.memberRow {
			marker = ">"
		}
		lines = append(lines, ansi.Truncate(marker+r.members[i].Username, width, "…"))
	}
	return lines[:min(len(lines), max(0, height))]
}
func (m model) roomContentPane(width, height int) []string {
	r := m.community.rooms
	if r.feedView {
		return m.roomFeedPane(width, height)
	}
	if !m.communityTranscriptSelected() {
		return communityPane([]string{"Select a room and Enter open.", "Opening history does not join.", "J join · L leave", "R remember · F forget", "G public feed (read-only)", r.actionErr}, width, height, 0)
	}
	room := r.active
	status := room.State
	if !m.community.summary.Connected {
		status = "offline; history retained"
	}
	role := "public"
	if room.Private {
		role = "private " + room.Role
	}
	label := room.Name + " · " + status + " · " + role
	if room.Remembered {
		label += " · autojoin"
	}
	err := r.actionErr
	if err == "" {
		err = room.Error
	}
	// Reuse the transcript size contract; replace header text without losing rows.
	lines := m.chatContentPane(width, height)
	if len(lines) > 0 {
		lines[0] = ansi.Truncate(label, width, "…")
	}
	if err != "" && len(lines) > 1 {
		lines[1] = ansi.Truncate("! "+browseErrorText(err), width, "…")
	}
	if !m.community.chats.composing && len(lines) > 0 {
		lines[len(lines)-1] = ansi.Truncate("i compose · J join · L leave · R remember · F forget · F6 members", width, "…")
	}
	return lines
}
func (m model) roomFeedPane(width, height int) []string {
	r := m.community.rooms
	state := "off"
	if r.feedWanted {
		state = "requested"
	}
	if r.feedWritten {
		state = "request written (no server ACK)"
	}
	lines := []string{"Public feed · read-only · " + state, "g subscribe/off · p/n pages · ↑↓ scroll · Esc back"}
	if r.feedErr != "" {
		lines = append(lines, "! "+browseErrorText(r.feedErr))
	}
	if r.actionErr != "" {
		lines = append(lines, "! "+browseErrorText(r.actionErr))
	}
	var messages []string
	for _, message := range r.feed {
		messages = append(messages, fmt.Sprintf("[%s] %s in %s: %s", message.CreatedAt.Local().Format("15:04:05"), message.Sender, message.Room, strings.ReplaceAll(message.Text, "\t", "    ")))
	}
	if len(messages) == 0 {
		messages = append(messages, "No feed messages; not logged by default.")
	}
	return append(communityPane(lines, width, min(2, height), 0), communityPane(messages, width, max(0, height-2), r.feedScroll)...)
}
func (m model) roomFormView(width, height int) []string {
	r := m.community.rooms
	label := "Join/create public room · exact name"
	if r.form == "filter" {
		label = "Filter rooms · case-insensitive"
	}
	lines := []string{label, renderInputWindow(r.input, r.inputCursor, width)}
	if r.form == "join" {
		lines = append(lines, fmt.Sprintf("Remember/autojoin: %t · Tab toggle", r.remember))
	}
	lines = append(lines, r.inputErr, r.actionErr, "Enter submit · Esc cancel · paste never submits")
	return communityPane(lines, width, height, 0)
}
func (m model) roomDialogView() string {
	d := m.community.rooms.dialog
	width, height := max(1, m.width), max(1, m.height)
	lines := communityPane([]string{d.label}, width, max(0, height-2), 0)
	for len(lines) < height-2 {
		lines = append(lines, "")
	}
	choices := "[Cancel] Confirm"
	if d.confirm {
		choices = "Cancel [Confirm]"
	}
	lines = append(lines, ansi.Truncate(choices, width, "…"), ansi.Truncate("←→ choose · Enter/Esc", width, "…"))
	return strings.Join(lines[:min(len(lines), height)], "\n")
}
