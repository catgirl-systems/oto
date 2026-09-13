package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

type activityReportMsg struct{ request uint64 }

// Only actual input schedules activity. Resource polling, timers, transfers and
// notifications use the ordinary update path without keeping the account online.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var activity tea.Cmd
	switch x := msg.(type) {
	case tea.KeyPressMsg, tea.PasteMsg:
		m.activityQueued = m.community.summary.CommunityIdentity
		activity = m.reportCommunityActivity()
	case activityReportMsg:
		if x.request != m.activityRequest {
			return m, nil
		}
		m.activityBusy = false
		cmd := m.reportCommunityActivity()
		return m, cmd
	}
	updated, cmd := m.updateMessage(msg)
	return updated, tea.Batch(cmd, activity)
}
func (m *model) reportCommunityActivity() tea.Cmd {
	if m.activityBusy || m.client == nil || !m.community.supports("activity") {
		return nil
	}
	id := m.activityQueued
	m.activityQueued = daemon.CommunityIdentity{}
	if id.Account == "" || id != m.community.summary.CommunityIdentity {
		return nil
	}
	m.activityBusy = true
	m.activityRequest++
	request, client, base := m.activityRequest, m.client, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(base, 2*time.Second)
		defer cancel()
		_, _ = client.CommunityActivity(ctx, id)
		return activityReportMsg{request: request}
	}
}
