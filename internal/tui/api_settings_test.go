package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestAPIEditorApproveAndRevoke(t *testing.T) {
	m, _ := privateChatModel(t)
	var created struct {
		RequestToken string `json:"request_token"`
	}
	must(t, m.client.Do(m.ctx, "POST", "/v1/auth/requests", map[string]string{"name": "agent"}, &created))
	failIf(t, created.RequestToken == "", "no request token")

	m.workspace = workspaceSettings
	drainChat(t, &m, m.openAPIEditor("requests"))
	e := m.apiEditor
	failIf(t, e == nil || e.busy || e.err != "", e)
	failIf(t, len(e.requests) != 1 || e.requests[0].Name != "agent", e.requests)

	press := func(key tea.Key) { drainChat(t, &m, m.key(tea.KeyPressMsg(key))) }
	press(tea.Key{Code: 'a'})
	failIf(t, e.approve == "" || e.approveDays != "30" || e.approveNever, "approve dialog state", e)
	// Bad input is rejected; the never toggle and Enter approve.
	e.approveDays = "0"
	press(tea.Key{Code: tea.KeyEnter})
	failIf(t, e.err == "" || e.approve == "", "zero days accepted", e)
	press(tea.Key{Code: tea.KeyTab})
	failIf(t, !e.approveNever, "never toggle", e)
	press(tea.Key{Code: tea.KeyEnter})
	failIf(t, len(e.requests) != 0, "approved request still pending", e.requests)

	press(tea.Key{Code: tea.KeyTab})
	failIf(t, e.mode != "apps" || len(e.apps) != 1 || e.apps[0].Name != "agent", e.apps)
	failIf(t, e.apps[0].ExpiresAt != nil, "never-expiring app has expiry", e.apps[0])

	press(tea.Key{Code: 'r'})
	failIf(t, e.revoke == "" || e.confirm, "revoke confirm state", e)
	press(tea.Key{Code: tea.KeyEnter})
	failIf(t, e.revoke != "", "enter without confirm revoked", e)
	press(tea.Key{Code: 'r'})
	press(tea.Key{Code: tea.KeyRight})
	press(tea.Key{Code: tea.KeyEnter})
	failIf(t, len(e.apps) != 0, "app not revoked", e.apps)

	press(tea.Key{Code: tea.KeyEsc})
	failIf(t, m.apiEditor != nil, "editor not closed")

	for _, size := range [][2]int{{120, 40}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		m.apiEditor = &apiEditor{mode: "requests", requests: e.requests, apps: nil}
		v := m.apiEditorView()
		failIf(t, lipgloss.Width(v) > m.width || lipgloss.Height(v) > m.height, size, v)
		m.apiEditor = &apiEditor{mode: "apps", requests: nil, apps: e.apps}
		v = m.apiEditorView()
		failIf(t, lipgloss.Width(v) > m.width || lipgloss.Height(v) > m.height, size, v)
	}
}

func TestAPIPendingNoticeFromStatus(t *testing.T) {
	m, _ := privateChatModel(t)
	update := func(m model, count int, name string) model {
		next, _ := m.Update(statusMsg{snapshot: daemon.Snapshot{PendingAPIAuthCount: count, PendingAPIAuthName: name}})
		return next.(model)
	}
	m = update(m, 1, "agent")
	failIf(t, m.apiNotice != "agent" || m.notice == "", m.apiNotice, m.notice)
	// Same request name does not repeat the notice.
	m.notice = ""
	m = update(m, 1, "agent")
	failIf(t, m.notice != "", "notice repeated", m.notice)
	// A different request does.
	m = update(m, 2, "other")
	failIf(t, m.apiNotice != "other" || m.notice == "", m.apiNotice, m.notice)
	// Clearing pending requests resets the deduplication.
	m = update(m, 0, "")
	failIf(t, m.apiNotice != "", "notice state not reset", m.apiNotice)
}
