package tui

import "github.com/catgirl-systems/oto/internal/daemon"

// Both operations use the same paged review and explicit-confirmation controls.
// This is UI metadata only; requests still use the distinct typed daemon APIs.
func sharedSendOutputPage(p daemon.SharedSendPage) daemon.CommunityBroadcastPage {
	return daemon.CommunityBroadcastPage{CommunityIdentity: p.CommunityIdentity, Captured: p.Captured, RequestID: p.RequestID, Token: p.Token, State: p.State, CreatedAt: p.CreatedAt, Total: p.Total, NextCursor: p.NextCursor, Recipients: make([]daemon.CommunityBroadcastRecipient, len(p.Files))}
}
func (m *model) showSharedSendOutput(p daemon.SharedSendPage) {
	m.showBroadcastOutput(sharedSendOutputPage(p))
	m.commandOutput.broadcast.shared = &p
	m.renderBroadcastOutput()
}
