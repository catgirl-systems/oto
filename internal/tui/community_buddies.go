package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/charmbracelet/x/ansi"
)

type buddyEditor struct {
	identity                                daemon.CommunityIdentity
	original, username, note, err           string
	notify, priority, trusted, dirty, stale bool
	field, nameCursor, noteCursor           int
	base                                    daemon.CommunityBuddy
}
type buddyDialog struct {
	kind, label, username string
	identity              daemon.CommunityIdentity
	revision              uint64
	confirm               bool
	scroll                int
}
type communityBuddiesModel struct {
	buddies                        []daemon.CommunityBuddy
	row, total                     int
	cursor, next, query, sort, err string
	back                           []string
	selected                       string
	active                         *daemon.CommunityBuddy
	activeRevision                 uint64
	scroll                         int
	request, listRevision          uint64
	loading, listReady             bool
	cancel                         context.CancelFunc
	editor                         *buddyEditor
	drafts                         map[chatKey]buddyEditor
	form, input, inputErr          string
	inputCursor                    int
	dialog                         *buddyDialog
	opening, busy                  bool
	openRequest, operation         uint64
	openCancel, actionCancel       context.CancelFunc
}
type buddyPageMsg struct {
	request        uint64
	identity       daemon.CommunityIdentity
	selected       string
	page, detail   daemon.CommunityBuddiesPage
	err, detailErr error
}
type buddyOpenMsg struct {
	request            uint64
	identity           daemon.CommunityIdentity
	username, original string
	discard            bool
	page               daemon.CommunityBuddiesPage
	err                error
}
type buddyActionMsg struct {
	operation uint64
	identity  daemon.CommunityIdentity
	username  string
	key       chatKey
	editing   bool
	result    daemon.CommunityBuddyResult
	err       error
}

func (b *communityBuddiesModel) cancelLoad() {
	if b.cancel != nil {
		b.cancel()
	}
	if b.openCancel != nil {
		b.openCancel()
	}
	b.cancel, b.openCancel, b.loading, b.opening = nil, nil, false, false
	b.request++
	b.openRequest++
}
func (b *communityBuddiesModel) reset(accountChanged bool) {
	b.cancelLoad()
	if b.actionCancel != nil {
		b.actionCancel()
	}
	editor, selected := b.editor, b.selected
	if accountChanged {
		editor, selected = nil, ""
	}
	*b = communityBuddiesModel{request: b.request, openRequest: b.openRequest, operation: b.operation + 1, drafts: b.drafts, editor: editor, selected: selected}
}
func (b communityBuddiesModel) available(c communityModel) bool { return c.supports("buddies") }
func (m model) buddyDraftKey(username string) chatKey {
	return chatDraftKey(m.community.summary.Account, username, "buddy")
}
func (m *model) saveBuddyDraft() {
	b := &m.community.buddies
	e := b.editor
	if e == nil || !e.dirty {
		return
	}
	if b.drafts == nil {
		b.drafts = map[chatKey]buddyEditor{}
	}
	b.drafts[chatDraftKey(e.identity.Account, e.original, "buddy")] = *e
}
func (m *model) loadCommunityBuddies(force bool) tea.Cmd {
	b := &m.community.buddies
	if m.client == nil || m.workspace != workspaceCommunity || m.community.view != 2 || !b.available(m.community) {
		return nil
	}
	if force {
		if b.cancel != nil {
			b.cancel()
		}
		b.loading = false
		b.request++
	}
	if b.loading || !force && b.listReady && b.listRevision == m.community.summary.Revision {
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	b.cancel, b.loading = cancel, true
	b.request++
	request, id, selected, client := b.request, m.community.summary.CommunityIdentity, b.selected, m.client
	req := daemon.CommunityBuddiesRequest{CommunityIdentity: id, Query: b.query, Sort: b.sort, Cursor: b.cursor, Limit: 50}
	return func() tea.Msg {
		defer cancel()
		x := buddyPageMsg{request: request, identity: id, selected: selected}
		x.page, x.err = client.CommunityBuddies(ctx, req)
		if selected != "" {
			x.detail, x.detailErr = client.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Username: selected})
		}
		return x
	}
}
func (m *model) applyBuddyPage(x buddyPageMsg) tea.Cmd {
	b := &m.community.buddies
	if x.request != b.request || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	b.loading, b.cancel = false, nil
	if x.err == nil && x.page.CommunityIdentity != x.identity {
		x.err = daemon.ErrCommunitySession
	}
	b.err = errText(x.err)
	if x.err == nil && x.page.Revision >= b.listRevision {
		selected := ""
		if b.row < len(b.buddies) {
			selected = b.buddies[b.row].Username
		}
		b.buddies, b.next, b.total, b.listRevision, b.listReady = x.page.Buddies, x.page.NextCursor, x.page.Total, x.page.Revision, true
		b.row = max(0, min(b.row, len(b.buddies)-1))
		for i, buddy := range b.buddies {
			if buddy.Username == selected {
				b.row = i
				break
			}
		}
	}
	if x.selected != "" && x.selected == b.selected {
		if x.detailErr == nil && x.detail.CommunityIdentity != x.identity {
			x.detailErr = daemon.ErrCommunitySession
		}
		if x.detailErr != nil {
			b.err = errText(x.detailErr)
		} else if x.detail.Revision >= b.activeRevision {
			b.active, b.activeRevision = nil, x.detail.Revision
			for _, buddy := range x.detail.Buddies {
				if buddy.Username == b.selected {
					b.active = &buddy
					break
				}
			}
		}
	}
	return nil
}
func buddyMetadataEqual(a, b daemon.CommunityBuddy) bool {
	return a.Username == b.Username && a.Note == b.Note && a.NotifyOnline == b.NotifyOnline && a.Priority == b.Priority && a.Trusted == b.Trusted
}
func (m *model) newBuddyEditor() {
	b := &m.community.buddies
	if b.busy || b.opening || !b.available(m.community) {
		return
	}
	e := buddyEditor{identity: m.community.summary.CommunityIdentity}
	if d, ok := b.drafts[m.buddyDraftKey("")]; ok {
		e = d
		e.identity = m.community.summary.CommunityIdentity
	}
	b.editor, b.form = &e, ""
}
func (m *model) openBuddyEditor(username string, discard bool) tea.Cmd {
	b := &m.community.buddies
	if m.client == nil || b.busy || !b.available(m.community) {
		return nil
	}
	if err := soulseek.ValidateUsername(username); err != nil {
		b.err = err.Error()
		return nil
	}
	if b.openCancel != nil {
		b.openCancel()
	}
	b.openRequest++
	b.opening = true
	original := username
	if discard && b.editor != nil {
		original = b.editor.original
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	b.openCancel = cancel
	request, id, client := b.openRequest, m.community.summary.CommunityIdentity, m.client
	return func() tea.Msg {
		defer cancel()
		page, err := client.CommunityBuddies(ctx, daemon.CommunityBuddiesRequest{CommunityIdentity: id, Username: username})
		return buddyOpenMsg{request, id, username, original, discard, page, err}
	}
}
func (m *model) applyBuddyOpen(x buddyOpenMsg) tea.Cmd {
	b := &m.community.buddies
	if x.request != b.openRequest || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	b.opening, b.openCancel = false, nil
	if x.err == nil && x.page.CommunityIdentity != x.identity {
		x.err = daemon.ErrCommunitySession
	}
	if x.err != nil {
		b.err = errText(x.err)
		if b.editor != nil {
			b.editor.err = b.err
		}
		return nil
	}
	e := buddyEditor{identity: x.identity, original: x.username, username: x.username, nameCursor: utf8.RuneCountInString(x.username), field: 1}
	for _, buddy := range x.page.Buddies {
		if buddy.Username == x.username {
			e.base = buddy
			e.note, e.notify, e.priority, e.trusted = buddy.Note, buddy.NotifyOnline, buddy.Priority, buddy.Trusted
			e.noteCursor = utf8.RuneCountInString(e.note)
		}
	}
	if x.discard {
		delete(b.drafts, m.buddyDraftKey(x.original))
	} else if draft, ok := b.drafts[m.buddyDraftKey(x.username)]; ok {
		base := e.base
		e = draft
		e.identity = x.identity
		if buddyMetadataEqual(e.base, base) {
			e.base = base
			e.stale, e.err = false, ""
		} else {
			e.stale = true
			e.err = "Buddy changed elsewhere; Ctrl+R reloads with confirmation"
		}
	}
	b.editor, b.form, b.err = &e, "", ""
	return nil
}
func (m model) selectedBuddy() (daemon.CommunityBuddy, bool) {
	b := m.community.buddies
	if m.community.pane != 0 && b.selected != "" {
		if b.active != nil {
			return *b.active, true
		}
		return daemon.CommunityBuddy{}, false
	}
	if b.row < len(b.buddies) {
		return b.buddies[b.row], true
	}
	return daemon.CommunityBuddy{}, false
}
func (m *model) buddyEditorKey(k tea.KeyPressMsg) tea.Cmd {
	b, e := &m.community.buddies, m.community.buddies.editor
	if e == nil || b.busy || b.opening {
		return nil
	}
	switch k.String() {
	case "esc":
		m.saveBuddyDraft()
		b.editor = nil
		return nil
	case "tab", "shift+tab":
		delta := 1
		if k.String() == "shift+tab" {
			delta = 4
		}
		e.field = (e.field + delta) % 5
		if e.base.Username != "" && e.field == 0 {
			if delta == 1 {
				e.field = 1
			} else {
				e.field = 4
			}
		}
		return nil
	case "ctrl+r":
		b.dialog = &buddyDialog{kind: "reload", identity: m.community.summary.CommunityIdentity, username: e.username, label: fmt.Sprintf("Reload saved note and flags for %q and discard this local draft?", e.username)}
		return nil
	case "enter":
		return m.sendBuddy(false, nil)
	}
	if e.field >= 2 {
		if k.String() == "space" || k.String() == " " || k.String() == "left" || k.String() == "right" {
			flag := &e.notify
			if e.field == 3 {
				flag = &e.priority
			}
			if e.field == 4 {
				flag = &e.trusted
			}
			*flag = !*flag
			e.dirty = true
			m.saveBuddyDraft()
		}
		return nil
	}
	if e.field == 0 && e.base.Username != "" {
		return nil
	}
	text, cursor := e.note, e.noteCursor
	if e.field == 0 {
		text, cursor = e.username, e.nameCursor
	}
	value, pos, _ := editText(text, cursor, k)
	valid := privateRoomTextOK(value, e.field == 1) && (e.field == 1 && len(value) <= daemon.MaxCommunityBuddyNoteBytes || e.field == 0 && len(value) <= soulseek.MaxUsernameBytes)
	if !valid {
		e.err = "Text is oversized or contains terminal controls"
		return nil
	}
	e.dirty = e.dirty || value != text
	e.err = ""
	if e.field == 0 {
		e.username, e.nameCursor = value, pos
	} else {
		e.note, e.noteCursor = value, pos
	}
	m.saveBuddyDraft()
	return nil
}
func (m *model) pasteCommunityBuddy(text string) {
	b, e := &m.community.buddies, m.community.buddies.editor
	if b.busy || b.opening || b.dialog != nil {
		return
	}
	if e == nil {
		if b.form == "filter" {
			value, pos := insertText(b.input, text, b.inputCursor)
			if len(value) <= 1024 && privateRoomTextOK(value, false) {
				b.input, b.inputCursor, b.inputErr = value, pos, ""
			} else {
				b.inputErr = "Invalid filter paste"
			}
		}
		return
	}
	if e.field > 1 || e.field == 0 && e.base.Username != "" {
		return
	}
	value, pos := e.note, e.noteCursor
	if e.field == 0 {
		value, pos = e.username, e.nameCursor
	} else {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	}
	value, pos = insertText(value, text, pos)
	if !privateRoomTextOK(value, e.field == 1) || e.field == 0 && len(value) > soulseek.MaxUsernameBytes || e.field == 1 && len(value) > daemon.MaxCommunityBuddyNoteBytes {
		e.err = "Invalid or oversized paste; nothing pasted"
		return
	}
	if e.field == 0 {
		e.username, e.nameCursor = value, pos
	} else {
		e.note, e.noteCursor = value, pos
	}
	e.dirty, e.err = true, ""
	m.saveBuddyDraft()
}
func (m *model) sendBuddy(remove bool, d *buddyDialog) tea.Cmd {
	b := &m.community.buddies
	if b.busy || b.opening || m.client == nil || !b.available(m.community) {
		return nil
	}
	id := m.community.summary.CommunityIdentity
	req := daemon.CommunityBuddyRequest{CommunityIdentity: id, Remove: remove, Confirm: remove}
	key := m.buddyDraftKey("")
	if remove {
		if d.identity != id {
			b.err = daemon.ErrCommunitySession.Error()
			return nil
		}
		req.Username, req.Revision = d.username, &d.revision
	} else {
		e := b.editor
		if e == nil {
			return nil
		}
		if e.stale || e.identity != id {
			e.err = "Buddy/account changed; Ctrl+R reloads saved metadata"
			return nil
		}
		if err := soulseek.ValidateUsername(e.username); err != nil {
			e.err = err.Error()
			return nil
		}
		req.Username, req.Note, req.NotifyOnline, req.Priority, req.Trusted = e.username, e.note, e.notify, e.priority, e.trusted
		if e.base.Username != "" {
			revision := e.base.Revision
			req.Revision = &revision
		}
		key = m.buddyDraftKey(e.original)
		m.saveBuddyDraft()
	}
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	b.actionCancel = cancel
	b.operation++
	b.busy, b.err = true, ""
	operation, client := b.operation, m.client
	return func() tea.Msg {
		defer cancel()
		result, err := client.SetCommunityBuddy(ctx, req)
		return buddyActionMsg{operation, id, req.Username, key, !remove, result, err}
	}
}
func (m *model) applyBuddyAction(x buddyActionMsg) tea.Cmd {
	b := &m.community.buddies
	if x.operation != b.operation || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	b.busy, b.actionCancel = false, nil
	if x.err == nil && (x.result.CommunityIdentity != x.identity || x.result.Removed == x.editing || !x.result.Removed && x.result.Buddy.Username != x.username) {
		x.err = daemon.ErrCommunitySession
	}
	if x.err != nil {
		b.err = errText(x.err)
		if b.editor != nil {
			b.editor.err = b.err + " · Ctrl+R reload"
		}
		return nil
	}
	if x.editing {
		delete(b.drafts, x.key)
		b.editor = nil
	}
	if x.result.Removed {
		m.setNotice("Buddy removed; history and local drafts retained")
	} else {
		// Mutation replies have no resource revision: refresh rather than replace
		// metadata that may already include a newer frontend's edit.
		if b.selected != x.username {
			b.active = nil
			b.activeRevision = 0
		}
		b.selected = x.username
		m.community.pane = 1
		m.setNotice("Buddy saved")
	}
	return tea.Batch(m.loadCommunitySummary(), m.loadCommunityBuddies(true))
}
func (m *model) buddyDialogKey(k tea.KeyPressMsg) tea.Cmd {
	b, d := &m.community.buddies, m.community.buddies.dialog
	if d == nil {
		return nil
	}
	switch k.String() {
	case "esc":
		b.dialog = nil
	case "left", "right", "tab", "shift+tab":
		d.confirm = !d.confirm
	case "up", "pgup":
		d.scroll = max(0, d.scroll-max(1, m.height/2))
	case "down", "pgdown":
		d.scroll += max(1, m.height/2)
	case "home":
		d.scroll = 0
	case "end":
		d.scroll = len(d.label)
	case "enter":
		b.dialog = nil
		if !d.confirm {
			return nil
		}
		if d.identity != m.community.summary.CommunityIdentity {
			b.err = daemon.ErrCommunitySession.Error()
			return nil
		}
		if d.kind == "reload" {
			return m.openBuddyEditor(d.username, true)
		}
		return m.sendBuddy(true, d)
	}
	return nil
}
func (m *model) buddyKey(k tea.KeyPressMsg) tea.Cmd {
	b := &m.community.buddies
	if b.editor != nil {
		return m.buddyEditorKey(k)
	}
	if b.form != "" {
		switch k.String() {
		case "esc":
			b.form = ""
		case "enter":
			b.query, b.cursor, b.back, b.row, b.form = b.input, "", nil, 0, ""
			return m.loadCommunityBuddies(true)
		default:
			value, pos, _ := editText(b.input, b.inputCursor, k)
			if len(value) <= 1024 && privateRoomTextOK(value, false) {
				b.input, b.inputCursor = value, pos
			}
		}
		return nil
	}
	if !b.available(m.community) {
		if k.String() == "r" {
			return m.loadCommunitySummary()
		}
		return nil
	}
	if b.busy || b.opening {
		return nil
	}
	switch k.String() {
	case "esc", "left":
		m.community.pane = max(0, m.community.pane-1)
	case "a", "N":
		m.newBuddyEditor()
	case "e":
		if buddy, ok := m.selectedBuddy(); ok {
			return m.openBuddyEditor(buddy.Username, false)
		}
	case "D":
		if buddy, ok := m.selectedBuddy(); ok {
			b.dialog = &buddyDialog{kind: "remove", identity: m.community.summary.CommunityIdentity, username: buddy.Username, revision: buddy.Revision, label: fmt.Sprintf("Remove exact buddy %q and its saved note/flags? History and local drafts remain.", buddy.Username)}
		}
	case "U":
		m.openUserActions()
	case "enter", "right":
		if m.community.pane == 0 {
			if buddy, ok := m.selectedBuddy(); ok {
				b.selected, b.active, b.scroll = buddy.Username, &buddy, 0
				m.community.pane = 1
				return m.loadCommunityBuddies(true)
			}
		} else if b.selected != "" {
			return m.openUserInspector(b.selected)
		}
	case "up", "k":
		if m.community.pane == 0 {
			b.row = max(0, b.row-1)
		} else {
			b.scroll = max(0, b.scroll-1)
		}
	case "down", "j":
		if m.community.pane == 0 {
			b.row = min(max(0, len(b.buddies)-1), b.row+1)
		} else {
			b.scroll++
		}
	case "pgup":
		if m.community.pane == 0 {
			b.row = max(0, b.row-m.pageRows())
		} else {
			b.scroll = max(0, b.scroll-m.pageRows())
		}
	case "pgdown":
		if m.community.pane == 0 {
			b.row = min(max(0, len(b.buddies)-1), b.row+m.pageRows())
		} else {
			b.scroll += m.pageRows()
		}
	case "home":
		if m.community.pane == 0 {
			b.row = 0
		} else {
			b.scroll = 0
		}
	case "end":
		if m.community.pane == 0 {
			b.row = max(0, len(b.buddies)-1)
		} else {
			b.scroll = daemon.MaxCommunityBuddyNoteBytes
		}
	case "r":
		return tea.Batch(m.loadCommunitySummary(), m.loadCommunityBuddies(true))
	case "f", "/":
		b.form, b.input, b.inputCursor = "filter", b.query, utf8.RuneCountInString(b.query)
	case "s":
		sorts := []string{"username", "status", "country", "last_seen", "note", "priority", "trusted", "notify"}
		if b.sort == "" {
			b.sort = "username"
		}
		for i, order := range sorts {
			if b.sort == order {
				b.sort = sorts[(i+1)%len(sorts)]
				break
			}
		}
		b.cursor, b.back, b.row = "", nil, 0
		return m.loadCommunityBuddies(true)
	case "p":
		if len(b.back) > 0 {
			b.cursor = b.back[len(b.back)-1]
			b.back = b.back[:len(b.back)-1]
			b.row = 0
			return m.loadCommunityBuddies(true)
		}
	case "n":
		if b.next != "" {
			b.back = append(b.back, b.cursor)
			b.cursor = b.next
			b.row = 0
			return m.loadCommunityBuddies(true)
		}
	}
	return nil
}
func buddyStatus(b daemon.CommunityBuddy, connected bool) string {
	if !connected || !b.StatusFresh {
		return "unknown/stale"
	}
	if !b.Exists {
		return "not found"
	}
	switch b.Status {
	case soulseek.UserStatusOnline:
		return "online"
	case soulseek.UserStatusAway:
		return "away"
	default:
		return "offline"
	}
}
func (m model) buddyListPane(width, height int) []string {
	b := m.community.buddies
	order := b.sort
	if order == "" {
		order = "username"
	}
	lines := []string{fmt.Sprintf("Buddies (%d) · %s", b.total, order), "a add · e edit · D remove", "s sort · f filter · U actions"}
	if b.query != "" {
		lines = append(lines, "Find: "+b.query)
	}
	if b.err != "" {
		lines = append(lines, "! "+browseErrorText(b.err))
	}
	if b.loading && !b.listReady {
		lines = append(lines, "Loading buddies…")
	}
	if len(b.buddies) == 0 {
		lines = append(lines, "No matching buddies.")
	}
	lines = communityPane(lines, width, max(0, height-2), 0)
	rows := max(0, height-len(lines)-1)
	start := max(0, b.row-rows+1)
	for i := start; i < min(len(b.buddies), start+rows); i++ {
		mark := " "
		if i == b.row {
			mark = ">"
		}
		buddy := b.buddies[i]
		lines = append(lines, ansi.Truncate(mark+buddy.Username+" · "+buddyStatus(buddy, m.community.summary.Connected && m.community.err == "" && b.err == ""), max(0, width), "…"))
	}
	if height > 0 {
		lines = append(lines, ansi.Truncate("p/n pages · Enter detail", max(0, width), "…"))
	}
	return lines
}
func (m model) buddyDetailPane(width, height int) []string {
	b := m.community.buddies
	if b.active == nil {
		return communityPane([]string{"Select a buddy and Enter.", b.selected, "a add · e edit · U actions", b.err}, width, height, 0)
	}
	buddy := b.active
	live := m.community.summary.Connected && m.community.err == "" && b.err == ""
	country := buddy.Country
	if country == "" {
		country = "unknown"
	}
	seen := "never observed offline"
	if !buddy.LastSeen.IsZero() {
		seen = buddy.LastSeen.Local().Format(time.RFC3339)
	}
	if country != "unknown" && (!live || !buddy.StatusFresh) {
		country += " (stale)"
	}
	lines := []string{"Buddy: " + buddy.Username, "Status: " + buddyStatus(*buddy, live), "Country: " + country, "Last seen (observed offline): " + seen, fmt.Sprintf("Notify online: %t", buddy.NotifyOnline), fmt.Sprintf("Priority preference: %t", buddy.Priority), fmt.Sprintf("Trusted preference: %t", buddy.Trusted), "Priority: preferred upload class; running files continue.", "Trust: trusted roots (not self); bans still apply.", "Note:", strings.ReplaceAll(buddy.Note, "\t", "    "), b.err}
	lines = communityPane(lines, width, max(0, height-1), b.scroll)
	if height > 0 {
		lines = append(lines, ansi.Truncate("↑↓ scroll · e edit · D remove · U actions", max(0, width), "…"))
	}
	return lines
}
func (m model) buddyEditorView(width, height int) []string {
	b, e := m.community.buddies, m.community.buddies.editor
	if e == nil {
		return communityPane([]string{"Find buddies by username/note", renderInputWindow(b.input, b.inputCursor, width), b.inputErr, "Enter filter · Esc back"}, width, height, 0)
	}
	labels := []string{"Exact username", "Note", "Notify online", "Priority preference", "Trusted preference"}
	values := []string{e.username, strings.ReplaceAll(strings.ReplaceAll(e.note, "\n", "↵"), "\t", "⇥"), fmt.Sprint(e.notify), fmt.Sprint(e.priority), fmt.Sprint(e.trusted)}
	cursor := e.noteCursor
	if e.field == 0 {
		cursor = e.nameCursor
	}
	if width < 60 || height < 14 {
		return communityPane([]string{"Buddy editor · " + labels[e.field], renderInputWindow(values[e.field], cursor, width), e.err, "Tab fields · Space toggle", "Enter save · Esc keep · Ctrl+R reload"}, width, height, 0)
	}
	lines := []string{"Buddy editor"}
	for i, label := range labels {
		mark := " "
		value := ansi.Truncate(values[i], max(1, width-24), "…")
		if i == e.field {
			mark = ">"
			if i < 2 {
				value = renderInputWindow(values[i], cursor, max(1, width-24))
			}
		}
		lines = append(lines, mark+label+": "+value)
	}
	if b.busy || b.opening {
		lines = append(lines, "Working…")
	}
	lines = append(lines, e.err, "Tab fields · Space toggle · Enter save", "Esc keep draft · Ctrl+R reload · paste never submits")
	return communityPane(lines, width, height, 0)
}
func (m model) buddyDialogView() string {
	d := m.community.buddies.dialog
	return communityConfirmationView(d.label, d.confirm, d.scroll, m.width, m.height)
}
func (m *model) applyBuddyNotification(next daemon.CommunitySummary) {
	old := m.community.summary
	if !m.community.ready || old.CommunityIdentity != next.CommunityIdentity || next.BuddyNotification.Sequence <= old.BuddyNotification.Sequence {
		return
	}
	count := next.BuddyNotification.Sequence - old.BuddyNotification.Sequence
	message := next.BuddyNotification.Message
	if count > 1 {
		message = fmt.Sprintf("%d buddy online transitions; latest: %s", count, message)
	}
	m.setNotice(message)
}
