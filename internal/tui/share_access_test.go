package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/config"
)

func TestShareAccessUIConfirmationAndSettingsPersistence(t *testing.T) {
	m, _ := privateChatModel(t)
	if _, err := m.client.AddShare(m.ctx, config.Share{Name: "Music", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	drainChat(t, &m, m.loadStatus())
	m.workspace, m.cursor = workspaceShares, 0
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	press(tea.KeyEnter)
	if m.shareAccess == nil || m.shareAccess.req.Access != "public" {
		t.Fatal("root editor unavailable")
	}
	press(tea.KeyRight)
	press(tea.KeyTab)
	press(tea.KeyRight)
	if m.shareAccess.req.Access != "buddy" || !m.shareAccess.req.Reveal || m.chatDraftCount() != 1 {
		t.Fatal("tier controls")
	}
	press(tea.KeyEnter)
	if !m.shareAccess.preview || m.shareAccess.confirm {
		t.Fatal("unsafe default")
	}
	press(tea.KeyEnter)
	if m.shareAccess.preview || m.shares[0].access != "" {
		t.Fatal("default cancel saved")
	}
	press(tea.KeyEnter)
	press(tea.KeyRight)
	press(tea.KeyEnter)
	if m.shareAccess != nil || m.shares[0].access != "buddy" || !m.shares[0].reveal || m.cfg.Shares[0].Access != "buddy" {
		t.Fatal("saved permission not reflected", m.shareAccess, m.shares, m.cfg.Shares)
	}
	drainChat(t, &m, m.saveSettings())
	drainChat(t, &m, m.loadStatus())
	if m.shares[0].access != "buddy" {
		t.Fatal("settings reverted permission")
	}
	press(tea.KeyEnter)
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		for _, preview := range []bool{false, true} {
			m.shareAccess.preview = preview
			screen := m.View().Content
			if lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height || !strings.Contains(screen, "buddy") || !strings.Contains(screen, "true") {
				t.Fatal("permission render", size, screen)
			}
			if preview && !strings.Contains(screen, "Cancel") {
				t.Fatal("cancel not visible", size)
			}
		}
	}
	m.width, m.height = 20, 6
	press(tea.KeyPgDown)
	if m.shareAccess.scroll == 0 {
		t.Fatal("details cannot scroll")
	}
	m.community.summary.Session++
	screen := m.View().Content
	if !strings.Contains(screen, "Session changed") {
		t.Fatal("stale permission not visible", screen)
	}
}
