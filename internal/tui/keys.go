package tui

import (
	"net/url"
	"sort"
	"strings"
	"unicode"

	"charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if m.passwordForm {
		return m.passwordFormKey(k)
	}
	if m.statusMenu {
		return m.statusMenuKey(k)
	}
	if m.help {
		if s == "?" || s == "esc" {
			m.help = false
		}
		return nil
	}
	if m.details {
		if s == "i" || s == "esc" {
			m.details = false
		}
		return nil
	}
	if m.folderMenu {
		return m.folderMenuKey(k)
	}
	if m.interfaceChoosing {
		return m.interfaceChoiceKey(k)
	}
	if m.setup {
		return m.setupKey(k)
	}
	if m.confirm {
		if s == "y" || s == "enter" {
			return tea.Quit
		}
		if s == "n" || s == "esc" {
			m.confirm = false
		}
		return nil
	}
	if m.editing {
		return m.editKey(k)
	}
	switch s {
	case "q", "ctrl+c":
		if m.transient && m.active() {
			m.confirm = true
			return nil
		}
		return tea.Quit
	case "?":
		m.help = true
	case "o":
		m.openStatusMenu()
	case "ctrl+pgup":
		if m.workspace == workspaceSearch {
			m.switchSearchTab(-1)
		} else if m.workspace == workspaceBrowse {
			m.switchBrowseTab(-1)
		} else if m.workspace == workspaceTransfers {
			m.switchTransferTab(transferDownloads)
		}
	case "ctrl+pgdown":
		if m.workspace == workspaceSearch {
			m.switchSearchTab(1)
		} else if m.workspace == workspaceBrowse {
			m.switchBrowseTab(1)
		} else if m.workspace == workspaceTransfers {
			m.switchTransferTab(transferUploads)
		}
	case "ctrl+w":
		if m.workspace == workspaceSearch {
			m.closeSearchTab()
		} else if m.workspace == workspaceBrowse {
			m.closeBrowseTab()
		}
	case "tab":
		m.switchWorkspace(m.workspace + 1)
		if m.workspace == workspaceSettings && m.settingsSection == settingsConnection {
			return m.loadNetworkInterfaces()
		}
	case "shift+tab":
		m.switchWorkspace(m.workspace - 1)
		if m.workspace == workspaceSettings && m.settingsSection == settingsConnection {
			return m.loadNetworkInterfaces()
		}
	case "right":
		if m.workspace == workspaceSettings {
			m.settingsSection = (m.settingsSection + 1) % settingsSectionCount
			m.cursor, m.selected = 0, map[int]bool{}
			if m.settingsSection == settingsConnection {
				return m.loadNetworkInterfaces()
			}
		} else {
			return m.openTreeNode(false)
		}
	case "left":
		if m.workspace == workspaceSettings {
			m.settingsSection = (m.settingsSection + settingsSectionCount - 1) % settingsSectionCount
			m.cursor, m.selected = 0, map[int]bool{}
			if m.settingsSection == settingsConnection {
				return m.loadNetworkInterfaces()
			}
		} else if tree := m.currentTree(); tree != nil {
			m.cursor = tree.left(m.cursor)
		}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor+1 < m.rows() {
			m.cursor++
		} else if m.workspace == workspaceSearch && !m.loadingMore && m.searchNext > 0 {
			m.loadingMore = true
			return m.loadSearchPage()
		}
	case "pgup":
		m.cursor = max(0, m.cursor-m.pageRows())
	case "pgdown":
		if m.cursor+1 < m.rows() {
			m.cursor = min(m.rows()-1, m.cursor+m.pageRows())
		} else if m.workspace == workspaceSearch && !m.loadingMore && m.searchNext > 0 {
			m.loadingMore = true
			return m.loadSearchPage()
		}
	case "home":
		m.cursor = 0
	case "end":
		m.cursor = max(0, m.rows()-1)
	case "space":
		m.toggle()
	case "enter":
		if m.workspace == workspaceBrowse && len(m.browseTabs) == 0 {
			if m.cursor >= 0 && m.cursor < len(m.savedBrowses) {
				return m.openBrowse(m.savedBrowses[m.cursor].Username, "", false)
			}
			return nil
		}
		if m.workspace == workspaceSettings {
			fields := m.settingFields()
			if m.cursor >= len(fields) {
				return nil
			}
			switch fields[m.cursor].id {
			case settingNetworkInterface:
				m.interfaceChoosing = true
				m.interfaceChoice = m.configuredInterfaceChoice()
			case settingListeningPortStatus:
				if m.status.status != daemon.StatusConnected || m.status.publicPort == 0 {
					m.setNotice("Connect to check listening port")
					break
				}
				if m.portChecking {
					break
				}
				m.portCheckPort, m.portCheckStatus, m.portChecking, m.err = m.status.publicPort, "", true, ""
				return m.checkListeningPort()
			case settingConnectOnStartup:
				m.cfg.Soulseek.ConnectOnStartup = !m.cfg.Soulseek.ConnectOnStartup
			case settingNATPMPPortMapping:
				m.cfg.Soulseek.NATPMPPortMapping = !m.cfg.Soulseek.NATPMPPortMapping
			case settingUPnPPortMapping:
				m.cfg.Soulseek.UPnPPortMapping = !m.cfg.Soulseek.UPnPPortMapping
			case settingRememberSearches:
				m.cfg.Search.RememberSearches = !m.cfg.Search.RememberSearches
			case settingRememberFilters:
				m.cfg.Search.RememberFilters = !m.cfg.Search.RememberFilters
			case settingChangePassword:
				m.openPasswordForm()
			case settingClearSearchHistory:
				m.clearHistory(false)
			case settingClearFilterHistory:
				m.clearHistory(true)
			default:
				m.beginEdit()
			}
			return nil
		}
		return m.enter()
	case "b":
		if m.workspace == workspaceSearch {
			_, node := m.searchTree.node(m.cursor)
			if node != nil {
				folder := node.path
				if node.kind == treeFile {
					folder, _ = resultPath(folder)
				}
				return m.openBrowse(node.user, folder, false)
			}
		}
	case "i":
		if m.workspace == workspaceSearch || m.workspace == workspaceBrowse {
			_, node := m.currentTree().node(m.cursor)
			m.details = node != nil && node.kind == treeFile && node.source >= 0
		}
	case "d":
		if m.workspace == workspaceSearch {
			if m.openFolderMenu() {
				return nil
			}
			return m.queueResult()
		}
		if m.workspace == workspaceBrowse {
			if m.openFolderMenu() {
				return nil
			}
			return m.queueBrowse()
		}
		if m.workspace == workspaceTransfers {
			return m.action("cancel")
		}
		if m.workspace == workspaceShares {
			return m.removeShare()
		}
	case "r":
		if m.workspace == workspaceBrowse {
			if m.browseUser != "" {
				return m.openBrowse(m.browseUser, "", true)
			}
			m.savedBrowseCursor, m.savedBrowseLoading = m.cursor, true
			return m.loadSavedBrowses()
		}
		if m.workspace == workspaceTransfers {
			return m.action("retry")
		}
		if m.workspace == workspaceShares {
			return m.rescanShares()
		}
	case "c":
		if m.workspace == workspaceSearch {
			filter := m.searchFilterUndo
			if m.searchFilter != "" {
				filter = ""
			}
			if m.searchID == "" {
				m.searchFilter, m.searchFilterUndo = filter, m.searchFilter
				return nil
			}
			m.loading = true
			return m.filterSearch(filter)
		}
		if m.workspace == workspaceTransfers {
			return m.action("clear")
		}
	case "s":
		if m.workspace == workspaceBrowse && m.browseLoaded && !m.loading && m.browseRevision != 0 {
			return m.saveBrowse()
		}
		if m.workspace == workspaceSettings {
			return m.saveSettings()
		}
	case "f":
		if m.workspace == workspaceSearch {
			m.editing, m.filterEditing, m.input = true, true, m.searchFilter
			m.inputCursor = len([]rune(m.input))
			m.historyCursor.reset(m.input)
			return nil
		}
	case "/":
		m.beginEdit()
		return nil
	}
	return nil
}

func (m *model) openStatusMenu() {
	m.statusMenu, m.statusMenuChoice = true, 0
	for i, presence := range presenceChoices {
		if presence == m.status.presence {
			m.statusMenuChoice = i
			return
		}
	}
}

func (m *model) statusMenuKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "o":
		m.statusMenu = false
	case "up", "k":
		m.statusMenuChoice = max(0, m.statusMenuChoice-1)
	case "down", "j":
		m.statusMenuChoice = min(len(presenceChoices)-1, m.statusMenuChoice+1)
	case "enter":
		presence := presenceChoices[m.statusMenuChoice]
		m.statusMenu = false
		return m.setPresence(presence)
	}
	return nil
}

func (m *model) openPasswordForm() {
	if m.status.status != daemon.StatusConnected || m.status.user == "" {
		m.err = "Connect to Soulseek before changing the password"
		return
	}
	if m.cfg.Soulseek.Username != m.status.user {
		m.err = "Save or revert the username change before changing the password"
		return
	}
	m.passwordForm, m.passwordChanging = true, false
	m.passwordField, m.inputCursor = 0, 0
	m.passwordVals, m.passwordUser, m.passwordErr, m.err = [2]string{}, m.status.user, "", ""
}

func (m *model) closePasswordForm() {
	m.passwordForm, m.passwordChanging = false, false
	m.passwordField, m.inputCursor = 0, 0
	m.passwordVals, m.passwordUser, m.passwordErr = [2]string{}, "", ""
}

func (m *model) passwordFormKey(k tea.KeyPressMsg) tea.Cmd {
	if m.passwordChanging {
		return nil
	}
	s := k.String()
	switch s {
	case "esc":
		m.closePasswordForm()
		return nil
	case "up", "shift+tab":
		m.passwordField = max(0, m.passwordField-1)
		m.inputCursor = len([]rune(m.passwordVals[m.passwordField]))
		return nil
	case "down", "tab":
		m.passwordField = min(1, m.passwordField+1)
		m.inputCursor = len([]rune(m.passwordVals[m.passwordField]))
		return nil
	case "enter":
		if m.passwordField == 0 {
			m.passwordField = 1
			m.inputCursor = len([]rune(m.passwordVals[1]))
			return nil
		}
		password := m.passwordVals[0]
		if strings.TrimSpace(password) == "" {
			m.passwordErr = "password cannot be empty"
			return nil
		}
		if password != m.passwordVals[1] {
			m.passwordErr = "passwords do not match"
			return nil
		}
		m.passwordChanging, m.passwordErr = true, ""
		return m.changePassword(password)
	}
	before := m.passwordVals[m.passwordField]
	m.passwordVals[m.passwordField], m.inputCursor, _ = editText(before, m.inputCursor, k)
	if m.passwordVals[m.passwordField] != before {
		m.passwordErr = ""
	}
	return nil
}

func (m *model) openFolderMenu() bool {
	for _, selected := range m.selected {
		if selected {
			return false
		}
	}
	tree := m.currentTree()
	if tree == nil {
		return false
	}
	index, node := tree.node(m.cursor)
	if node == nil || node.kind != treeFolder || normalizeBrowsePath(node.path) == "" {
		return false
	}
	user := node.user
	if user == "" && m.workspace == workspaceBrowse {
		user = m.browseUser
	}
	if user == "" {
		return false
	}
	fileAt := func(source int) (download, bool) {
		if m.workspace == workspaceSearch && source >= 0 && source < len(m.results) && !m.results[source].directory {
			return download{filename: m.results[source].path, size: m.results[source].size}, true
		}
		if m.workspace == workspaceBrowse && source >= 0 && source < len(m.entries) && !m.entries[source].directory {
			return download{filename: m.entries[source].name, size: m.entries[source].size}, true
		}
		return download{}, false
	}
	var directFiles []download
	for _, child := range node.children {
		if tree.nodes[child].kind == treeFile {
			if file, ok := fileAt(tree.nodes[child].source); ok {
				directFiles = append(directFiles, file)
			}
		}
	}
	allFiles := make([]download, 0, len(node.leaves))
	for _, source := range node.leaves {
		if file, ok := fileAt(source); ok {
			allFiles = append(allFiles, file)
		}
	}
	var subfolders []string
	var visit func(int)
	visit = func(parent int) {
		for _, child := range tree.nodes[parent].children {
			if tree.nodes[child].kind != treeFolder {
				continue
			}
			subfolders = append(subfolders, normalizeBrowsePath(tree.nodes[child].path))
			visit(child)
		}
	}
	visit(index)
	m.folderMenu, m.folderMenuChoice = true, 0
	m.folderMenuUser, m.folderMenuPath = user, normalizeBrowsePath(node.path)
	m.folderMenuSubfolders, m.folderMenuFiles = subfolders, [2][]download{directFiles, allFiles}
	return true
}

func (m *model) folderMenuRequest() daemon.FolderDownloadRequest {
	recursive := m.folderMenuChoice == 1
	req := daemon.FolderDownloadRequest{Username: m.folderMenuUser, Folder: m.folderMenuPath, Recursive: recursive}
	if recursive {
		req.Subfolders = append([]string(nil), m.folderMenuSubfolders...)
	}
	for _, file := range m.folderMenuFiles[m.folderMenuChoice] {
		req.Files = append(req.Files, daemon.DownloadItem{Filename: file.filename, Size: file.size})
	}
	return req
}

func (m *model) folderMenuKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.folderMenu = false
	case "up", "k":
		m.folderMenuChoice = max(0, m.folderMenuChoice-1)
	case "down", "j":
		m.folderMenuChoice = min(1, m.folderMenuChoice+1)
	case "enter":
		req := m.folderMenuRequest()
		m.folderMenu = false
		return m.queueFolder(req)
	}
	return nil
}

func (m *model) interfaceChoiceKey(k tea.KeyPressMsg) tea.Cmd {
	count := len(m.networkInterfaces) + 2
	switch k.String() {
	case "left":
		m.interfaceChoice = (m.interfaceChoice + count - 1) % count
	case "right":
		m.interfaceChoice = (m.interfaceChoice + 1) % count
	case "esc":
		m.interfaceChoosing = false
	case "enter":
		choice := m.interfaceChoice
		m.interfaceChoosing = false
		if choice == 0 {
			m.cfg.Soulseek.NetworkInterface = ""
		} else if choice <= len(m.networkInterfaces) {
			m.cfg.Soulseek.NetworkInterface = m.networkInterfaces[choice-1]
		} else {
			m.editing = true
			m.input = ""
			if m.configuredInterfaceChoice() == len(m.networkInterfaces)+1 {
				m.input = m.cfg.Soulseek.NetworkInterface
			}
			m.inputCursor = len([]rune(m.input))
		}
	}
	return nil
}

func (m *model) beginEdit() {
	if m.workspace == workspaceSettings {
		fields := m.settingFields()
		if m.cursor >= len(fields) || fields[m.cursor].kind == settingBool || fields[m.cursor].kind == settingAction || fields[m.cursor].kind == settingInfo || fields[m.cursor].kind == settingChoice {
			return
		}
	}
	m.editing, m.filterEditing = true, false
	switch m.workspace {
	case workspaceSearch:
		m.input = m.query
	case workspaceBrowse:
		m.input = m.browseUser
	case workspaceSettings:
		fields := m.settingFields()
		if m.cursor < len(fields) {
			m.input = fields[m.cursor].value
		}
	default:
		m.input = ""
	}
	m.inputCursor = len([]rune(m.input))
	if m.workspace == workspaceSearch {
		m.historyCursor.reset(m.input)
	}
}
func (m *model) editKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if m.workspace == workspaceSearch && (s == "up" || s == "down") {
		m.recallHistory(s == "up")
		return nil
	}
	if m.filterEditing && (s == "tab" || s == "shift+tab") {
		before := inputBeforeCursor(m.input, m.inputCursor)
		after := []rune(m.input)[len([]rune(before)):]
		completed := completeFilter(before, s == "shift+tab")
		m.input = completed + string(after)
		m.inputCursor = len([]rune(completed))
		m.historyCursor.reset(m.input)
		return nil
	}
	if s == "esc" {
		m.editing, m.filterEditing = false, false
		m.input = ""
		return nil
	}
	if s == "enter" {
		filterEditing := m.filterEditing
		m.editing, m.filterEditing = false, false
		if filterEditing {
			filter := strings.TrimSpace(m.input)
			if err := daemon.ValidateSearchFilter(filter); err != nil {
				m.err = err.Error()
				return nil
			}
			m.recordHistory(filter, true)
			if m.searchID == "" {
				m.searchFilter = filter
				m.err = ""
				return nil
			}
			m.loading = true
			return m.filterSearch(filter)
		}
		if m.workspace == workspaceSearch {
			return m.openSearch(m.input)
		}
		if m.workspace == workspaceBrowse {
			return m.openBrowse(m.input, "", false)
		}
		if m.workspace == workspaceShares {
			return m.addShare()
		}
		if m.workspace == workspaceSettings {
			value := strings.TrimSpace(m.input)
			if err := m.setSettingValue(value); err != nil {
				m.err = err.Error()
				m.editing = m.settingFields()[m.cursor].id == settingNetworkInterface
			} else {
				m.err = ""
			}
			return nil
		}
		return nil
	}
	before := m.input
	m.input, m.inputCursor, _ = editText(m.input, m.inputCursor, k)
	if m.workspace == workspaceSearch && m.input != before {
		m.historyCursor.reset(m.input)
	}
	return nil
}

var filterFields = []string{"in:", "out:", "type:", "country:", "size:", "bitrate:", "duration:", "free:", "public:"}
var filterTypes = []string{"audio", "video", "image", "document", "text", "archive", "executable"}
var filterComparisons = []string{">=", "<=", "=", "==", "!=", ">", "<"}

func completeFilter(input string, reverse bool) string {
	start := strings.LastIndexAny(input, " \t\n") + 1
	token := input[start:]
	field, value, hasValue := strings.Cut(token, ":")
	if !hasValue {
		return input[:start] + cycleCompletion(token, filterFields, reverse)
	}
	var options []string
	switch field {
	case "type":
		valueStart := strings.LastIndex(value, ",") + 1
		prefix := value[valueStart:]
		negated := strings.HasPrefix(prefix, "!")
		prefix = strings.TrimPrefix(prefix, "!")
		completed := cycleCompletion(prefix, filterTypes, reverse)
		if negated {
			completed = "!" + completed
		}
		return input[:start] + field + ":" + value[:valueStart] + completed
	case "free", "public":
		options = []string{"true", "false"}
	case "size", "bitrate", "duration":
		options = filterComparisons
	case "in", "out":
		if value == "" {
			return input[:start] + cycleCompletion(token, filterFields, reverse)
		}
		return input
	default:
		return input
	}
	return input[:start] + field + ":" + cycleCompletion(value, options, reverse)
}

func cycleCompletion(prefix string, options []string, reverse bool) string {
	for i, option := range options {
		if option == prefix {
			if reverse {
				return options[(i+len(options)-1)%len(options)]
			}
			return options[(i+1)%len(options)]
		}
	}
	if reverse {
		for i := len(options) - 1; i >= 0; i-- {
			if strings.HasPrefix(options[i], prefix) {
				return options[i]
			}
		}
	} else {
		for _, option := range options {
			if strings.HasPrefix(option, prefix) {
				return option
			}
		}
	}
	return prefix
}

func filterCompletionHint(input string) string {
	start := strings.LastIndexAny(input, " \t\n") + 1
	field, _, ok := strings.Cut(input[start:], ":")
	if !ok {
		return "tab complete: " + strings.Join(filterFields, "  ")
	}
	switch field {
	case "type":
		return "tab complete: " + strings.Join(filterTypes, "  ")
	case "free", "public":
		return "tab complete: true  false"
	case "size", "bitrate", "duration":
		return "tab complete: " + strings.Join(filterComparisons, "  ")
	case "in", "out":
		return "value: case-insensitive regular expression"
	default:
		return "tab complete: " + strings.Join(filterFields, "  ")
	}
}
func (m *model) enter() tea.Cmd {
	if m.workspace == workspaceBrowse && len(m.browseTabs) == 0 {
		return nil
	}
	tree := m.currentTree()
	if tree == nil {
		return nil
	}
	_, node := tree.node(m.cursor)
	if node == nil {
		return nil
	}
	if node.kind != treeFile {
		return m.openTreeNode(true)
	}
	if node.source >= 0 && (m.workspace == workspaceSearch || m.workspace == workspaceBrowse) {
		m.selected[node.source] = true
		if m.workspace == workspaceSearch {
			return m.queueResult()
		}
		return m.queueBrowse()
	}
	return nil
}

func (m *model) toggle() {
	if m.workspace != workspaceSearch && m.workspace != workspaceBrowse {
		return
	}
	tree := m.currentTree()
	index, node := tree.node(m.cursor)
	if node == nil {
		return
	}
	chosen, total := tree.selection(index, m.selected)
	selectAll := chosen != total
	for _, source := range node.leaves {
		m.selected[source] = selectAll
	}
}
func (m *model) queueResult() tea.Cmd {
	byUser := make(map[string][]daemon.DownloadItem)
	for i, x := range m.results {
		if m.selected[i] && !x.directory {
			byUser[x.user] = append(byUser[x.user], daemon.DownloadItem{Filename: x.path, Size: x.size})
		}
	}
	if len(byUser) == 0 {
		_, node := m.searchTree.node(m.cursor)
		if node != nil {
			for _, source := range node.leaves {
				x := m.results[source]
				byUser[x.user] = append(byUser[x.user], daemon.DownloadItem{Filename: x.path, Size: x.size})
			}
		}
	}
	if len(byUser) == 0 {
		return nil
	}
	requests := make([]daemon.DownloadRequest, 0, len(byUser))
	for user, files := range byUser {
		requests = append(requests, daemon.DownloadRequest{Username: user, Files: files})
	}
	return func() tea.Msg {
		_, err := m.client.QueueDownloads(m.ctx, requests)
		return statusMsg{err: err}
	}
}
func (m *model) queueBrowse() tea.Cmd {
	chosen := make(map[string]download)
	for source, item := range m.entries {
		if m.selected[source] && !item.directory {
			chosen[item.name] = download{item.name, item.size}
		}
	}
	if len(chosen) == 0 {
		_, node := m.browseTree.node(m.cursor)
		if node != nil {
			for _, source := range node.leaves {
				item := m.entries[source]
				chosen[item.name] = download{item.name, item.size}
			}
		}
	}
	if len(chosen) == 0 {
		return nil
	}
	files := make([]download, 0, len(chosen))
	for _, file := range chosen {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].filename < files[j].filename })
	return m.queue(files, m.browseUser)
}
func (m *model) addShare() tea.Cmd {
	p := strings.SplitN(strings.TrimSpace(m.input), ":", 2)
	if len(p) != 2 || strings.TrimSpace(p[0]) == "" || strings.TrimSpace(p[1]) == "" {
		m.err = "share format is name:path"
		return nil
	}
	return func() tea.Msg {
		x, e := m.client.AddShare(m.ctx, config.Share{Name: strings.TrimSpace(p[0]), Path: strings.TrimSpace(p[1])})
		return sharesMsg{shares: toShares(x), err: e}
	}
}
func (m *model) removeShare() tea.Cmd {
	_, node := m.shareTree.node(m.cursor)
	if node == nil || node.kind != treeShareRoot || node.source < 0 || node.source >= len(m.shares) {
		return nil
	}
	name := m.shares[node.source].name
	return func() tea.Msg {
		var out []config.Share
		e := m.client.Do(m.ctx, "DELETE", "/v1/shares/"+url.PathEscape(name), nil, &out)
		return sharesMsg{shares: toShares(out), err: e}
	}
}

func (m *model) queue(files []download, user string) tea.Cmd {
	return func() tea.Msg {
		items := make([]daemon.DownloadItem, len(files))
		for i, f := range files {
			items[i] = daemon.DownloadItem{Filename: f.filename, Size: f.size}
		}
		_, e := m.client.QueueDownloads(m.ctx, []daemon.DownloadRequest{{Username: user, Files: items}})
		return statusMsg{err: e}
	}
}

func (m *model) queueFolder(req daemon.FolderDownloadRequest) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.QueueFolder(m.ctx, req)
		return folderDownloadMsg{err: err}
	}
}

func (m model) transferActionIDs() []string {
	tree := &m.transferTrees[m.transferTab]
	_, node := tree.node(m.cursor)
	if node == nil {
		return nil
	}
	ids := make([]string, 0, len(node.leaves))
	for _, source := range node.leaves {
		ids = append(ids, m.transfers[source].id)
	}
	return ids
}
func (m model) action(action string) tea.Cmd {
	ids := m.transferActionIDs()
	if len(ids) == 0 {
		return nil
	}
	return func() tea.Msg {
		var first error
		for _, id := range ids {
			if err := m.client.TransferAction(m.ctx, id, action); err != nil && first == nil {
				first = err
			}
		}
		transfers, err := m.client.Transfers(m.ctx)
		view := toTransfers(transfers)
		if err != nil {
			view = m.transfers
		}
		if first == nil {
			first = err
		}
		return transferActionMsg{transfers: view, err: first}
	}
}
func (m model) active() bool {
	for _, x := range m.transfers {
		if x.state == "queued" || x.state == "incomplete" || x.state == "running" {
			return true
		}
	}
	return false
}

func inputBeforeCursor(value string, cursor int) string {
	runes := []rune(value)
	return string(runes[:max(0, min(cursor, len(runes)))])
}

func insertText(value, text string, cursor int) (string, int) {
	runes, added := []rune(value), []rune(text)
	cursor = max(0, min(cursor, len(runes)))
	out := make([]rune, 0, len(runes)+len(added))
	out = append(out, runes[:cursor]...)
	out = append(out, added...)
	out = append(out, runes[cursor:]...)
	return string(out), cursor + len(added)
}

func deleteBeforeCursor(value string, cursor int) (string, int) {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	if cursor == 0 {
		return value, cursor
	}
	return string(append(runes[:cursor-1], runes[cursor:]...)), cursor - 1
}

func deleteAtCursor(value string, cursor int) string {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	if cursor == len(runes) {
		return value
	}
	return string(append(runes[:cursor], runes[cursor+1:]...))
}

func wordBefore(value string, cursor int) int {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	for cursor > 0 && unicode.IsSpace(runes[cursor-1]) {
		cursor--
	}
	for cursor > 0 && !unicode.IsSpace(runes[cursor-1]) {
		cursor--
	}
	return cursor
}

func wordAfter(value string, cursor int) int {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	for cursor < len(runes) && unicode.IsSpace(runes[cursor]) {
		cursor++
	}
	for cursor < len(runes) && !unicode.IsSpace(runes[cursor]) {
		cursor++
	}
	return cursor
}

func editText(value string, cursor int, k tea.KeyPressMsg) (string, int, bool) {
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	switch k.String() {
	case "left", "ctrl+b":
		cursor = max(0, cursor-1)
	case "right", "ctrl+f":
		cursor = min(len(runes), cursor+1)
	case "ctrl+left", "alt+left", "alt+b":
		cursor = wordBefore(value, cursor)
	case "ctrl+right", "alt+right", "alt+f":
		cursor = wordAfter(value, cursor)
	case "home", "ctrl+a":
		cursor = 0
	case "end", "ctrl+e":
		cursor = len(runes)
	case "backspace", "ctrl+h":
		value, cursor = deleteBeforeCursor(value, cursor)
	case "delete", "ctrl+d":
		value = deleteAtCursor(value, cursor)
	case "ctrl+backspace", "alt+backspace", "ctrl+w":
		start := wordBefore(value, cursor)
		value, cursor = string(append(runes[:start], runes[cursor:]...)), start
	case "ctrl+delete", "alt+delete", "alt+d":
		value = string(append(runes[:cursor], runes[wordAfter(value, cursor):]...))
	case "ctrl+u":
		value, cursor = string(runes[cursor:]), 0
	case "ctrl+k":
		value = string(runes[:cursor])
	default:
		if text := k.Key().Text; text != "" {
			value, cursor = insertText(value, text, cursor)
			return value, cursor, true
		}
		return value, cursor, false
	}
	return value, cursor, true
}

func (m *model) selectSetupField(field int) {
	m.setupField = (field + len(m.setupVals)) % len(m.setupVals)
	m.inputCursor = len([]rune(m.setupVals[m.setupField]))
}

func (m *model) setupKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "esc" {
		return tea.Quit
	}
	switch s {
	case "tab", "down":
		m.selectSetupField(m.setupField + 1)
		return nil
	case "shift+tab", "up":
		m.selectSetupField(m.setupField - 1)
		return nil
	}
	if s == "enter" {
		if m.setupField < 5 {
			m.selectSetupField(m.setupField + 1)
			return nil
		}
		cfg := config.Default()
		cfg.Soulseek.Username = strings.TrimSpace(m.setupVals[0])
		cfg.Soulseek.Password = m.setupVals[1]
		cfg.Soulseek.ListenAddr = strings.TrimSpace(m.setupVals[2])
		cfg.Soulseek.NetworkInterface = strings.TrimSpace(m.setupVals[3])
		cfg.DownloadDir = strings.TrimSpace(m.setupVals[4])
		if x := strings.TrimSpace(m.setupVals[5]); x != "" {
			p := strings.SplitN(x, ":", 2)
			if len(p) != 2 {
				m.setupErr = "share must be name:path"
				return nil
			}
			cfg.Shares = []config.Share{{Name: strings.TrimSpace(p[0]), Path: strings.TrimSpace(p[1])}}
		}
		if cfg.Soulseek.Username == "" || cfg.Soulseek.Password == "" {
			m.setupErr = "username and password are required"
			return nil
		}
		if err := cfg.Save(m.configPath); err != nil {
			m.setupErr = err.Error()
			return nil
		}
		if m.client == nil {
			return tea.Quit
		}
		m.setup = false
		return nil
	}
	m.setupVals[m.setupField], m.inputCursor, _ = editText(m.setupVals[m.setupField], m.inputCursor, k)
	return nil
}
