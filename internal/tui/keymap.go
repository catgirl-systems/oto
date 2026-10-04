package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Key conventions shared by every workspace:
//
//	tab / shift+tab  workspaces       [ / ]   tabs, views, pages, sections
//	↑↓ jk pgup pgdn  move             ← → hl  collapse / expand
//	home end g G     first / last     space   mark, esc clears marks
//	enter            primary action   /       search or type a value
//	f                filter or find   a       add
//	x / delete       remove, cancel   r       refresh, retry, rescan
//	s                save             i       details
//	U                user actions     ?       keyboard guide
//
// Shift selects the broader variant of a lowercase action: d / D download /
// download as, x / X cancel / cancel for every marked user, c / C clear /
// clear by status, s / S search file / search folder.

type keyGroup struct {
	title string
	home  workspace // workspace that opens the guide at this group; -1 for none
	rows  [][2]string
}

var keyGuide = []keyGroup{
	{"Everywhere", -1, [][2]string{
		{"tab  shift+tab", "next / previous workspace"},
		{"1 … 8", "jump to a workspace"},
		{"[  ]", "previous / next tab, view, page or section"},
		{"ctrl+w", "close the current search or user tab"},
		{"U", "user actions for the highlighted user"},
		{"u", "search a user's, room's or buddies' shares"},
		{"o", "set Online, Away or Offline"},
		{"?", "open / close this guide"},
		{"q", "quit"},
	}},
	{"Lists and trees", -1, [][2]string{
		{"↑ ↓  j k", "move"},
		{"pgup  pgdn", "move by a page"},
		{"home end  g G", "first / last item"},
		{"← →  h l", "collapse / expand a folder"},
		{"space", "mark an item or a whole folder"},
		{"esc", "clear marks"},
		{"enter", "primary action for the highlighted row"},
	}},
	{"Text input", -1, [][2]string{
		{"enter  esc", "apply / cancel"},
		{"← →  ctrl+← →", "move by character / word"},
		{"home end  ctrl+a e", "line start / end"},
		{"ctrl+w  ctrl+⌫", "delete the previous word"},
		{"ctrl+u  ctrl+k", "delete to line start / end"},
		{"↑ ↓", "history (Search and Wishlist)"},
		{"tab", "complete filter keywords and values"},
	}},
	{"Search", workspaceSearch, [][2]string{
		{"/", "new search"},
		{"f  c", "filter results / clear or restore the filter"},
		{"enter", "download a file, expand a folder"},
		{"d", "download; folders ask for mode and destination"},
		{"D", "download with a different local filename"},
		{"i", "file details"},
		{"b", "browse the user's folder"},
		{"w", "save the query and filter to Wishlist"},
	}},
	{"Wishlist", workspaceWishlist, [][2]string{
		{"a  /", "add an item"},
		{"enter  r", "open results / search again now"},
		{"f", "edit the item's filter"},
		{"x", "remove the item"},
	}},
	{"Browse", workspaceBrowse, [][2]string{
		{"/", "browse a user's shares"},
		{"enter", "open a saved list, download a file, expand a folder"},
		{"f", "find in the loaded list"},
		{"d  D  i", "download / download as / details"},
		{"s", "save the list for offline browsing"},
		{"r", "refresh"},
	}},
	{"Transfers", workspaceTransfers, [][2]string{
		{"[  ]", "downloads / uploads"},
		{"p  r", "pause / resume or retry"},
		{"x  X", "cancel / cancel all uploads of the marked users"},
		{"c  C", "clear finished / clear uploads by status"},
		{"F", "download filtered files anyway"},
		{"/  S", "search for the file / its folder"},
		{"elapsed  ETA", "folder and user rows add up their files"},
	}},
	{"Community", workspaceCommunity, [][2]string{
		{"[  ]", "Chats · Rooms · Buddies · Discover"},
		{"F6  shift+F6", "next / previous pane; esc goes back"},
		{"/", "inspect an exact username"},
		{"N  ctrl+n", "Chats: new conversation / next unread"},
		{"i  enter", "Chats: compose; multiline previews before sending"},
		{"tab  esc", "composer: complete username / back to navigation"},
		{"f  p n  end", "chat: find / older, newer pages / latest"},
		{"y  e E", "chat: copy message / export text or JSON"},
		{"R  X  C", "private chat: retry / cancel / clear history"},
		{"ctrl+w  h", "Chats list: close / show closed history"},
		{"N  J", "Rooms: join or create / join selected"},
		{"L  R  F", "room: leave / remember / forget autojoin"},
		{"f  m  p n", "Rooms list: filter / mode / pages"},
		{"G  g", "public feed: view / subscribe or stop"},
		{"M  W  I", "private roles / room wall / invitations"},
		{"a A o O d", "roles: add member, operator; grant, revoke; remove"},
		{"c C  r", "roles: relinquish membership, ownership / reconcile"},
		{"a  e  D", "Buddies: add / edit / remove"},
		{"f  s  p n", "Buddies: filter / sort / pages"},
		{"s  i  u", "Discover: search / recommendations / related users"},
		{"a  e  D", "Interests: add / edit / remove"},
		{"r  p n  P", "Inspector: refresh / interest pages / save picture"},
	}},
	{"Stats", workspaceStats, [][2]string{
		{"[  ]", "previous / next page"},
		{"a", "next account"},
		{"/", "filter by peer; search on Logs"},
		{"r", "change range; refresh on Logs"},
		{"<  >", "set from / before dates"},
		{"d  e  s", "direction / outcome / sort"},
		{"enter  n p", "event details / next, first page"},
		{"f", "log level"},
		{"P", "prune old statistics"},
	}},
	{"Shares", workspaceShares, [][2]string{
		{"a  /", "add a share as name:path"},
		{"enter", "access level and locked reveal"},
		{"s", "send the highlighted file or folder to a user"},
		{"r  c", "rescan / cancel a running scan"},
		{"x", "remove the share"},
	}},
	{"Settings", workspaceSettings, [][2]string{
		{"[  ]  ← →", "previous / next section"},
		{"enter", "edit, toggle, choose or run"},
		{"s", "save every section"},
		{"x", "remove the highlighted download filter"},
		{"a e x R", "exclusions: add / edit / remove / restore defaults"},
	}},
}

// helpLines lays out the guide with the current workspace's group first.
func (m model) helpLines(width int) []string {
	order := make([]keyGroup, 0, len(keyGuide))
	for _, g := range keyGuide {
		if g.home == m.workspace {
			order = append(order, g)
		}
	}
	for _, g := range keyGuide {
		if g.home != m.workspace {
			order = append(order, g)
		}
	}
	keyWidth := 20
	if width < 56 {
		keyWidth = 14
	}
	var lines []string
	for i, g := range order {
		if i > 0 {
			lines = append(lines, "")
		}
		title := strings.ToUpper(g.title)
		if g.home == m.workspace {
			title += "  " + muted("(this workspace)")
		}
		lines = append(lines, accent(title))
		for _, row := range g.rows {
			lines = append(lines, "  "+styled(searchTextColumn(row[0], keyWidth), fg(theme.highlight).Bold(true))+" "+subtle(ansi.Truncate(row[1], max(4, width-keyWidth-3), "…")))
		}
	}
	return lines
}

func (m model) helpView() string {
	width := max(34, min(84, m.width-4))
	rows := m.helpLines(width - 4)
	visible := max(1, m.height-8)
	scroll := max(0, min(m.helpScroll, len(rows)-visible))
	body := append([]string{}, rows[scroll:min(len(rows), scroll+visible)]...)
	more := ""
	if scroll+visible < len(rows) {
		more = " · ↓ more"
	}
	return m.dialog("Keyboard", body, "↑/↓ scroll · ? close · esc close"+more, width)
}

// footerHints lists the most useful keys for the current context, primary
// actions first; the footer shows as many as fit.
func (m model) footerHints() []string {
	if m.stats.prune {
		if m.stats.prunePending {
			if m.stats.pruneConfirm {
				return []string{"pruning…"}
			}
			return []string{"loading preview…", "esc cancel"}
		}
		if m.stats.pruneConfirm {
			return []string{"enter confirm prune", "esc cancel"}
		}
		return []string{"enter preview", "l logs", "d daily", "esc cancel"}
	}
	if m.choiceChoosing {
		return []string{"←/→ choose", "enter accept", "esc cancel"}
	}
	if m.editing {
		action := "apply"
		switch {
		case m.filterEditing:
			action = "apply filter"
		case m.browseFindEditing:
			action = "apply find"
		case m.workspace == workspaceSearch:
			action = "search"
		case m.workspace == workspaceBrowse:
			action = "browse"
		case m.workspace == workspaceShares, m.workspace == workspaceWishlist:
			action = "add"
		}
		hints := []string{"enter " + action, "esc cancel"}
		if m.filterEditing {
			hints = append(hints, "tab complete")
		}
		if m.workspace == workspaceSearch || m.workspace == workspaceWishlist {
			hints = append(hints, "↑/↓ history")
		}
		return hints
	}

	switch m.workspace {
	case workspaceSearch:
		var hints []string
		if _, node := m.searchTree.node(m.cursor); node != nil {
			if node.kind == treeFile {
				hints = []string{"enter download", "space mark", "D save as", "i info", "b browse"}
			} else if node.kind == treeFolder {
				hints = []string{"enter expand", "d folder download", "space mark", "b browse"}
			} else {
				hints = []string{"enter expand", "d download", "space mark", "b browse"}
			}
		}
		hints = append(hints, "/ search", "f filter", "w wishlist", "U user")
		if len(m.searchTabs) > 1 {
			hints = append(hints, "[/] tab", "ctrl+w close")
		}
		return hints
	case workspaceWishlist:
		return []string{"enter open", "a add", "f filter", "r rerun", "x remove"}
	case workspaceBrowse:
		if len(m.browseTabs) == 0 {
			return []string{"enter open", "/ browse user", "r refresh"}
		}
		var hints []string
		if _, node := m.browseTree.node(m.cursor); node != nil {
			if node.kind == treeFile {
				hints = []string{"enter download", "space mark", "D save as", "i info"}
			} else if node.kind == treeFolder {
				hints = []string{"enter expand", "d folder download", "space mark"}
			} else {
				hints = []string{"enter expand"}
			}
		}
		if m.browseLoaded {
			hints = append(hints, "f find")
		}
		hints = append(hints, "s save list", "r refresh", "/ user", "U user")
		if len(m.browseTabs) > 1 {
			hints = append(hints, "[/] tab", "ctrl+w close")
		}
		return hints
	case workspaceTransfers:
		if m.transferTab == transferDownloads {
			return []string{"space mark", "p pause", "r resume/retry", "x cancel", "c clear", "F download anyway", "/ search", "S folder search", "] uploads", "U user"}
		}
		return []string{"space mark", "r retry", "x abort", "X abort users", "c clear selected", "C clear status", "/ search", "[ downloads", "U user"}
	case workspaceCommunity:
		return m.communityHints()
	case workspaceStats:
		if m.stats.edit != "" {
			return []string{"enter apply", "esc cancel"}
		}
		if m.stats.detail != nil {
			return []string{"↑/↓ scroll", "esc close"}
		}
		var hints []string
		switch m.stats.page {
		case 0:
			hints = []string{"↑/↓ scroll", "/ peer", "esc clear peer"}
		case 1:
			hints = []string{"r range", "</> dates", "/ peer", "↑/↓ scroll", "esc clear peer"}
		case 2:
			hints = []string{"enter details", "s sort", "d direction", "n next", "p first", "/ peer"}
		case 3:
			hints = []string{"enter details", "d direction", "e outcome", "</> dates", "n next", "p first", "/ peer"}
		case 4:
			hints = []string{"r refresh", "f level", "/ search", "esc clear filters"}
		}
		return append(hints, "[/] page", "a account", "P prune")
	case workspaceShares:
		hints := []string{"enter access"}
		if _, node := m.shareTree.node(m.cursor); node != nil && node.kind != treeFile {
			hints = append(hints, "→ expand")
		}
		hints = append(hints, "a add", "s send", "r rescan")
		if scan := m.status.shareScan; scan != nil && scan.State == "scanning" {
			hints = append(hints, "c cancel scan")
		}
		if _, node := m.shareTree.node(m.cursor); node != nil && node.kind == treeShareRoot {
			hints = append(hints, "x remove")
		}
		return hints
	case workspaceSettings:
		if m.shareExclusions.open {
			if m.shareExclusions.editing {
				return []string{"enter stage rule", "esc cancel edit", "←/→ move caret"}
			}
			if m.settingsSaving {
				return []string{"saving settings", "esc back"}
			}
			return []string{"a add", "enter edit", "x remove", "s save", "esc back", "R restore defaults"}
		}
		hints := []string{"s save", "[/] section"}
		fields := m.settingFields()
		if m.cursor < 0 || m.cursor >= len(fields) {
			return hints
		}
		field := fields[m.cursor]
		action := "edit"
		switch field.kind {
		case settingInfo:
			return hints
		case settingBool:
			action = "toggle"
		case settingChoice:
			action = "choose"
		case settingAction:
			switch field.id {
			case settingStatsPrune:
				action = "edit prune days"
			case settingManageShareExclusions:
				action = "manage exclusions"
			case settingChangePassword:
				action = "change password"
			case settingListeningPortStatus:
				action = "check port"
			case settingClearSearchHistory:
				action = "clear searches"
			case settingClearFilterHistory:
				action = "clear filters"
			default:
				action = "run"
			}
		}
		hints = append([]string{"enter " + action}, hints...)
		if m.downloadRuleIndex() >= 0 && m.settingsSection == settingsDownloads {
			hints = append(hints, "x remove rule")
		}
		return hints
	default:
		return nil
	}
}

// communityHints lists Community keys for the focused view and pane.
func (m model) communityHints() []string {
	c := m.community
	common := []string{"[/] view", "F6 pane", "/ inspect"}
	switch {
	case c.inspectEditing:
		return []string{"enter inspect", "esc back"}
	case c.chats.composing:
		return []string{"enter send/preview", "tab complete", "esc navigate"}
	case c.peer.form:
		return []string{"enter review", "esc cancel"}
	case c.chats.form != "" || c.discover.form != "":
		return []string{"enter submit", "esc cancel"}
	case c.rooms.form != "":
		return []string{"enter submit", "tab autojoin", "ctrl+p private", "esc cancel"}
	case c.rooms.private.editing():
		return []string{"enter submit/preview", "esc keep draft"}
	case c.buddies.editor != nil:
		return []string{"enter save", "tab field", "space toggle", "ctrl+r reload", "esc keep draft"}
	case c.buddies.form != "":
		return []string{"enter filter", "esc back"}
	}
	if c.pane == 2 && !(c.view == 1 && c.target == "") {
		return append([]string{"U actions", "r refresh", "p/n interests", "P save picture", "↑/↓ scroll", "esc back"}, common[:2]...)
	}
	var hints []string
	switch c.view {
	case 0:
		if !c.supports("private-chat") {
			break
		}
		if c.pane == 0 {
			hints = []string{"enter open", "N new", "ctrl+n unread", "f filter", "h history", "ctrl+w close", "p/n pages"}
		} else {
			hints = []string{"i compose", "↑/↓ select", "f find", "y copy", "e/E export", "R/X retry/cancel", "C clear", "end latest"}
		}
	case 1:
		r := c.rooms
		switch {
		case r.feedView:
			hints = []string{"g subscribe/off", "↑/↓ scroll", "p/n pages", "esc back"}
		case r.private.view == "roles":
			hints = []string{"a/A add member/op", "o/O grant/revoke", "d remove", "c/C relinquish", "r reconcile", "esc back"}
		case r.private.view == "wall":
			hints = []string{"i edit", "C clear", "p/n pages", "esc back"}
		case c.pane == 0:
			hints = []string{"enter open", "J join", "N new", "f filter", "m mode", "G feed", "u search", "p/n pages"}
			if r.private.available(c) {
				hints = append(hints, "M roles", "W wall", "I invites")
			}
		case c.pane == 1:
			hints = []string{"i compose", "J join", "L leave", "R remember", "F forget", "e/E export", "ctrl+w close"}
		default:
			hints = []string{"enter inspect", "U actions", "p/n pages"}
		}
	case 2:
		if !c.supports("buddies") {
			break
		}
		if c.pane == 0 {
			hints = []string{"enter details", "a add", "e edit", "D remove", "f filter", "s sort", "u search", "p/n pages"}
		} else {
			hints = []string{"e edit", "D remove", "↑/↓ scroll", "U actions"}
		}
	case 3:
		d := c.discover
		switch {
		case c.pane == 0:
			hints = []string{"↑/↓ mode", "enter open"}
		case d.kind() == "profile":
			hints = []string{"e edit", "r refresh", "↑/↓ scroll"}
		case d.kind() == "interests":
			hints = []string{"a add", "e edit", "D remove", "f filter", "s search", "i recs", "u users"}
		default:
			hints = []string{"enter inspect", "f filter", "t target", "r refresh", "b browse", "m message", "p/n pages"}
		}
	}
	if len(hints) == 0 {
		hints = []string{"U actions", "esc back"}
	}
	return append(hints, common...)
}
