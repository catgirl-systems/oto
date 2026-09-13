package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func drainActivity(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	for n := 0; cmd != nil; n++ {
		if n > 2 {
			t.Fatal("unbounded activity reporting")
		}
		msg := cmd()
		if _, ok := msg.(activityReportMsg); !ok {
			t.Fatalf("input unexpectedly triggered %T", msg)
		}
		updated, next := m.Update(msg)
		*m = updated.(model)
		cmd = next
	}
}
func TestActivityOnlyReportsInputAndCoalesces(t *testing.T) {
	m, _ := privateChatModel(t)
	before, err := m.client.CommunitySummary(m.ctx)
	if err != nil {
		t.Fatal(err)
	}
	drainChat(t, &m, m.loadCommunitySummary())
	after, err := m.client.CommunitySummary(m.ctx)
	if err != nil || !after.LastActivity.Equal(before.LastActivity) {
		t.Fatal("poll counted as activity")
	}
	updated, cmd := m.Update(tea.PasteMsg{Content: ""})
	m = updated.(model)
	if cmd == nil || !m.activityBusy {
		t.Fatal("paste did not schedule activity")
	}
	next, second := m.Update(tea.PasteMsg{Content: ""})
	m = next.(model)
	if second != nil || m.activityQueued.Account == "" {
		t.Fatal("input not coalesced")
	}
	drainActivity(t, &m, cmd)
	after, err = m.client.CommunitySummary(m.ctx)
	if err != nil || !after.LastActivity.After(before.LastActivity) || m.activityBusy || m.activityQueued.Account != "" {
		t.Fatal("activity not recorded/settled", err)
	}
	updated, cmd = m.Update(tea.PasteMsg{Content: ""})
	m = updated.(model)
	next, _ = m.Update(tea.PasteMsg{Content: ""})
	m = next.(model)
	m.community.summary.Session++
	drainActivity(t, &m, cmd)
	if m.activityBusy || m.activityQueued.Account != "" {
		t.Fatal("stale queued activity retained")
	}
}
