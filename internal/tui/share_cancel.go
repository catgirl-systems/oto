package tui

import tea "charm.land/bubbletea/v2"

type scanCancelMsg struct{ err error }

func (m model) cancelShareScan() tea.Cmd {
	scan := m.status.shareScan
	if scan == nil || scan.State != "scanning" {
		return nil
	}
	id := scan.ID
	return func() tea.Msg { return scanCancelMsg{err: m.client.CancelShareScan(m.ctx, id)} }
}
