package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestBroadcastViewerPagesConsentAndStaleResponses(t *testing.T) {
	m, _ := privateChatModel(t)
	ctx := context.Background()
	id := m.community.summary.CommunityIdentity
	for _, name := range []string{"Alice", "猫"} {
		_, err := m.client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: name})
		must(t, err)
	}
	out, err := m.client.PreviewCommunityBroadcast(ctx, daemon.CommunityBroadcastRequest{CommunityIdentity: id, RequestID: "viewer", Audience: "buddies", Text: "hello 👋", Offline: []string{"Alice", "猫"}})
	must(t, err)
	m.showBroadcastOutput(out)
	owner := m.commandOutput
	b := owner.broadcast
	b.page.Recipients = b.page.Recipients[:1]
	b.page.NextCursor = 1
	b.reviewed = 1
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	press('s')
	failIf(t, b.dialog != "" || !strings.Contains(b.err, "Review all"), b)
	press(']')
	failIf(t, b.cursor != 1 || b.reviewed != 2 || b.page.Recipients[0].Username != "猫", b)
	press('s')
	failIf(t, b.dialog != "send" || b.confirm, "unsafe default")
	press(tea.KeyEnter)
	failIf(t, b.page.State != "preview" || b.dialog != "", "default sent")
	press('x')
	press(tea.KeyRight)
	press(tea.KeyEnter)
	failIf(t, b.page.State != "stopped", b)
	m.showBroadcastOutput(out)
	current := m.commandOutput
	m.applyBroadcastOutput(broadcastOutputMsg{owner: owner, page: out})
	failIf(t, m.commandOutput != current, "stale owner")
	m.community.summary.Session++
	press('s')
	failIf(t, current.broadcast.dialog != "" || !strings.Contains(current.broadcast.err, "Session changed"), "stale confirmation")
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}, {10, 3}} {
		m.width, m.height = size[0], size[1]
		for _, dialog := range []string{"", "send", "stop"} {
			current.broadcast.dialog = dialog
			current.broadcast.err = ""
			m.renderBroadcastOutput()
			v := m.commandOutputView()
			failIf(t, lipgloss.Width(v) > m.width || lipgloss.Height(v) > m.height, size, v)
			failIf(t, dialog != "" && m.width >= 20 && !strings.Contains(v, "[Cancel]"), "confirmation invisible", size, v)
		}
	}
}
