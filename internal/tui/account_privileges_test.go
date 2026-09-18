package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestPrivilegeEditorOfflineDraftConfirmationAndFencing(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openPrivileges("Alice"))
	e := m.privileges
	failIf(t, e == nil || e.busy || e.balance.Fresh || e.balance.Error == "", "offline balance", e)
	m.privilegesKey(key("tab"))
	m.pastePrivileges("q/?猫")
	failIf(t, !strings.Contains(e.username, "q/?猫") || m.chatDraftCount() != 1, "draft/paste", e)
	e.days = "1.5"
	if cmd := m.submitPrivilegeGift(false); cmd != nil || e.err == "" {
		t.Fatal("fractional days accepted")
	}
	e.days = "1"
	e.mode = "preview"
	e.result = daemon.AccountPrivilegeGiftResult{Username: e.username, Days: 1, State: "preview"}
	if cmd := m.privilegesKey(key("enter")); cmd != nil || e.mode != "edit" {
		t.Fatal("default cancel submitted")
	}
	for _, mode := range []string{"edit", "preview", "result"} {
		e.mode = mode
		for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
			m.width, m.height = size[0], size[1]
			screen := m.View().Content
			failIf(t, lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height, "privilege layout overflow", mode, size, screen)
		}
	}
	old := privilegeMsg{editor: e, request: e.request, balance: daemon.AccountPrivileges{Fresh: true}}
	m.privilegesKey(key("esc"))
	drainChat(t, &m, m.openPrivileges("Bob"))
	m.applyPrivilegeMsg(old)
	failIf(t, m.privileges.username != "Bob" || m.privileges.balance.Fresh, "old overlay response applied")
	m.community.summary.Session++
	if cmd := m.privilegesKey(key("enter")); cmd != nil || m.privileges.err == "" {
		t.Fatal("stale session action")
	}
}
