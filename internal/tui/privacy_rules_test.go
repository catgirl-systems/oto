package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestPrivacyRulesEditorCRUDAndDrafts(t *testing.T) {
	m, _ := privateChatModel(t)
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	drainChat(t, &m, m.openPrivacyRules("ban", "Case"))
	failIf(t, m.privacyRules == nil || m.privacyRules.form == nil, "editor unavailable")
	for range 3 {
		press(tea.KeyEnter)
	}
	updated, cmd := m.Update(tea.PasteMsg{Content: "q/?猫"})
	m = updated.(model)
	drainActivity(t, &m, cmd)
	failIf(t, m.privacyRules.form.rule.Message != "q/?猫" || m.chatDraftCount() != 1, "paste sent or lost draft")
	press(tea.KeyEnter)
	failIf(t, m.privacyRules.dialog == nil || m.privacyRules.dialog.confirm, "missing safe preview")
	press(tea.KeyEnter)
	failIf(t, m.privacyRules.dialog != nil || m.privacyRules.form == nil || len(m.privacyRules.rules) != 0, "default confirmation mutated")
	press(tea.KeyEnter)
	press(tea.KeyRight)
	press(tea.KeyEnter)
	failIf(t, m.privacyRules.err != "" || len(m.privacyRules.rules) != 1 || m.privacyRules.form != nil, "save", m.privacyRules.err)
	id := m.privacyRules.rules[0].ID
	m.key(chatPress("e"))
	press(tea.KeyTab)
	press(tea.KeyTab)
	for range 4 {
		press(tea.KeyBackspace)
	}
	m.key(chatPress("Other"))
	press(tea.KeyEnter)
	press(tea.KeyEnter)
	press(tea.KeyRight)
	press(tea.KeyEnter)
	failIf(t, len(m.privacyRules.rules) != 1 || m.privacyRules.rules[0].Value != "Other" || m.privacyRules.rules[0].ID != id, "edit was not atomic", m.privacyRules.rules, m.privacyRules.err)
	m.key(chatPress("d"))
	press(tea.KeyEnter)
	failIf(t, len(m.privacyRules.rules) != 1, "default deletion")
	m.key(chatPress("d"))
	press(tea.KeyRight)
	press(tea.KeyEnter)
	failIf(t, len(m.privacyRules.rules) != 0, "delete", m.privacyRules.err)
	m.key(chatPress("a"))
	press(tea.KeyTab)
	press(tea.KeyTab)
	m.key(chatPress("q/?猫"))
	press(tea.KeyEsc)
	failIf(t, m.privacyRules.dialog == nil || m.privacyRules.dialog.confirm, "draft silently discarded")
	press(tea.KeyEnter)
	failIf(t, m.privacyRules.form == nil, "cancel discarded draft")
	m.key(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	failIf(t, m.community.chats.dialog == nil || m.community.chats.dialog.kind != "quit" || m.community.chats.dialog.confirm, "quit bypassed draft confirmation")
}

func TestPrivacyRulesPagingAndOverlayFencing(t *testing.T) {
	m, _ := privateChatModel(t)
	id := m.community.summary.CommunityIdentity
	page, err := m.client.CommunityRules(m.ctx, daemon.CommunityRulesRequest{CommunityIdentity: id})
	must(t, err)
	for i := 0; i < 201; i++ {
		page, err = m.client.SetCommunityRule(m.ctx, daemon.CommunityRuleRequest{CommunityIdentity: id, Revision: page.Revision, Confirm: true, Rule: daemon.CommunityRule{Action: "ban", Kind: "username", Value: fmt.Sprintf("user%03d", i)}})
		must(t, err)
	}
	old := m.openPrivacyRules("", "")
	m.closePrivacyRules()
	drainChat(t, &m, m.openPrivacyRules("", ""))
	current := m.privacyRules.request
	oldPage := old().(privacyRulesPageMsg)
	oldPage.page.Rules = nil
	m.applyPrivacyRulesPage(oldPage)
	failIf(t, m.privacyRules.request != current || len(m.privacyRules.rules) != 200, "closed overlay response applied")
	drainChat(t, &m, m.key(chatPress("n")))
	failIf(t, len(m.privacyRules.rules) != 1 || m.privacyRules.cursor == "" || len(m.privacyRules.back) != 1, "next page", m.privacyRules)
	drainChat(t, &m, m.key(chatPress("p")))
	failIf(t, len(m.privacyRules.rules) != 200 || m.privacyRules.cursor != "" || len(m.privacyRules.back) != 0, "previous page")
	pending := m.loadPrivacyRulesPage("")
	m.community.summary.Session++
	m.applyPrivacyRulesPage(pending().(privacyRulesPageMsg))
	failIf(t, !strings.Contains(m.privacyRulesView(), "Session changed"), "stale session not visible")
}

func TestPrivacyRulesResponsiveInputAndSettings(t *testing.T) {
	m, _ := privateChatModel(t)
	m.settingsSection = settingsCommunity
	fields := m.settingFields()
	failIf(t, len(fields) == 0 || fields[0].id != settingPrivacyRules, "privacy settings inaccessible")
	drainChat(t, &m, m.openPrivacyRules("ban", "Case"))
	m.privacyRules.form.field = 3
	m.pastePrivacyRules("hello\nworld")
	failIf(t, m.privacyRules.form.rule.Message != "", "multiline paste was silently transformed")
	m.pastePrivacyRules(strings.Repeat("猫", 200))
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		for _, dialog := range []bool{false, true} {
			if dialog {
				m.privacyRules.dialog = &privacyRuleDialog{kind: "save", label: privacyRuleLabel(m.privacyRules.form.rule, daemon.CommunityRule{})}
			} else {
				m.privacyRules.dialog = nil
			}
			screen := m.View().Content
			failIf(t, lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height, "privacy overflow", size, screen)
			failIf(t, dialog && !strings.Contains(screen, "Cancel"), "cancel not visible", size, screen)
		}
	}
}
