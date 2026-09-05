package tui

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
)

func (m *model) toggleDownloadSelection() {
	_, node := m.transferTrees[transferDownloads].node(m.cursor)
	if node == nil || node.kind != treeFile || node.source < 0 {
		return
	}
	if m.downloadSelected == nil {
		m.downloadSelected = map[string]bool{}
	}
	id := m.transfers[node.source].id
	m.downloadSelected[id] = !m.downloadSelected[id]
}
func (m model) filteredDownloadIDs() []string {
	ids := []string{}
	marked := false
	for _, tr := range m.transfers {
		if tr.direction == "download" && m.downloadSelected[tr.id] {
			marked = true
			if tr.state == "filtered" {
				ids = append(ids, tr.id)
			}
		}
	}
	if marked {
		return ids
	}
	_, node := m.transferTrees[transferDownloads].node(m.cursor)
	// Never turn a folder or user row into an implicit bypass.
	if node != nil && node.kind == treeFile && node.source >= 0 {
		tr := m.transfers[node.source]
		if tr.state == "filtered" {
			ids = append(ids, tr.id)
		}
	}
	return ids
}
func (m *model) confirmForceDownloads() {
	if m.workspace != workspaceTransfers || m.transferTab != transferDownloads {
		return
	}
	ids := m.filteredDownloadIDs()
	if len(ids) == 0 {
		m.setNotice("Select individual filtered files to download anyway")
		return
	}
	m.forcePending = ids
	m.uploadConfirm, m.uploadConfirmChoice = true, 0
	m.uploadConfirmLabel = fmt.Sprintf("Download %d selected filtered file(s) anyway", len(ids))
}
func (m model) forceDownloads(ids []string) tea.Cmd {
	return func() tea.Msg {
		result, err := m.client.ForceDownloads(m.ctx, ids)
		if err == nil && len(result.Errors) > 0 {
			err = fmt.Errorf("%s", result.Errors[0].Error)
		}
		transfers, refreshErr := m.client.Transfers(m.ctx)
		view := m.transfers
		if refreshErr == nil {
			view = toTransfers(transfers)
		}
		if err == nil {
			err = refreshErr
		}
		return transferActionMsg{transfers: view, result: result, err: err}
	}
}
