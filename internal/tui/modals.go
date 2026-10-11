package tui

import (
	"strings"
	"unicode"

	"charm.land/bubbletea/v2"
)

// modal is one input-owning overlay or mode. key(), paste and screen() all
// walk modals in order, so the dialog drawn is always the one receiving input.
type modal struct {
	name   string
	active func(*model) bool
	key    func(*model, tea.KeyPressMsg) tea.Cmd
	paste  func(*model, string) // nil: the modal swallows paste
	view   func(model) string   // nil: drawn inside the workspace or footer
	full   bool                 // view replaces the screen rather than overlaying it
}

// modals lists every modal, highest priority first. Community dialogs come
// first because some open asynchronously (room retry) or from inside other
// modals (draft-loss quit), and must then take over both screen and input.
var modals = []modal{
	{name: "chat dialog", active: func(m *model) bool { return m.community.chats.dialog != nil },
		key: (*model).chatDialogKey, view: model.chatDialogView},
	{name: "room dialog", active: func(m *model) bool { return m.community.rooms.dialog != nil },
		key: (*model).roomDialogKey, view: model.roomDialogView},
	{name: "private room dialog", active: func(m *model) bool { return m.community.rooms.private.dialog != nil },
		key: (*model).privateRoomDialogKey, view: model.privateRoomDialogView},
	{name: "buddy dialog", active: func(m *model) bool { return m.community.buddies.dialog != nil },
		key: (*model).buddyDialogKey, view: model.buddyDialogView},
	{name: "discover dialog", active: func(m *model) bool { return m.community.discover.dialog != nil },
		key: (*model).communityKey, view: model.discoverDialogView},
	{name: "picture dialog", active: func(m *model) bool { return m.community.peer.dialog },
		key: (*model).communityKey, view: model.peerDialogView},
	{name: "setup", active: func(m *model) bool { return m.setup },
		key: (*model).setupKey, paste: (*model).pasteSetup, view: model.setupView, full: true},
	{name: "command output", active: func(m *model) bool { return m.commandOutput != nil },
		key: (*model).commandOutputKey, paste: (*model).pasteCommandOutput, view: model.commandOutputView},
	{name: "download as", active: func(m *model) bool { return m.downloadAs != nil },
		key: (*model).downloadAsKey, paste: (*model).pasteDownloadAs, view: model.downloadAsView},
	{name: "statistics", active: func(m *model) bool {
		return m.stats.prune || m.workspace == workspaceStats && (m.stats.edit != "" || m.stats.detail != nil)
	}, key: (*model).statsKey},
	{name: "search scope", active: func(m *model) bool { return m.searchScope != nil },
		key: (*model).searchScopeKey, paste: (*model).pasteSearchScope, view: model.searchScopeView},
	// Editors yield to the quit confirmation, which is drawn in the footer.
	{name: "privileges", active: func(m *model) bool { return m.privileges != nil && !m.confirm },
		key: (*model).privilegesKey, paste: (*model).pastePrivileges, view: model.privilegesView},
	{name: "receiving settings", active: func(m *model) bool { return m.receivingEditor != nil && !m.confirm },
		key: (*model).receivingSettingsKey, paste: (*model).pasteReceivingSettings, view: model.receivingSettingsView},
	{name: "away settings", active: func(m *model) bool { return m.awayEditor != nil && !m.confirm },
		key: (*model).awaySettingsKey, paste: (*model).pasteAwaySettings, view: model.awaySettingsView},
	{name: "API editor", active: func(m *model) bool { return m.apiEditor != nil && !m.confirm },
		key: (*model).apiEditorKey, paste: (*model).pasteAPISettings, view: model.apiEditorView},
	{name: "text tools", active: func(m *model) bool { return m.textTools != nil && !m.confirm },
		key: (*model).textToolsKey, paste: (*model).pasteTextTools, view: model.textToolsView},
	{name: "privacy rules", active: func(m *model) bool { return m.privacyRules != nil && !m.confirm },
		key: (*model).privacyRulesKey, paste: (*model).pastePrivacyRules, view: model.privacyRulesView},
	{name: "share access", active: func(m *model) bool { return m.shareAccess != nil && !m.confirm },
		key: (*model).shareAccessKey, view: model.shareAccessView},
	{name: "user actions", active: func(m *model) bool { return m.userActions != nil },
		key: (*model).userActionsKey, view: model.userActionsView},
	{name: "password", active: func(m *model) bool { return m.passwordForm },
		key: (*model).passwordFormKey, paste: (*model).pastePassword, view: model.passwordFormView},
	{name: "status menu", active: func(m *model) bool { return m.statusMenu },
		key: (*model).statusMenuKey, view: model.statusMenuView},
	{name: "upload status menu", active: func(m *model) bool { return m.uploadStatusMenu },
		key: (*model).uploadStatusMenuKey, view: model.uploadStatusMenuView},
	{name: "upload confirm", active: func(m *model) bool { return m.uploadConfirm },
		key: (*model).uploadConfirmKey, view: model.uploadConfirmView},
	{name: "help", active: func(m *model) bool { return m.help },
		key: (*model).helpKey, view: model.helpView},
	{name: "details", active: func(m *model) bool { return m.details },
		key: (*model).detailsKey, view: model.detailView},
	{name: "folder menu", active: func(m *model) bool { return m.folderMenu },
		key: (*model).folderMenuKey, paste: (*model).pasteFolderMenu, view: model.folderMenuView},
	{name: "setting choice", active: func(m *model) bool { return m.choiceChoosing },
		key: (*model).settingChoiceKey},
	{name: "quit confirm", active: func(m *model) bool { return m.confirm },
		key: (*model).quitConfirmKey},
}

// activeModal returns the highest-priority open modal, if any.
func (m *model) activeModal() *modal {
	for i := range modals {
		if modals[i].active(m) {
			return &modals[i]
		}
	}
	return nil
}

func (m *model) helpKey(k tea.KeyPressMsg) tea.Cmd {
	if s := k.String(); s == "?" || s == "esc" || s == "q" {
		m.help, m.helpScroll = false, 0
		return nil
	}
	scrollKey(k.String(), &m.helpScroll, m.pageRows())
	return nil
}

func (m *model) detailsKey(k tea.KeyPressMsg) tea.Cmd {
	if s := k.String(); s == "i" || s == "esc" {
		m.details = false
	}
	return nil
}

func (m *model) quitConfirmKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "y", "enter":
		return tea.Quit
	case "n", "esc":
		m.confirm = false
	}
	return nil
}

func (m model) peerDialogView() string {
	p := m.community.peer
	return communityConfirmationView("Save picture for "+p.image.Username+" to "+p.path+"? Existing files are never overwritten.", p.confirm, p.dialogScroll, m.width, m.height)
}

func (m *model) pasteCommandOutput(text string) {
	if m.commandOutput.sharedPrompt != nil {
		m.pasteSharedSendRecipient(text)
	}
}

func (m *model) pasteSearchScope(text string) {
	if d := m.searchScope; d.editing && len(text) <= 4096 {
		value, cursor := insertText(d.value, text, d.cursor)
		d.setInput(value, cursor)
	}
}

func (m *model) pasteFolderMenu(text string) {
	if !m.folderMenuEditing {
		return
	}
	if strings.IndexFunc(text, unicode.IsControl) >= 0 {
		m.folderMenuError = "Name/path cannot contain control characters"
		return
	}
	value := m.folderMenuInput()
	*value, m.inputCursor = insertText(*value, text, m.inputCursor)
	m.folderMenuError = ""
}

func (m *model) pastePassword(text string) {
	if !m.passwordChanging {
		m.passwordVals[m.passwordField], m.inputCursor = insertText(m.passwordVals[m.passwordField], stripControl(text), m.inputCursor)
		m.passwordErr = ""
	}
}

func (m *model) pasteSetup(text string) {
	m.setupVals[m.setupField], m.inputCursor = insertText(m.setupVals[m.setupField], stripControl(text), m.inputCursor)
}

func stripControl(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
}
