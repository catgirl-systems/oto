package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/charmbracelet/x/ansi"
)

// Community lays its views out as up to three panes (list, content,
// inspector). Each pane opens with a header naming what it shows; key hints
// live in the footer, so panes spend their rows on content.

// paneHeader titles a pane: a pill when focused, muted otherwise, then a faint
// rule out to an optional right-hand detail.
func paneHeader(title, detail string, width int, focused bool) string {
	label := " " + muted(title) + " "
	if focused {
		label = pill(title, theme.surface, theme.accent)
	}
	right := ""
	if detail != "" {
		right = " " + muted(detail)
	}
	rule := width - ansi.StringWidth(ansi.Strip(label)) - ansi.StringWidth(ansi.Strip(right)) - 1
	if rule < 1 {
		return ansi.Truncate(label+right, max(0, width), "…")
	}
	return label + " " + faint(strings.Repeat("─", rule)) + right
}

// badgedRow pads name so badges sit flush right in a listRow of width.
func badgedRow(lead []span, name span, badges []span, width int) []span {
	badgeWidth := 0
	for _, b := range badges {
		badgeWidth += ansi.StringWidth(b.text)
	}
	leadWidth := 0
	for _, s := range lead {
		leadWidth += ansi.StringWidth(s.text)
	}
	room := max(1, width-2-leadWidth-badgeWidth-1)
	name.text = searchTextColumn(ansi.Truncate(name.text, room, "…"), room)
	spans := append(append([]span{}, lead...), name, gap(1))
	return append(spans, badges...)
}

// kv is an inspector-style label / value line.
func kv(label, value string) string {
	return muted(searchTextColumn(label, 11)) + value
}

// emptyPane is a short centred-feeling explanation for a pane with nothing to
// show yet.
func emptyPane(title string, notes ...string) []string {
	lines := []string{"", "  " + strong(title)}
	for _, note := range notes {
		if note != "" {
			lines = append(lines, "  "+muted(note))
		}
	}
	return lines
}

// communityForm is the shared single-input form used by every Community view.
func communityForm(title, input string, notes []string, err, hints string, width, height int) []string {
	lines := []string{accent(title), "", accent("› ") + input}
	for _, note := range notes {
		if note != "" {
			lines = append(lines, muted(note))
		}
	}
	if err != "" {
		lines = append(lines, danger("✗ "+err))
	}
	lines = append(lines, "", dialogHints(hints, width))
	return communityPane(lines, width, height, 0)
}

// miniHeader titles a block inside a pane whose width is not known.
func miniHeader(title, detail string) string {
	header := styled(strings.ToUpper(title), fg(theme.subtext).Bold(true))
	if detail != "" {
		header += " " + faint(detail)
	}
	return header
}

func errorLine(err string) string { return danger("✗ " + browseErrorText(err)) }

// communityState is the connection indicator shown in the panel border.
func (m model) communityState() string {
	c := m.community
	switch {
	case !c.ready:
		return styled("◌", fg(theme.download)) + " " + muted("loading")
	case !c.summary.Connected:
		return warning("○") + " " + muted("offline")
	}
	return success("●") + " " + subtle("online")
}

func (m model) communityColumnSizes(width int) []int {
	c := m.community
	if m.width < 80 || m.width < 110 && c.pane == 2 {
		return []int{width}
	}
	list := min(30, max(22, width/4))
	if m.width >= 110 && (c.target != "" || c.view == 1 && c.rooms.selected != "" || c.view == 3) || c.pane == 2 {
		inspector := min(34, max(26, width/4))
		return []int{list, width - list - inspector - 6, inspector}
	}
	return []int{list, width - list - 3}
}

// communityBodyHeight is the row count below the pane headers, matching
// renderCommunity so scrolling maths agree with what is drawn.
func (m model) communityBodyHeight() int {
	height := m.height - len(m.headerLines()) - 4
	if m.community.err != "" {
		height--
	}
	return max(0, height-1)
}

func (m model) chatContentSize() (int, int) {
	width := max(10, m.width-4)
	if sizes := m.communityColumnSizes(width); len(sizes) > 1 {
		width = sizes[1]
	}
	return max(1, width), m.communityBodyHeight()
}

func (m model) chatTranscriptHeight(height int) int {
	rows := 0
	if m.community.chats.composing {
		rows += 2
	}
	if m.community.chats.err != "" {
		rows++
	}
	return max(0, height-rows)
}

func (m model) communityInspectorEnd() int {
	width := max(1, m.width-4)
	if sizes := m.communityColumnSizes(width); len(sizes) == 3 {
		width = sizes[2]
	}
	lines := communityPane(m.community.inspectorLines(), width, 1<<20, 0)
	return max(0, len(lines)-max(1, m.communityBodyHeight()))
}

// communityPaneTitle names pane i of the current view and adds a detail.
func (m model) communityPaneTitle(i int) (string, string) {
	c := m.community
	if i == 2 && !(c.view == 1 && c.target == "") {
		if c.target == "" {
			return "User", ""
		}
		return "User", browseErrorText(c.target)
	}
	switch c.view {
	case 0:
		ch := c.chats
		if i == 0 {
			detail := countLabel(len(ch.conversations), "chat")
			if ch.includeClosed {
				detail = "all history"
			}
			if ch.listQuery != "" {
				detail = "find: " + ch.listQuery
			}
			return "Chats", detail
		}
		if ch.conversation.Target == "" || !m.communityTranscriptSelected() {
			return "Conversation", ""
		}
		return ch.conversation.Target, m.chatState()
	case 1:
		r := c.rooms
		switch i {
		case 0:
			mode := r.mode
			if mode == "" {
				mode = "all"
			}
			return "Rooms", mode
		case 2:
			detail := countLabel(len(r.members), "member")
			if !r.active.RosterFresh || !c.summary.Connected {
				detail += " · stale"
			}
			return "Members", detail
		}
		switch {
		case r.feedView:
			return "Public feed", "read-only"
		case r.private.view == "wall":
			return "Wall", r.selected
		case r.private.view == "roles":
			return "Roles", r.selected
		case !m.communityTranscriptSelected():
			return "Room", ""
		}
		status := r.active.State
		if !c.summary.Connected {
			status = "offline · history kept"
		}
		role := "public"
		if r.active.Private {
			role = "private " + r.active.Role
		}
		detail := status + " · " + role
		if r.active.Remembered {
			detail += " · autojoin"
		}
		if state := m.chatState(); state != "" {
			detail = state + " · " + detail
		}
		return r.active.Name, detail
	case 2:
		b := c.buddies
		if i == 0 {
			order := b.sort
			if order == "" {
				order = "username"
			}
			return "Buddies", fmt.Sprintf("%d · by %s", b.total, order)
		}
		if b.active != nil {
			return b.active.Username, "buddy"
		}
		return "Buddy", ""
	case 3:
		if i == 0 {
			return "Discover", ""
		}
		return discoverModes[c.discover.mode].label, c.discover.target
	}
	return communityPanes[i], ""
}

// chatState is the transcript position or health shown in its pane header.
func (m model) chatState() string {
	ch := m.community.chats
	switch {
	case ch.historyErr != "":
		return "stale · r retry"
	case ch.conversation.Closed:
		return "closed elsewhere · history kept"
	case !ch.historyReady:
		return "loading…"
	case !ch.position.follow:
		return fmt.Sprintf("%d newer · end latest", ch.newerCount)
	case ch.position.query != "":
		return "find: " + ch.position.query
	case ch.conversation.Mentions > 0:
		return fmt.Sprintf("@%d mentions", ch.conversation.Mentions)
	}
	return ""
}

func (m model) renderCommunity(width, height int) string {
	c := m.community
	var lines []string
	if c.err != "" {
		lines = append(lines, ansi.Truncate(danger("✗ Community unavailable: "+browseErrorText(c.err))+muted(" · r retry or restart the daemon"), width, "…"))
	}
	remaining := max(0, height-len(lines))
	if form := m.communityFormLines(width, remaining); form != nil {
		return strings.Join(append(lines, form...), "\n")
	}
	body := max(0, remaining-1)
	fallback := func(i int) []string {
		switch i {
		case 0:
			return emptyPane("Nothing loaded", "/ inspect a user", "U user actions")
		case 1:
			return emptyPane(communityViews[c.view]+" unavailable", "This daemon does not offer it yet.", "User details still work with / or U.")
		}
		return c.inspectorLines()
	}
	pane := func(i, size int) []string {
		content := m.communityPaneContent(i, size, body)
		if content == nil {
			content = fallback(i)
		}
		scroll := 0
		if i == 2 && !(c.view == 1 && c.target == "") {
			scroll = c.inspectorScroll
		}
		title, detail := m.communityPaneTitle(i)
		return append([]string{paneHeader(title, detail, size, i == c.pane)}, communityPane(content, size, body, scroll)...)
	}
	sizes := m.communityColumnSizes(width)
	if len(sizes) == 1 {
		column := pane(c.pane, width)
		if c.pane > 0 {
			title, _ := m.communityPaneTitle(c.pane)
			column[0] = paneHeader(title, "esc back", width, true)
		}
		return strings.Join(append(lines, column...), "\n")
	}
	columns := make([][]string, len(sizes))
	for i, size := range sizes {
		columns[i] = pane(i, size)
	}
	sep := " " + borderLine("│") + " "
	for row := 0; row < remaining; row++ {
		var cells []string
		for i := range columns {
			cell := ""
			if row < len(columns[i]) {
				cell = columns[i][row]
			}
			cells = append(cells, cell+strings.Repeat(" ", max(0, sizes[i]-ansi.StringWidth(cell))))
		}
		lines = append(lines, strings.Join(cells, sep))
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

// communityFormLines renders the active Community form, or nil when none is
// open.
func (m model) communityFormLines(width, height int) []string {
	c := m.community
	switch {
	case c.peer.form:
		return m.peerPictureForm(width, height)
	case c.inspectEditing:
		return communityForm("Inspect user", renderInputWindow(c.input, c.inputCursor, max(1, width-2)), []string{"Exact Soulseek username."}, c.inputErr, "enter inspect · esc back", width, height)
	case c.chats.form != "" && (c.view == 0 || c.view == 1):
		return m.chatFormView(width, height)
	case c.rooms.form != "" && c.view == 1:
		return m.roomFormView(width, height)
	case c.rooms.private.editing() && c.view == 1:
		return m.privateRoomFormView(width, height)
	case c.view == 2 && (c.buddies.editor != nil || c.buddies.form != ""):
		return m.buddyEditorView(width, height)
	case c.view == 3 && c.discover.form != "":
		return m.discoverFormView(width, height)
	}
	return nil
}

// communityPaneContent renders one Community column for the active view, or nil
// to keep the shared fallback pane.
func (m model) communityPaneContent(i, size, height int) []string {
	c := m.community
	switch {
	case c.view == 0 && c.supports("private-chat") && i < 2:
		return m.chatPane(i, size, height)
	case c.view == 1:
		switch i {
		case 0:
			return m.roomListPane(size, height)
		case 1:
			return m.roomContentPane(size, height)
		case 2:
			return m.roomRosterPane(size, height)
		}
	case c.view == 2 && c.supports("buddies") && i < 2:
		if i == 0 {
			return m.buddyListPane(size, height)
		}
		return m.buddyDetailPane(size, height)
	case c.view == 3:
		switch i {
		case 0:
			return m.discoverSidebar(size, height)
		case 1:
			return m.discoverRowsPane(size, height)
		case 2:
			return c.inspectorLines()
		}
	}
	return nil
}

func (c communityModel) paneScroll() int {
	if c.pane == 2 && c.view != 1 {
		return c.inspectorScroll
	}
	return 0
}

func communityPane(lines []string, width, height, scroll int) []string {
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Wrap(line, max(1, width), ""), "\n")...)
	}
	start := max(0, min(scroll, len(wrapped)-height))
	out := wrapped[start:min(len(wrapped), start+max(0, height))]
	for i := range out {
		out[i] = ansi.Truncate(out[i], max(0, width), "…")
	}
	return out
}

// focusRow highlights the cursor row of a pane: the full bar when the pane
// has focus, an accent tint otherwise so the selection stays visible.
func focusRow(spans []span, width int, current, focused bool) string {
	return listRow(spans, width, current && focused, current && !focused)
}

// listWindow returns the visible slice of a list so the cursor row stays on
// screen within rows.
func listWindow(total, cursor, rows int) (int, int) {
	start := max(0, cursor-rows+1)
	return start, min(total, start+max(0, rows))
}

// ---- Chats ----

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
	var lines []string
	if c.listErr != "" {
		lines = append(lines, errorLine(c.listErr), muted("r retry · list shown is stale"))
	}
	if !c.listReady && c.loading {
		lines = append(lines, muted("Loading chats…"))
	}
	if len(c.conversations) == 0 && c.listReady {
		return append(lines, emptyPane("No chats", "N starts a conversation.")...)
	}
	start, end := listWindow(len(c.conversations), c.listRow, height-len(lines))
	for i := start; i < end; i++ {
		conversation := c.conversations[i]
		dot := tinted(" ", theme.faint)
		var badges []span
		if conversation.Unread > 0 {
			dot = tinted("●", theme.mention)
			badges = append(badges, boldSpan(capCount(conversation.Unread), theme.mention))
		}
		if conversation.Mentions > 0 {
			badges = append(badges, gap(1), boldSpan("@"+capCount(conversation.Mentions), theme.mention))
		}
		if m.community.chats.drafts[chatDraftKey(m.community.summary.Account, conversation.Target, conversation.Kind)].text != "" {
			badges = append(badges, gap(1), tinted("draft", theme.warning))
		}
		if conversation.Closed {
			badges = append(badges, gap(1), tinted("closed", theme.faint))
		}
		name := plain(conversation.Target)
		if conversation.Unread > 0 {
			name = boldSpan(conversation.Target, theme.text)
		}
		lines = append(lines, focusRow(badgedRow([]span{dot, gap(1)}, name, badges, width), width, i == c.listRow, m.community.pane == 0))
	}
	return lines
}

// chatHistoryLines lays out the transcript oldest first: a header per message
// (time, sender, state badges, id) and its wrapped, indented body. Offsets
// are counted on plain text so scroll anchors survive restyling.
func (m model) chatHistoryLines(width int) []chatLine {
	c := m.community.chats
	var lines []chatLine
	unread := false
	for i := len(c.messages) - 1; i >= 0; i-- {
		message := c.messages[i]
		if !unread && message.Direction == "incoming" && message.ID > c.unreadThrough {
			label := " unread "
			lines = append(lines, chatLine{id: message.ID, offset: -1, text: faint("──") + danger(label) + faint(strings.Repeat("─", max(0, width-2-len(label))))})
			unread = true
		}
		stamp := message.CreatedAt
		if message.ServerTime != nil {
			stamp = *message.ServerTime
		}
		system := message.Direction == "system" || message.Direction == "incoming" && message.Sender == "server"
		body := strings.ReplaceAll(message.Text, "\t", "    ")
		action := strings.HasPrefix(body, "/me ") && !system
		if action {
			body = "* " + message.Sender + " " + strings.TrimPrefix(body, "/me ")
		}
		senderTone := theme.highlight
		switch {
		case system:
			senderTone = theme.muted
		case message.Direction == "outgoing":
			senderTone = theme.accent
		}
		selected := message.ID == c.position.selected
		bar := " "
		if selected {
			bar = accent("▍")
		}
		header := bar + faint(stamp.Local().Format("01-02 15:04")) + " " + styled(message.Sender, fg(senderTone).Bold(true))
		plainHeader := " " + stamp.Local().Format("01-02 15:04") + " " + message.Sender
		badge := func(text string, tone color.Color) {
			if !colorsEnabled() {
				text = "[" + text + "]"
			}
			header += " " + styled(text, fg(tone))
			plainHeader += " " + text
		}
		if message.Direction == "outgoing" {
			tone := theme.muted
			switch message.State {
			case "queued", "pending", "submitting":
				tone = theme.warning
			case "failed", "unknown", "rejected":
				tone = theme.danger
			}
			badge(message.State, tone)
		}
		if system {
			badge("system", theme.muted)
		}
		if message.Mention {
			badge("mention", theme.mention)
		}
		if action {
			badge("action", theme.muted)
		}
		id := fmt.Sprintf("#%d", message.ID)
		header = spread(header, faint(id), width)
		lines = append(lines, chatLine{id: message.ID, offset: 0, text: header})
		offset := len(plainHeader) + 1
		text := body
		if message.Error != "" {
			text += "\n! " + browseErrorText(message.Error)
		}
		for _, part := range strings.Split(ansi.Wrap(text, max(1, width-2), ""), "\n") {
			styledPart := part
			if strings.HasPrefix(part, "! ") {
				styledPart = danger(part)
			} else if system {
				styledPart = muted(part)
			}
			lines = append(lines, chatLine{id: message.ID, offset: offset, text: bar + " " + styledPart})
			offset += len(part) + 1
		}
	}
	return lines
}

func (m model) chatContentPane(width, height int) []string {
	c := m.community.chats
	if c.conversation.Target == "" || !m.communityTranscriptSelected() {
		return emptyPane("Pick a conversation", "Enter opens the highlighted chat; N starts one.", "Messages are stored by the daemon.", "Offline sends stay queued; unknown sends need an explicit retry.")
	}
	var lines []string
	rows := m.chatTranscriptHeight(height)
	history := m.chatHistoryLines(width)
	start := m.chatHistoryStart(history, rows)
	if len(history) > 0 {
		// Short histories sit at the bottom, next to the composer.
		for i := len(history); i < rows; i++ {
			lines = append(lines, "")
		}
	}
	for _, line := range history[start:min(len(history), start+rows)] {
		lines = append(lines, line.text)
	}
	if len(history) == 0 && rows > 0 {
		lines = append(lines, emptyPane("No messages here yet", "i composes the first one.")...)
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	lines = lines[:min(len(lines), rows)]
	if c.err != "" {
		lines = append(lines, ansi.Truncate(errorLine(c.err), width, "…"))
	}
	if c.composing {
		d := c.drafts[m.chatKey()]
		label := fmt.Sprintf("message · %d/%d bytes", len(d.text), 64<<10)
		if strings.Contains(d.text, "\n") {
			label = "multiline · enter previews before sending"
		}
		if c.busy {
			label = "submitting… draft kept until accepted"
		}
		if d.requestID != "" && !c.busy {
			label = "enter reconciles the pending submission"
		}
		lines = append(lines, ansi.Truncate(faint("─ ")+accent(label)+" "+faint(strings.Repeat("─", max(0, width-ansi.StringWidth(label)-3))), width, "…"), accent("› ")+renderInputWindow(strings.ReplaceAll(strings.ReplaceAll(d.text, "\n", "↵"), "\t", "⇥"), d.cursor, max(1, width-2)))
	}
	return lines[:min(len(lines), max(0, height))]
}

func (m model) chatFormView(width, height int) []string {
	c := m.community.chats
	title := map[string]string{"new": "New chat", "find": "Find in history", "filter": "Filter chats", "text": "Export as text", "json": "Export as JSON"}[c.form]
	note := map[string]string{"new": "Exact username.", "find": "Literal, case-sensitive.", "filter": "Literal, case-sensitive.", "text": "Path to a new file; existing files are never overwritten.", "json": "Path to a new file; existing files are never overwritten."}[c.form]
	err := c.inputErr
	if err == "" {
		err = c.err
	}
	return communityForm(title, renderInputWindow(c.input, c.inputCursor, max(1, width-2)), []string{note, "Pasting never submits."}, err, "enter submit · esc back", width, height)
}

// ---- Rooms ----

func (m model) roomListPane(width, height int) []string {
	r := m.community.rooms
	var lines []string
	if r.invitationsState != "" && r.private.available(m.community) {
		state := "off"
		if r.invitationsEnabled {
			state = "on"
		}
		lines = append(lines, muted("invitations ")+accent(state)+muted(" · "+r.invitationsState))
	}
	if r.query != "" {
		lines = append(lines, muted("find ")+accent(r.query))
	}
	if r.listErr != "" {
		lines = append(lines, errorLine(r.listErr))
	}
	if r.actionErr != "" {
		lines = append(lines, errorLine(r.actionErr))
	}
	if r.loading && !r.listReady {
		lines = append(lines, muted("Loading directory…"))
	}
	if len(r.rooms) == 0 {
		return append(lines, emptyPane("No matching rooms", "N joins or creates one.")...)
	}
	start, end := listWindow(len(r.rooms), r.row, height-len(lines))
	for i := start; i < end; i++ {
		room := r.rooms[i]
		dot := tinted("○", theme.faint)
		if room.Joined {
			dot = tinted("●", theme.success)
		}
		var badges []span
		if room.Private {
			badges = append(badges, tinted("private", theme.warning), gap(1))
		}
		if room.Remembered {
			badges = append(badges, tinted("auto", theme.accent), gap(1))
		}
		pop := "?"
		if room.PopulationKnown {
			pop = fmt.Sprint(room.Population)
		}
		badges = append(badges, tinted(searchColumn(pop, 4), theme.muted))
		lines = append(lines, focusRow(badgedRow([]span{dot, gap(1)}, plain(room.Name), badges, width), width, i == r.row, m.community.pane == 0))
	}
	return lines
}

func (m model) roomRosterPane(width, height int) []string {
	if m.community.target != "" {
		return m.community.inspectorLines()
	}
	r := m.community.rooms
	var lines []string
	if r.membersErr != "" {
		lines = append(lines, errorLine(r.membersErr))
	}
	if r.membersLoading && !r.membersReady {
		lines = append(lines, muted("Loading roster…"))
	}
	if len(r.members) == 0 && r.membersReady {
		return append(lines, emptyPane("No members listed")...)
	}
	start, end := listWindow(len(r.members), r.memberRow, height-len(lines))
	for i := start; i < end; i++ {
		lines = append(lines, focusRow([]span{plain(r.members[i].Username)}, width, i == r.memberRow, m.community.pane == 2))
	}
	return lines
}

func (m model) roomContentPane(width, height int) []string {
	r := m.community.rooms
	if r.feedView {
		return m.roomFeedPane(width, height)
	}
	if r.private.view == "wall" {
		return m.roomWallPane(width, height)
	}
	if r.private.view == "roles" {
		return m.roomRolesPane(width, height)
	}
	if !m.communityTranscriptSelected() {
		return emptyPane("Pick a room", "Enter opens its history without joining.", "J joins · L leaves · R / F remember or forget autojoin.", "G shows the read-only public feed.", r.actionErr)
	}
	err := r.actionErr
	if err == "" {
		err = r.active.Error
	}
	if err != "" {
		return append([]string{ansi.Truncate(errorLine(err), width, "…")}, m.chatContentPane(width, max(0, height-1))...)
	}
	return m.chatContentPane(width, height)
}

func (m model) roomFeedPane(width, height int) []string {
	r := m.community.rooms
	state := "off"
	if r.feedWanted {
		state = "requested"
	}
	if r.feedWritten {
		state = "request sent (no server ACK)"
	}
	head := []string{muted("subscription ") + accent(state)}
	if r.feedErr != "" {
		head = append(head, errorLine(r.feedErr))
	}
	if r.actionErr != "" {
		head = append(head, errorLine(r.actionErr))
	}
	var messages []string
	for _, message := range r.feed {
		messages = append(messages, faint(message.CreatedAt.Local().Format("15:04:05"))+" "+styled(message.Sender, fg(theme.highlight).Bold(true))+muted(" in "+message.Room)+"  "+strings.ReplaceAll(message.Text, "\t", "    "))
	}
	if len(messages) == 0 {
		messages = emptyPane("No feed messages", "The feed is not logged by default.")
	}
	head = communityPane(head, width, min(len(head), height), 0)
	return append(head, communityPane(messages, width, max(0, height-len(head)), r.feedScroll)...)
}

func (m model) roomRolesPane(width, height int) []string {
	r, p := m.community.rooms, m.community.rooms.private
	fresh := faint("stale")
	if r.active.RoleFresh && m.community.summary.Connected && m.community.err == "" {
		fresh = success("fresh")
	}
	lines := []string{kv("Your role", strong(r.active.Role)+" "+fresh), kv("Owner", r.active.Owner)}
	if r.active.LastRoleAction != nil {
		a := r.active.LastRoleAction
		lines = append(lines, kv("Last action", fmt.Sprintf("%s · %s", a.Action, a.State)))
	}
	for _, err := range []string{r.active.Error, p.err, r.membersErr} {
		if err != "" {
			lines = append(lines, errorLine(err))
		}
	}
	if !r.membersFresh {
		lines = append(lines, muted("Membership list unavailable or stale."))
	}
	if r.membersLoading {
		lines = append(lines, muted("Loading members…"))
	}
	if p.busy {
		lines = append(lines, warning("Submitting; not yet confirmed"))
	}
	// Status and errors never crowd out the member list: keep two rows for it.
	lines = append(communityPane(lines, width, max(0, height-3), 0), sectionRule("Members", "", width))
	start, end := listWindow(len(r.members), r.memberRow, height-len(lines))
	for i := start; i < end; i++ {
		member := r.members[i]
		tone := theme.muted
		if member.Role == "owner" || member.Role == "operator" {
			tone = theme.accent
		}
		lines = append(lines, focusRow(badgedRow(nil, plain(member.Username), []span{tinted(member.Role, tone)}, width), width, i == r.memberRow, m.community.pane == 1))
	}
	return lines
}

func (m model) roomWallPane(width, height int) []string {
	p := m.community.rooms.private
	fresh := faint("stale")
	if p.wall.Fresh && m.community.rooms.active.Joined && m.community.summary.Connected && m.community.err == "" {
		fresh = success("fresh")
	}
	own := p.wall.OwnText
	if own == "" {
		own = faint("not set")
	}
	body := []string{kv("State", subtle(p.wall.State)+" "+fresh), kv("Your text", own)}
	for _, err := range []string{m.community.rooms.active.Error, p.err} {
		if err != "" {
			body = append(body, errorLine(err))
		}
	}
	if p.wallLoading {
		body = append(body, muted("Loading wall…"))
	}
	if p.busy {
		body = append(body, warning("Saving…"))
	}
	body = append(body, "", sectionRule("Entries", "", width))
	if len(p.wall.Entries) == 0 {
		body = append(body, muted("No wall entries."))
	}
	for _, entry := range p.wall.Entries {
		body = append(body, styled(entry.Username, fg(theme.highlight).Bold(true))+"  "+strings.ReplaceAll(entry.Text, "\t", "    "))
	}
	return communityPane(body, width, height, p.wallScroll)
}

func (m model) roomFormView(width, height int) []string {
	r := m.community.rooms
	title, notes := "Join or create a public room", []string{"Exact room name."}
	if r.form == "join" && r.createPrivate {
		title = "Create a private room"
	}
	if r.form == "filter" {
		title, notes = "Filter rooms", []string{"Case-insensitive."}
	}
	hints := "enter submit · esc cancel"
	if r.form == "join" {
		notes = append(notes, onOff("Remember / autojoin", r.remember)+muted("  tab"), onOff("Private room", r.createPrivate)+muted("  ctrl+p"))
		hints = "enter submit · tab autojoin · ctrl+p private · esc cancel"
	}
	err := r.inputErr
	if err == "" {
		err = r.actionErr
	}
	return communityForm(title, renderInputWindow(r.input, r.inputCursor, max(1, width-2)), append(notes, "Pasting never submits."), err, hints, width, height)
}

// onOff renders a labelled toggle state.
func onOff(label string, on bool) string {
	label = subtle(searchTextColumn(label, 20))
	if on {
		return label + success("● on")
	}
	return label + faint("○ off")
}

func (m model) privateRoomFormView(width, height int) []string {
	p := m.community.rooms.private
	title, value, cursor, note := "Edit your room wall", p.wallInput, p.wallInputCursor, "Local draft until you submit; Enter previews first."
	if p.roleForm != "" {
		title, value, cursor, note = strings.ToUpper(p.roleForm[:1])+p.roleForm[1:], p.roleInput, p.roleCursor, "Exact username."
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\n", "↵"), "\t", "⇥")
	return communityForm(title, renderInputWindow(value, cursor, max(1, width-2)), []string{note, "Pasting never submits."}, p.err, "enter submit/preview · esc keep draft", width, height)
}

// ---- Buddies ----

func buddyTone(b daemon.CommunityBuddy, connected bool) color.Color {
	if !connected || !b.StatusFresh || !b.Exists {
		return theme.faint
	}
	switch b.Status {
	case soulseek.UserStatusOnline:
		return theme.success
	case soulseek.UserStatusAway:
		return theme.warning
	}
	return theme.muted
}

func (m model) buddyListPane(width, height int) []string {
	b := m.community.buddies
	live := m.community.summary.Connected && m.community.err == "" && b.err == ""
	var lines []string
	if b.query != "" {
		lines = append(lines, muted("find ")+accent(b.query))
	}
	if b.err != "" {
		lines = append(lines, errorLine(b.err))
	}
	if b.loading && !b.listReady {
		lines = append(lines, muted("Loading buddies…"))
	}
	if len(b.buddies) == 0 {
		return append(lines, emptyPane("No buddies", "a adds one by exact username.")...)
	}
	start, end := listWindow(len(b.buddies), b.row, height-len(lines))
	for i := start; i < end; i++ {
		buddy := b.buddies[i]
		var badges []span
		if buddy.Trusted {
			badges = append(badges, tinted("trusted", theme.accent), gap(1))
		}
		if buddy.Priority {
			badges = append(badges, tinted("priority", theme.upload))
		}
		lines = append(lines, focusRow(badgedRow([]span{tinted("●", buddyTone(buddy, live)), gap(1)}, plain(buddy.Username), badges, width), width, i == b.row, m.community.pane == 0))
	}
	return lines
}

func (m model) buddyDetailPane(width, height int) []string {
	b := m.community.buddies
	if b.active == nil {
		return emptyPane("Pick a buddy", "Enter shows their details.", b.selected, b.err)
	}
	buddy := b.active
	live := m.community.summary.Connected && m.community.err == "" && b.err == ""
	country := buddy.Country
	if country == "" {
		country = "unknown"
	}
	if country != "unknown" && (!live || !buddy.StatusFresh) {
		country += " (stale)"
	}
	seen := "never observed offline"
	if !buddy.LastSeen.IsZero() {
		seen = buddy.LastSeen.Local().Format(time.RFC3339)
	}
	note := strings.ReplaceAll(buddy.Note, "\t", "    ")
	if note == "" {
		note = faint("No note.")
	}
	lines := []string{
		kv("Status", styled("● "+buddyStatus(*buddy, live), fg(buddyTone(*buddy, live)))),
		kv("Country", country),
		kv("Last seen", seen),
		"",
		sectionRule("Preferences", "", width),
		onOff("Notify when online", buddy.NotifyOnline),
		onOff("Upload priority", buddy.Priority),
		onOff("Trusted", buddy.Trusted),
		muted("Priority: preferred upload class; running files continue."),
		muted("Trust: trusted roots (not self); bans still apply."),
		"",
		sectionRule("Note", "", width),
		note,
	}
	if b.err != "" {
		lines = append(lines, errorLine(b.err))
	}
	return communityPane(lines, width, height, b.scroll)
}

func (m model) buddyEditorView(width, height int) []string {
	b, e := m.community.buddies, m.community.buddies.editor
	if e == nil {
		return communityForm("Find buddies", renderInputWindow(b.input, b.inputCursor, max(1, width-2)), []string{"Matches usernames and notes."}, b.inputErr, "enter filter · esc back", width, height)
	}
	labels := []string{"Username", "Note", "Notify online", "Upload priority", "Trusted"}
	values := []string{e.username, strings.ReplaceAll(strings.ReplaceAll(e.note, "\n", "↵"), "\t", "⇥"), "", "", ""}
	toggles := []bool{false, false, e.notify, e.priority, e.trusted}
	cursor := e.noteCursor
	if e.field == 0 {
		cursor = e.nameCursor
	}
	if width < 44 || height < 12 {
		// Too small for every field: edit the focused one at full width.
		value := renderInputWindow(values[e.field], cursor, max(1, width-2))
		if e.field >= 2 {
			value = onOff("", toggles[e.field])
		}
		return communityForm("Edit buddy · "+labels[e.field], value, nil, e.err, "tab field · space toggle · enter save · esc keep draft", width, height)
	}
	lines := []string{accent("Edit buddy"), ""}
	for i, label := range labels {
		value := ansi.Truncate(values[i], max(1, width-20), "…")
		if i >= 2 {
			value = success("● on")
			if !toggles[i] {
				value = faint("○ off")
			}
		}
		if i == e.field && i < 2 {
			value = renderInputWindow(values[i], cursor, max(1, width-20))
		}
		marker := "  "
		if i == e.field {
			marker = accent("› ")
		}
		lines = append(lines, marker+muted(searchTextColumn(label, 16))+value)
	}
	if b.busy || b.opening {
		lines = append(lines, "", warning("Working…"))
	}
	if e.err != "" {
		lines = append(lines, "", danger("✗ "+e.err))
	}
	lines = append(lines, "", dialogHints("tab field · space toggle · enter save · esc keep draft · ctrl+r reload", width))
	return communityPane(lines, width, height, 0)
}

// ---- Discover ----

func (m model) discoverSidebar(width, height int) []string {
	d := m.community.discover
	var lines []string
	for i, mode := range discoverModes {
		lines = append(lines, focusRow([]span{plain(mode.label)}, width, i == d.mode, m.community.pane == 0))
	}
	start, end := listWindow(len(lines), d.mode, height)
	return lines[start:end]
}

func (m model) discoverRowsPane(width, height int) []string {
	d := m.community.discover
	if d.kind() == "profile" {
		description := strings.ReplaceAll(d.profile.Description, "\t", "⇥")
		if description == "" {
			description = faint("No description yet; e writes one.")
		}
		lines := []string{sectionRule("Description", "", width), description}
		if d.err != "" {
			lines = append([]string{errorLine(d.err)}, lines...)
		}
		return communityPane(lines, width, height, d.profileScroll)
	}
	var lines []string
	if d.query != "" {
		lines = append(lines, muted("find ")+accent(d.query))
	}
	if d.kind() == "interests" {
		if d.err != "" {
			lines = append(lines, errorLine(d.err))
		}
		if len(d.interests) == 0 {
			return append(lines, emptyPane("No interests saved", "a adds a like or dislike.")...)
		}
		start, end := listWindow(len(d.interests), d.row, height-len(lines))
		for i := start; i < end; i++ {
			x := d.interests[i]
			tone := theme.success
			if x.Opinion != "like" && x.Opinion != "liked" {
				tone = theme.danger
			}
			lines = append(lines, focusRow(badgedRow(nil, plain(x.Item), []span{tinted(x.Opinion, tone), gap(1), tinted(x.State, theme.faint)}, width), width, i == d.row, m.community.pane == 1))
		}
		return lines
	}
	if d.state != "" {
		state := d.state
		if d.err != "" {
			state += ": " + d.err
		}
		lines = append(lines, muted("state ")+subtle(state))
	}
	visible := m.discoverVisibleRows()
	if len(visible) == 0 {
		return append(lines, emptyPane("No results yet", "r refreshes; t sets the target.")...)
	}
	start, end := listWindow(len(visible), d.row, height-len(lines))
	for i := start; i < end; i++ {
		x := visible[i]
		name, badge := plain(x.Item), span{}
		if x.User != nil {
			name = boldSpan(x.User.Username, theme.highlight)
			if x.Rating > 0 {
				badge = tinted(fmt.Sprintf("rating %d", x.Rating), theme.muted)
			}
		} else if x.Score != 0 {
			badge = tinted(fmt.Sprintf("score %d", x.Score), theme.muted)
		}
		lines = append(lines, focusRow(badgedRow(nil, name, []span{badge}, width), width, i == d.row, m.community.pane == 1))
	}
	return lines
}

func (m model) discoverFormView(width, height int) []string {
	d := m.community.discover
	if d.form == "profile" {
		return communityForm("Edit your description", renderInputWindow(strings.ReplaceAll(strings.ReplaceAll(d.profileDraft, "\n", "↵"), "\t", "⇥"), d.profileCursor, max(1, width-2)), []string{"ctrl+j inserts a newline."}, d.inputErr, "enter save · ctrl+r reload · esc keep draft", width, height)
	}
	title, notes := "Filter results", []string(nil)
	if d.form == "target" {
		title, notes = "Discovery target", []string{"An interest or an exact username."}
	}
	if d.form == "interest" {
		title, notes = "Interest · "+d.interestOpinion, []string{"tab switches like / dislike."}
	}
	return communityForm(title, renderInputWindow(d.input, d.inputCursor, max(1, width-2)), append(notes, "Pasting never submits."), d.inputErr, "enter submit · esc keep draft", width, height)
}

// ---- Inspector ----

func stale(value string, fresh bool) string {
	if fresh {
		return value
	}
	return value + faint(" (stale)")
}

func (c communityModel) inspectorLines() []string {
	if c.target == "" {
		return emptyPane("No user selected", "U on any user, or / to type a username.")
	}
	u := c.user
	lines := []string{kv("User", fmt.Sprintf("%q", c.target))}
	if c.userLoading && u.Username == "" {
		lines = append(lines, muted("Refreshing user details…"))
	}
	if c.userErr != "" {
		lines = append(lines, errorLine(c.userErr), muted("r retry"))
	}
	live := c.summary.Connected && c.err == "" && c.userErr == ""
	status := faint("unknown")
	if !u.StatusUpdatedAt.IsZero() || u.StatusFresh {
		label, tone := "offline", theme.muted
		switch u.Status {
		case soulseek.UserStatusOnline:
			label, tone = "online", theme.success
		case soulseek.UserStatusAway:
			label, tone = "away", theme.warning
		}
		if !u.Exists && u.StatusFresh {
			label, tone = "not found", theme.danger
		}
		status = stale(styled("● "+label, fg(tone)), u.StatusFresh && live)
	}
	lines = append(lines, kv("Status", status))
	country := faint("unknown")
	if u.Country != "" {
		country = stale(fmt.Sprintf("%q", u.Country), u.StatusFresh && live)
	}
	lines = append(lines, kv("Country", country))
	supporter := faint("unknown")
	if u.PrivilegeFresh || !u.PrivilegeUpdatedAt.IsZero() {
		supporter = "no"
		if u.Privileged {
			supporter = accent("yes")
		}
		supporter = stale(supporter, live && u.PrivilegeFresh)
	}
	lines = append(lines, kv("Supporter", supporter))
	if u.StatsFresh || !u.StatsUpdatedAt.IsZero() {
		fresh := u.StatsFresh && live
		lines = append(lines, kv("Files", stale(fmt.Sprint(u.Stats.Files), fresh)), kv("Folders", stale(fmt.Sprint(u.Stats.Directories), fresh)), kv("Speed", stale(formatBytes(uint64(u.Stats.AverageSpeed))+"/s", fresh)))
	} else {
		lines = append(lines, kv("Shares", faint("unknown")))
	}
	address := faint("unknown")
	if u.IP != "" {
		address = stale(fmt.Sprintf("%q", u.IP), u.AddressFresh && live)
	}
	lines = append(lines, kv("IP", address))
	if !u.LastSeen.IsZero() {
		lines = append(lines, kv("Last seen", u.LastSeen.Local().Format("2006-01-02 15:04")))
	}
	lines = append(lines, "")
	lines = append(lines, c.peerLines()...)
	return append(lines, "", faint("Partial server information."))
}

func (c communityModel) peerLines() []string {
	if !c.supports("profiles") {
		return []string{muted("Peer profiles unavailable in this daemon.")}
	}
	p := c.peer
	state := p.profile.State
	if p.err != "" {
		state = "stale"
	}
	if state == "" {
		state = "loading"
	}
	if !c.summary.Connected {
		state = "offline"
	}
	lines := []string{miniHeader("Profile", state)}
	for _, err := range []string{p.err, p.profile.Error} {
		if err != "" {
			lines = append(lines, errorLine(err))
		}
	}
	if !p.profile.UpdatedAt.IsZero() {
		fresh := state == "ready" && p.err == ""
		policy := faint("unknown")
		if p.profile.UploadAllowedKnown {
			policy = fmt.Sprintf("%d", p.profile.UploadAllowed) + faint(" (peer reported)")
		}
		slots := "full"
		if p.profile.SlotsAvailable {
			slots = "free"
		}
		lines = append(lines,
			kv("Fetched", p.profile.UpdatedAt.Local().Format("2006-01-02 15:04")),
			kv("Slots", stale(fmt.Sprintf("%d · %s", p.profile.UploadSlots, slots), fresh)),
			kv("Queue", stale(fmt.Sprint(p.profile.QueueLength), fresh)),
			kv("Uploads in", policy))
		if p.profile.PictureType != "" {
			lines = append(lines, kv("Picture", stale(fmt.Sprintf("%s %dx%d", p.profile.PictureType, p.profile.PictureWidth, p.profile.PictureHeight), fresh)+muted(" · P save")))
		}
		description := strings.ReplaceAll(p.profile.Description, "\t", "⇥")
		if description == "" {
			description = faint("No description.")
		}
		lines = append(lines, "", description)
	}
	if c.supports("discovery") {
		interestState := p.interests.State
		if p.interestErr != "" {
			interestState = "stale"
		}
		if !c.summary.Connected {
			interestState = "offline (stale)"
		}
		lines = append(lines, "", miniHeader("Interests", interestState))
		for _, err := range []string{p.interestErr, p.interests.Error} {
			if err != "" {
				lines = append(lines, errorLine(err))
			}
		}
		for _, row := range p.interests.Rows {
			tone := theme.success
			if strings.Contains(row.Kind, "dislike") || strings.Contains(row.Kind, "hate") {
				tone = theme.danger
			}
			lines = append(lines, styled("● ", fg(tone))+row.Item+faint(" "+row.Kind))
		}
	}
	return lines
}

func (m model) peerPictureForm(width, height int) []string {
	p := m.community.peer
	return communityForm("Save picture for "+p.image.Username, renderInputWindow(p.path, p.pathCursor, max(1, width-2)), []string{"Existing files are never overwritten."}, p.err, "enter review · esc cancel", width, height)
}
