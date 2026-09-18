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
	_, err := m.client.AddShare(m.ctx, config.Share{Name: "Music", Path: t.TempDir()})
	must(t, err)
	drainChat(t, &m, m.loadStatus())
	m.workspace, m.cursor = workspaceShares, 0
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	press(tea.KeyEnter)
	failIf(t, m.shareAccess == nil || m.shareAccess.req.Access != "public", "root editor unavailable")
	press(tea.KeyRight)
	press(tea.KeyTab)
	press(tea.KeyRight)
	failIf(t, m.shareAccess.req.Access != "buddy" || !m.shareAccess.req.Reveal || m.chatDraftCount() != 1, "tier controls")
	press(tea.KeyEnter)
	failIf(t, !m.shareAccess.preview || m.shareAccess.confirm, "unsafe default")
	press(tea.KeyEnter)
	failIf(t, m.shareAccess.preview || m.shares[0].access != "", "default cancel saved")
	press(tea.KeyEnter)
	press(tea.KeyRight)
	press(tea.KeyEnter)
	failIf(t, m.shareAccess != nil || m.shares[0].access != "buddy" || !m.shares[0].reveal || m.cfg.Shares[0].Access != "buddy", "saved permission not reflected", m.shareAccess, m.shares, m.cfg.Shares)
	drainChat(t, &m, m.saveSettings())
	drainChat(t, &m, m.loadStatus())
	failIf(t, m.shares[0].access != "buddy", "settings reverted permission")
	press(tea.KeyEnter)
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		for _, preview := range []bool{false, true} {
			m.shareAccess.preview = preview
			screen := m.View().Content
			failIf(t, lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height || !strings.Contains(screen, "buddy") || !strings.Contains(screen, "true"), "permission render", size, screen)
			failIf(t, preview && !strings.Contains(screen, "Cancel"), "cancel not visible", size)
		}
	}
	m.width, m.height = 20, 6
	press(tea.KeyPgDown)
	failIf(t, m.shareAccess.scroll == 0, "details cannot scroll")
	m.community.summary.Session++
	screen := m.View().Content
	failIf(t, !strings.Contains(screen, "Session changed"), "stale permission not visible", screen)
}
