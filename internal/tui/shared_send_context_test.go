package tui

import (
	tea "charm.land/bubbletea/v2"
	"testing"
)

func TestSharedSendContextRecipientAndSessionFence(t *testing.T) {
	m, _ := privateChatModel(t)
	open := func() {
		m.userActions = &userActions{username: "猫", identity: m.community.summary.CommunityIdentity, row: 7}
		m.userActionsKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		if m.workspace != workspaceShares || m.userActions != nil || m.sharedSendTarget == nil {
			t.Fatal("missing contextual Shares selection")
		}
		m.shareTree = treeState{nodes: []treeNode{{kind: treeFile, path: `Music\song.flac`}}, visible: []int{0}}
		m.cursor = 0
	}
	open()
	if cmd := m.openSharedSendPrompt(); cmd != nil {
		t.Fatal("context sent without preview")
	}
	p := m.commandOutput.sharedPrompt
	if p.input != "猫" || p.cursor != 1 || p.busy || m.sharedSendTarget != nil {
		t.Fatal("recipient not safely captured", p)
	}
	m.commandOutput = nil
	open()
	m.community.summary.Session++
	m.openSharedSendPrompt()
	if m.commandOutput != nil || m.sharedSendTarget != nil {
		t.Fatal("stale contextual recipient used")
	}
	open()
	m.switchWorkspace(workspaceSearch)
	if m.sharedSendTarget != nil {
		t.Fatal("recipient leaked to later navigation")
	}
}
