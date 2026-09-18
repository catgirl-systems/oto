package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestSharedSendPromptAndReviewSafety(t *testing.T) {
	m, _ := privateChatModel(t)
	m.workspace = workspaceShares
	m.cursor = 0
	m.shareTree = treeState{nodes: []treeNode{{kind: treeFile, path: `Music\猫.flac`}}, visible: []int{0}}
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	press('s')
	owner := m.commandOutput
	failIf(t, owner == nil || owner.sharedPrompt == nil, "missing send prompt")
	p := owner.sharedPrompt
	failIf(t, len(p.req.Files) != 1 || p.req.Files[0] != `Music\猫.flac` || p.req.Folder != "", p.req)
	m.pasteSharedSendRecipient("Alice\ns")
	failIf(t, p.input != "" || p.err == "", "multiline paste accepted")
	oldID := p.req.RequestID
	m.pasteSharedSendRecipient("Alice")
	failIf(t, p.input != "Alice" || p.req.RequestID == oldID, "recipient edit retained stale request")
	press(tea.KeyEnter)
	failIf(t, p.busy || p.err == "" || p.input != "Alice", "offline preview did not retain editable recipient", p)
	page := daemon.SharedSendPage{CommunityIdentity: p.req.CommunityIdentity, Captured: p.req.CommunityIdentity, RequestID: p.req.RequestID, Token: "token", Username: "Alice", State: "preview", Total: 1, Eligible: 1, Bytes: 42, SizesKnown: true, Files: []daemon.SharedSendFile{{Filename: `Music\猫.flac`, Size: 42, State: "preview"}}}
	m.applySharedSendPreview(sharedSendPreviewMsg{owner: owner, page: page})
	b := m.commandOutput.broadcast
	failIf(t, b == nil || b.shared == nil, "missing shared-send review")
	press('s')
	failIf(t, b.dialog != "send" || b.confirm, "unsafe confirmation")
	press(tea.KeyEnter)
	failIf(t, b.page.State != "preview" || b.dialog != "", "implicit send")
	b.shared.Eligible = 0
	press('s')
	failIf(t, b.dialog != "" || !strings.Contains(b.err, "No permitted"), "denied selection can be confirmed")
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}, {10, 3}} {
		m.width, m.height = size[0], size[1]
		b.dialog = "send"
		b.err = ""
		m.renderBroadcastOutput()
		v := m.commandOutputView()
		failIf(t, lipgloss.Width(v) > m.width || lipgloss.Height(v) > m.height, size, v)
		failIf(t, m.width >= 20 && !strings.Contains(v, "[Cancel]"), "invisible consent", v)
	}
	current := m.commandOutput
	m.applySharedSendPreview(sharedSendPreviewMsg{owner: owner, page: page})
	failIf(t, m.commandOutput != current, "stale preview replaced viewer")
}
