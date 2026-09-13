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
		if _, err := m.client.SetCommunityBuddy(ctx, daemon.CommunityBuddyRequest{CommunityIdentity: id, Username: name}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := m.client.PreviewCommunityBroadcast(ctx, daemon.CommunityBroadcastRequest{CommunityIdentity: id, RequestID: "viewer", Audience: "buddies", Text: "hello 👋", Offline: []string{"Alice", "猫"}})
	if err != nil {
		t.Fatal(err)
	}
	m.showBroadcastOutput(out)
	owner := m.commandOutput
	b := owner.broadcast
	b.page.Recipients = b.page.Recipients[:1]
	b.page.NextCursor = 1
	b.reviewed = 1
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	press('s')
	if b.dialog != "" || !strings.Contains(b.err, "Review all") {
		t.Fatal(b)
	}
	press(']')
	if b.cursor != 1 || b.reviewed != 2 || b.page.Recipients[0].Username != "猫" {
		t.Fatal(b)
	}
	press('s')
	if b.dialog != "send" || b.confirm {
		t.Fatal("unsafe default")
	}
	press(tea.KeyEnter)
	if b.page.State != "preview" || b.dialog != "" {
		t.Fatal("default sent")
	}
	press('x')
	press(tea.KeyRight)
	press(tea.KeyEnter)
	if b.page.State != "stopped" {
		t.Fatal(b)
	}
	m.showBroadcastOutput(out)
	current := m.commandOutput
	m.applyBroadcastOutput(broadcastOutputMsg{owner: owner, page: out})
	if m.commandOutput != current {
		t.Fatal("stale owner")
	}
	m.community.summary.Session++
	press('s')
	if current.broadcast.dialog != "" || !strings.Contains(current.broadcast.err, "Session changed") {
		t.Fatal("stale confirmation")
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}, {10, 3}} {
		m.width, m.height = size[0], size[1]
		for _, dialog := range []string{"", "send", "stop"} {
			current.broadcast.dialog = dialog
			current.broadcast.err = ""
			m.renderBroadcastOutput()
			v := m.commandOutputView()
			if lipgloss.Width(v) > m.width || lipgloss.Height(v) > m.height {
				t.Fatal(size, v)
			}
			if dialog != "" && m.width >= 20 && !strings.Contains(v, "[Cancel]") {
				t.Fatal("confirmation invisible", size, v)
			}
		}
	}
}
