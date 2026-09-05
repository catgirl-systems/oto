package tui

import (
	"fmt"
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
	if m.workspace == workspaceStats && (m.stats.prune || m.stats.edit != "" || m.stats.detail != nil) {
		return m.statsKey(k)
	}
	if m.searchScope != nil {
		return m.searchScopeKey(k)
	}
	if m.passwordForm {
		return m.passwordFormKey(k)
	}
	if m.statusMenu {
		return m.statusMenuKey(k)
	}
	if m.uploadStatusMenu {
		return m.uploadStatusMenuKey(k)
	}
	if m.uploadConfirm {
		return m.uploadConfirmKey(k)
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
	if m.choiceChoosing {
		return m.settingChoiceKey(k)
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
	if m.workspace == workspaceStats && s != "tab" && s != "shift+tab" && s != "q" && s != "ctrl+c" && s != "?" && s != "o" {
		return m.statsKey(k)
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
	case "u":
		m.openSearchScope()
	case "F":
		m.confirmForceDownloads()
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
		if m.workspace == workspaceWishlist {
			if m.cursor >= 0 && m.cursor < len(m.wishlist) {
				m.wishlistCursor = m.cursor
				return m.openWishlist(m.wishlist[m.cursor], false)
			}
			return nil
		}
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
			case settingAudioMetadata:
				m.cfg.AudioMetadata = !m.cfg.AudioMetadata
			case settingStatsASCII:
				m.cfg.Statistics.ASCIICharts = !m.cfg.Statistics.ASCIICharts
			case settingStatsPrune:
				m.switchWorkspace(workspaceStats)
				m.openStatsPrune()
			case settingDownloadFilters:
				m.cfg.Downloads.FiltersEnabled = !m.cfg.Downloads.FiltersEnabled
			case settingRestoreDownloadRules:
				m.restoreDownloadRules = true
				m.uploadConfirm, m.uploadConfirmChoice = true, 0
				m.uploadConfirmLabel = "Restore default download filters"
			case settingRestoreShareExclusions:
				m.restoreShareExclusions = true
				m.uploadConfirm, m.uploadConfirmChoice = true, 0
				m.uploadConfirmLabel = "Restore default share exclusions (staged until s)"
			case settingNetworkInterface, settingBandwidthProfile, settingUploadLimitScope, settingUploadScheduling:
				m.choiceChoosing = true
				m.choiceSetting = fields[m.cursor].id
				m.choiceIndex = m.configuredChoice(m.choiceSetting)
			case settingDeleteBandwidthProfile:
				if len(m.cfg.Bandwidth.Profiles) == 1 {
					m.setNotice("At least one bandwidth profile is required")
					break
				}
				i := m.activeBandwidthProfileIndex()
				m.cfg.Bandwidth.Profiles = append(m.cfg.Bandwidth.Profiles[:i], m.cfg.Bandwidth.Profiles[i+1:]...)
				m.cfg.Bandwidth.ActiveProfile = m.cfg.Bandwidth.Profiles[min(i, len(m.cfg.Bandwidth.Profiles)-1)].Name
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
			case settingRespondToIncomingSearches:
				m.cfg.Search.RespondToIncomingSearches = !m.cfg.Search.RespondToIncomingSearches
			case settingRememberSearches:
				m.cfg.Search.RememberSearches = !m.cfg.Search.RememberSearches
			case settingRememberFilters:
				m.cfg.Search.RememberFilters = !m.cfg.Search.RememberFilters
			case settingWishlistNotifications:
				m.cfg.Search.WishlistNotifications = !m.cfg.Search.WishlistNotifications
			case settingFileNotifications:
				m.cfg.Downloads.FileNotifications = !m.cfg.Downloads.FileNotifications
			case settingFolderNotifications:
				m.cfg.Downloads.FolderNotifications = !m.cfg.Downloads.FolderNotifications
			case settingAutoClearDownloads:
				m.cfg.Downloads.AutoClearCompleted = !m.cfg.Downloads.AutoClearCompleted
			case settingAutoClearUploads:
				m.cfg.Uploads.AutoClearCompleted = !m.cfg.Uploads.AutoClearCompleted
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
		if m.workspace == workspaceSettings && m.settingsSection == settingsDownloads {
			if i := m.downloadRuleIndex(); i >= 0 {
				rules := append([]string{}, m.cfg.Downloads.FilterPatterns...)
				m.cfg.Downloads.FilterPatterns = append(rules[:i], rules[i+1:]...)
			}
			return nil
		}
		if m.workspace == workspaceSettings && m.settingsSection == settingsShares {
			if m.cursor >= 0 && m.cursor < len(m.cfg.ShareExclusions) {
				rules := append([]string{}, m.cfg.ShareExclusions...)
				m.cfg.ShareExclusions = append(rules[:m.cursor], rules[m.cursor+1:]...)
			}
			return nil
		}
		if m.workspace == workspaceWishlist && m.cursor >= 0 && m.cursor < len(m.wishlist) {
			m.wishlistCursor = m.cursor
			return m.removeWishlist(m.wishlist[m.cursor].ID)
		}
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
			if m.transferTab == transferUploads {
				return m.beginUploadAction("cancel", false)
			}
			return m.action("cancel")
		}
		if m.workspace == workspaceShares {
			return m.removeShare()
		}
	case "r":
		if m.workspace == workspaceWishlist && m.cursor >= 0 && m.cursor < len(m.wishlist) {
			m.wishlistCursor = m.cursor
			return m.openWishlist(m.wishlist[m.cursor], true)
		}
		if m.workspace == workspaceBrowse {
			if m.browseUser != "" {
				return m.openBrowse(m.browseUser, "", true)
			}
			m.savedBrowseCursor, m.savedBrowseLoading = m.cursor, true
			return m.loadSavedBrowses()
		}
		if m.workspace == workspaceTransfers {
			if m.transferTab == transferDownloads {
				return m.action("resume")
			}
			return m.beginUploadAction("retry", false)
		}
		if m.workspace == workspaceShares {
			return m.rescanShares()
		}
	case "p":
		if m.workspace == workspaceTransfers && m.transferTab == transferDownloads {
			return m.action("pause")
		}
	case "c":
		if m.workspace == workspaceShares {
			return m.cancelShareScan()
		}
		if m.workspace == workspaceSearch {
			filter := m.searchFilterUndo
			if m.searchFilter != "" {
				filter = ""
			}
			if m.searchID == "" {
				m.searchFilter, m.searchFilterUndo = filter, m.searchFilter
				if len(m.searchTabs) == 0 {
					m.searchPreFilterSet = true
				} else if m.searchTabIndex >= 0 && m.searchTabIndex < len(m.searchTabs) {
					m.searchTabs[m.searchTabIndex].filter = filter
				}
				return nil
			}
			m.loading = true
			return m.filterSearch(filter)
		}
		if m.workspace == workspaceTransfers {
			if m.transferTab == transferUploads {
				return m.beginUploadAction("clear", true)
			}
			return m.action("clear")
		}
	case "D":
		if m.workspace == workspaceTransfers && m.transferTab == transferUploads {
			return m.beginUploadAction("cancel", true)
		}
	case "C":
		if m.workspace == workspaceTransfers && m.transferTab == transferUploads {
			m.openUploadStatusMenu()
		}
	case "S":
		if m.workspace == workspaceTransfers {
			return m.prepareTransferSearch(true)
		}
	case "s":
		if m.workspace == workspaceTransfers {
			return m.prepareTransferSearch(false)
		}
		if m.workspace == workspaceBrowse && m.browseLoaded && !m.loading && m.browseRevision != 0 {
			return m.saveBrowse()
		}
		if m.workspace == workspaceSettings {
			return m.saveSettings()
		}
	case "f":
		if m.workspace == workspaceWishlist && m.cursor >= 0 && m.cursor < len(m.wishlist) {
			m.wishlistCursor = m.cursor
			m.editing, m.filterEditing, m.input = true, true, m.wishlist[m.cursor].Filter
			m.inputCursor = len([]rune(m.input))
			m.historyCursor.reset(m.input)
			return nil
		}
		if m.workspace == workspaceSearch {
			m.editing, m.filterEditing, m.input = true, true, m.searchFilter
			m.inputCursor = len([]rune(m.input))
			m.historyCursor.reset(m.input)
			return nil
		}
		if m.workspace == workspaceBrowse && m.browseLoaded && len(m.browseTabs) > 0 {
			m.editing, m.filterEditing, m.browseFindEditing = true, false, true
			m.input = m.browseFilter
			m.inputCursor = len([]rune(m.input))
			return nil
		}
	case "w":
		if m.workspace == workspaceSearch && strings.TrimSpace(m.query) != "" {
			if len(m.searchUsers()) > 0 {
				m.setNotice("Wishlist searches are global; targeted searches cannot be saved")
				return nil
			}
			return m.putWishlist(m.query, m.searchFilter, "Saved search to Wishlist")
		}
	case "/":
		m.beginEdit()
		return nil
	}
	return nil
}

func (m *model) prepareTransferSearch(folder bool) tea.Cmd {
	_, node := m.transferTrees[m.transferTab].node(m.cursor)
	if node == nil || (node.kind != treeFile && node.kind != treeFolder) {
		m.setNotice("Select a transfer file or folder first")
		return nil
	}
	parts := treeParts(node.path)
	if folder && node.kind == treeFile && len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		m.setNotice("Selected transfer has no containing folder")
		return nil
	}
	query := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, parts[len(parts)-1])
	if node.kind == treeFile && !folder {
		if i := strings.LastIndexByte(query, '.'); i > 0 {
			query = query[:i]
		}
	}
	if strings.TrimSpace(query) == "" {
		m.setNotice("Selected transfer has no search name")
		return nil
	}
	m.workspace = workspaceSearch
	if len(m.searchTabs) > 0 {
		m.loadSearchTab(m.searchTabIndex)
	} else {
		m.searchFilter, m.searchFilterUndo, m.searchPreFilterSet = "", "", false
		m.cursor = 0
	}
	m.beginEdit()
	m.input, m.inputCursor = query, len([]rune(query))
	m.historyCursor.reset(query)
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
	if m.workspace == workspaceBrowse && m.browseFilter != "" {
		fullTree, _ := buildBrowseTree(m.entries, "", treeState{}, 0)
		fullIndex, ok := fullTree.byID[node.id]
		if !ok {
			return false
		}
		tree, index = &fullTree, fullIndex
		node = &tree.nodes[index]
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
	m.folderMenu, m.folderMenuEditing, m.folderMenuChoice = true, false, 0
	m.folderMenuUser, m.folderMenuPath = user, normalizeBrowsePath(node.path)
	m.folderMenuDownloadDir = m.cfg.DownloadDir
	m.folderMenuSubfolders, m.folderMenuFiles = subfolders, [2][]download{directFiles, allFiles}
	return true
}

func (m *model) folderMenuRequest() daemon.FolderDownloadRequest {
	recursive := m.folderMenuChoice == 1
	req := daemon.FolderDownloadRequest{Username: m.folderMenuUser, DownloadDir: strings.TrimSpace(m.folderMenuDownloadDir), Folder: m.folderMenuPath, Recursive: recursive}
	if recursive {
		req.Subfolders = append([]string(nil), m.folderMenuSubfolders...)
	}
	for _, file := range m.folderMenuFiles[m.folderMenuChoice] {
		req.Files = append(req.Files, daemon.DownloadItem{Filename: file.filename, Size: file.size})
	}
	return req
}

func (m *model) folderMenuKey(k tea.KeyPressMsg) tea.Cmd {
	if m.folderMenuEditing {
		switch k.String() {
		case "esc", "enter":
			m.folderMenuEditing = false
			return nil
		}
		m.folderMenuDownloadDir, m.inputCursor, _ = editText(m.folderMenuDownloadDir, m.inputCursor, k)
		return nil
	}
	switch k.String() {
	case "esc":
		m.folderMenu = false
	case "up", "k":
		m.folderMenuChoice = max(0, m.folderMenuChoice-1)
	case "down", "j":
		m.folderMenuChoice = min(1, m.folderMenuChoice+1)
	case "/":
		m.folderMenuEditing = true
		m.inputCursor = len([]rune(m.folderMenuDownloadDir))
	case "enter":
		req := m.folderMenuRequest()
		m.folderMenu = false
		return m.queueFolder(req)
	}
	return nil
}

func (m *model) settingChoiceKey(k tea.KeyPressMsg) tea.Cmd {
	options := m.choiceOptions(m.choiceSetting)
	count := len(options)
	if count == 0 {
		m.choiceChoosing = false
		return nil
	}
	switch k.String() {
	case "left":
		m.choiceIndex = (m.choiceIndex + count - 1) % count
	case "right":
		m.choiceIndex = (m.choiceIndex + 1) % count
	case "esc":
		m.choiceChoosing = false
	case "enter":
		id, choice := m.choiceSetting, m.choiceIndex
		m.choiceChoosing = false
		switch id {
		case settingNetworkInterface:
			if choice == 0 {
				m.cfg.Soulseek.NetworkInterface = ""
			} else if choice <= len(m.networkInterfaces) {
				m.cfg.Soulseek.NetworkInterface = m.networkInterfaces[choice-1]
			} else {
				m.editing = true
				m.input = ""
				if m.configuredChoice(settingNetworkInterface) == len(m.networkInterfaces)+1 {
					m.input = m.cfg.Soulseek.NetworkInterface
				}
				m.inputCursor = len([]rune(m.input))
			}
		case settingBandwidthProfile:
			if choice == len(m.cfg.Bandwidth.Profiles) {
				m.editing, m.addingBandwidthProfile = true, true
				m.input, m.inputCursor = "", 0
			} else {
				m.cfg.Bandwidth.ActiveProfile = m.cfg.Bandwidth.Profiles[choice].Name
			}
		case settingUploadLimitScope:
			if choice == 0 {
				m.cfg.Uploads.LimitScope = config.UploadLimitTotal
			} else {
				m.cfg.Uploads.LimitScope = config.UploadLimitPerTransfer
			}
		case settingUploadScheduling:
			m.cfg.Uploads.Scheduling = []config.UploadScheduling{config.UploadSchedulingFIFO, config.UploadSchedulingRoundRobin, config.UploadSchedulingRandom, config.UploadSchedulingSmallestFirst}[choice]
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
	m.editing, m.filterEditing, m.browseFindEditing = true, false, false
	switch m.workspace {
	case workspaceSearch:
		m.input = m.query
	case workspaceBrowse:
		m.input = m.browseUser
	case workspaceSettings:
		fields := m.settingFields()
		if m.cursor < len(fields) {
			m.input = fields[m.cursor].value
			m.filterEditing = fields[m.cursor].id == settingDefaultFilter
		}
	default:
		m.input = ""
	}
	m.inputCursor = len([]rune(m.input))
	if m.workspace == workspaceSearch || m.workspace == workspaceWishlist {
		m.historyCursor.reset(m.input)
	}
}
func (m *model) editKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if (m.workspace == workspaceSearch || m.workspace == workspaceWishlist) && (s == "up" || s == "down") {
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
		m.editing, m.filterEditing, m.browseFindEditing, m.addingBandwidthProfile = false, false, false, false
		m.input = ""
		return nil
	}
	if s == "enter" {
		filterEditing, browseFindEditing := m.filterEditing, m.browseFindEditing
		m.editing, m.filterEditing, m.browseFindEditing = false, false, false
		if browseFindEditing {
			filter := strings.TrimSpace(m.input)
			if filter != m.browseFilter {
				m.browseFilter, m.selected = filter, map[int]bool{}
				m.browseTree, m.cursor = buildBrowseTree(m.entries, filter, m.browseTree, m.cursor)
				m.saveBrowseTab()
			}
			m.input = ""
			return nil
		}
		if filterEditing {
			filter := strings.TrimSpace(m.input)
			if err := daemon.ValidateSearchFilter(filter); err != nil {
				m.err = err.Error()
				return nil
			}
			if m.workspace == workspaceSettings {
				if err := m.setSettingValue(filter); err != nil {
					m.err = err.Error()
					return nil
				}
				m.editing, m.filterEditing, m.input, m.err = false, false, "", ""
				return nil
			}
			m.recordHistory(filter, true)
			if m.workspace == workspaceWishlist {
				if m.wishlistCursor >= 0 && m.wishlistCursor < len(m.wishlist) {
					item := m.wishlist[m.wishlistCursor]
					return m.putWishlist(item.Query, filter, "Wishlist filter saved")
				}
				return nil
			}
			if m.searchID == "" {
				m.searchFilter = filter
				if len(m.searchTabs) == 0 {
					m.searchPreFilterSet = true
				} else if m.searchTabIndex >= 0 && m.searchTabIndex < len(m.searchTabs) {
					m.searchTabs[m.searchTabIndex].filter = filter
				}
				m.err = ""
				return nil
			}
			m.loading = true
			return m.filterSearch(filter)
		}
		if m.workspace == workspaceSearch {
			return m.openSearch(m.input)
		}
		if m.workspace == workspaceWishlist {
			query := strings.TrimSpace(m.input)
			if query == "" {
				return nil
			}
			return m.putWishlist(query, "", "Wishlist item added")
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
				m.addingBandwidthProfile = false
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
	if (m.workspace == workspaceSearch || m.workspace == workspaceWishlist) && m.input != before {
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
	if m.workspace == workspaceTransfers && m.transferTab == transferDownloads {
		m.toggleDownloadSelection()
		return
	}
	if m.workspace == workspaceTransfers && m.transferTab == transferUploads {
		m.toggleUploadSelection()
		return
	}
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
	if m.transferTab == transferUploads {
		return m.uploadAction(daemon.UploadActionRequest{Action: action, IDs: m.uploadActionIDs()})
	}
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

func (m model) uploadActionIDs() []string {
	ids := make([]string, 0)
	for _, x := range m.transfers {
		if x.direction == "upload" && x.id != "" && m.uploadSelected[x.id] {
			ids = append(ids, x.id)
		}
	}
	if len(ids) > 0 {
		return ids
	}
	tree := &m.transferTrees[transferUploads]
	_, node := tree.node(m.cursor)
	if node == nil {
		return nil
	}
	for _, source := range node.leaves {
		if source >= 0 && source < len(m.transfers) && m.transfers[source].direction == "upload" && m.transfers[source].id != "" {
			ids = append(ids, m.transfers[source].id)
		}
	}
	return ids
}

func (m model) uploadActionUsernames() []string {
	seen := map[string]bool{}
	users := make([]string, 0)
	ids := make(map[string]bool)
	for _, id := range m.uploadActionIDs() {
		ids[id] = true
	}
	for _, x := range m.transfers {
		if ids[x.id] && x.user != "" && !seen[x.user] {
			seen[x.user] = true
			users = append(users, x.user)
		}
	}
	return users
}

func (m *model) toggleUploadSelection() {
	if m.uploadSelected == nil {
		m.uploadSelected = map[string]bool{}
	}
	tree := &m.transferTrees[transferUploads]
	index, node := tree.node(m.cursor)
	if node == nil {
		return
	}
	chosen, total := 0, 0
	for _, source := range node.leaves {
		if source >= 0 && source < len(m.transfers) && m.transfers[source].direction == "upload" {
			total++
			if m.uploadSelected[m.transfers[source].id] {
				chosen++
			}
		}
	}
	selectAll := chosen != total
	for _, source := range tree.nodes[index].leaves {
		if source >= 0 && source < len(m.transfers) {
			if id := m.transfers[source].id; m.transfers[source].direction == "upload" && id != "" {
				m.uploadSelected[id] = selectAll
			}
		}
	}
}

func (m *model) pruneUploadSelection() {
	if m.uploadSelected == nil {
		return
	}
	present := make(map[string]bool)
	for _, x := range m.transfers {
		if x.direction == "upload" && x.id != "" {
			present[x.id] = true
		}
	}
	for id := range m.uploadSelected {
		if !present[id] {
			delete(m.uploadSelected, id)
		}
	}
}

var uploadClearScopes = []struct {
	label  string
	states []string
	all    bool
}{
	{"Completed + cancelled + failed", []string{"completed", "cancelled", "failed"}, false},
	{"Completed + cancelled", []string{"completed", "cancelled"}, false},
	{"Completed", []string{"completed"}, false},
	{"Cancelled", []string{"cancelled"}, false},
	{"Failed", []string{"failed"}, false},
	{"Queued", []string{"queued"}, false},
	{"Everything", nil, true},
}

func (m *model) beginUploadAction(action string, confirm bool) tea.Cmd {
	req := daemon.UploadActionRequest{Action: action}
	if action == "cancel" && confirm {
		req.Usernames = m.uploadActionUsernames()
		if len(req.Usernames) == 0 {
			m.setNotice("Select an upload or upload subtree first")
			return nil
		}
		m.uploadPending, m.uploadConfirmLabel = req, "Cancel all current uploads for "+strings.Join(req.Usernames, ", ")
	} else {
		req.IDs = m.uploadActionIDs()
		if len(req.IDs) == 0 {
			m.setNotice("Select an upload or upload subtree first")
			return nil
		}
		if confirm {
			m.uploadPending, m.uploadConfirmLabel = req, "Clear selected uploads (live transfers will stop)"
		}
	}
	if confirm {
		m.uploadConfirm, m.uploadConfirmChoice = true, 0
		return nil
	}
	return m.uploadAction(req)
}

func (m *model) openUploadStatusMenu() {
	m.uploadStatusMenu, m.uploadStatusChoice = true, 0
}

func (m *model) uploadStatusMenuKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "C":
		m.uploadStatusMenu = false
	case "up", "k":
		m.uploadStatusChoice = max(0, m.uploadStatusChoice-1)
	case "down", "j":
		m.uploadStatusChoice = min(len(uploadClearScopes)-1, m.uploadStatusChoice+1)
	case "enter":
		scope := uploadClearScopes[m.uploadStatusChoice]
		m.uploadStatusMenu = false
		req := daemon.UploadActionRequest{Action: "clear", States: append([]string(nil), scope.states...), All: scope.all}
		if scope.all || m.uploadStatusChoice == 5 {
			m.uploadPending, m.uploadConfirmLabel = req, "Clear "+scope.label+" uploads (live transfers will stop)"
			m.uploadConfirm, m.uploadConfirmChoice = true, 0
			return nil
		}
		return m.uploadAction(req)
	}
	return nil
}

func (m *model) uploadConfirmKey(k tea.KeyPressMsg) tea.Cmd {
	accepted := false
	switch k.String() {
	case "esc", "n":
	case "left", "up", "right", "down", "j", "k":
		m.uploadConfirmChoice = 1 - m.uploadConfirmChoice
		return nil
	case "y":
		accepted = true
	case "enter":
		accepted = m.uploadConfirmChoice == 1
	default:
		return nil
	}
	m.uploadConfirm = false
	if m.forcePending != nil {
		ids := m.forcePending
		m.forcePending = nil
		if accepted {
			return m.forceDownloads(ids)
		}
		return nil
	}
	if m.restoreDownloadRules {
		m.restoreDownloadRules = false
		if accepted {
			m.cfg.Downloads.FilterPatterns = config.DefaultDownloadFilters()
		}
		return nil
	}
	if m.restoreShareExclusions {
		m.restoreShareExclusions = false
		if accepted {
			m.cfg.ShareExclusions = config.DefaultShareExclusions()
			m.cursor = min(m.cursor, len(m.settingFields())-1)
		}
		return nil
	}
	if accepted {
		return m.uploadAction(m.uploadPending)
	}
	return nil
}

func (m model) uploadAction(req daemon.UploadActionRequest) tea.Cmd {
	if len(req.IDs) == 0 && len(req.Usernames) == 0 && len(req.States) == 0 && !req.All {
		return nil
	}
	return func() tea.Msg {
		result, actionErr := m.client.UploadAction(m.ctx, req)
		transfers, refreshErr := m.client.Transfers(m.ctx)
		view := toTransfers(transfers)
		if refreshErr != nil {
			view = m.transfers
		}
		first := actionErr
		if first == nil {
			for _, item := range result.Errors {
				if item.Error != "" {
					first = fmt.Errorf("%s", item.Error)
					break
				}
			}
		}
		if first == nil {
			first = refreshErr
		}
		return transferActionMsg{transfers: view, result: result, upload: true, err: first}
	}
}
func (m model) active() bool {
	for _, x := range m.transfers {
		if x.state == "queued" || x.state == "incomplete" || x.state == "running" || x.state == "finalizing" || x.state == "retrying" {
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
