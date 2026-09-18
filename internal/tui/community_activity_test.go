package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func drainActivity(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	for n := 0; cmd != nil; n++ {
		failIf(t, n > 2, "unbounded activity reporting")
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
	must(t, err)
	drainChat(t, &m, m.loadCommunitySummary())
	after, err := m.client.CommunitySummary(m.ctx)
	failIf(t, err != nil || !after.LastActivity.Equal(before.LastActivity), "poll counted as activity")
	updated, cmd := m.Update(tea.PasteMsg{Content: ""})
	m = updated.(model)
	failIf(t, cmd == nil || !m.activityBusy, "paste did not schedule activity")
	next, second := m.Update(tea.PasteMsg{Content: ""})
	m = next.(model)
	failIf(t, second != nil || m.activityQueued.Account == "", "input not coalesced")
	drainActivity(t, &m, cmd)
	after, err = m.client.CommunitySummary(m.ctx)
	failIf(t, err != nil || !after.LastActivity.After(before.LastActivity) || m.activityBusy || m.activityQueued.Account != "", "activity not recorded/settled", err)
	updated, cmd = m.Update(tea.PasteMsg{Content: ""})
	m = updated.(model)
	next, _ = m.Update(tea.PasteMsg{Content: ""})
	m = next.(model)
	m.community.summary.Session++
	drainActivity(t, &m, cmd)
	failIf(t, m.activityBusy || m.activityQueued.Account != "", "stale queued activity retained")
}
