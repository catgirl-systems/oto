package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestUploadPolicySettingsIPCAndRendering(t *testing.T) {
	m, _ := privateChatModel(t)
	m.workspace, m.settingsSection = workspaceSettings, settingsUploads
	for _, id := range []settingID{settingPrioritizeBuddies, settingPrioritizePrivileged, settingExemptBuddiesFromQueueLimits} {
		found := false
		for i, field := range m.settingFields() {
			if field.id == id {
				m.cursor = i
				m.key(key("enter"))
				found = true
				break
			}
		}
		if !found {
			t.Fatal("missing upload preference", id)
		}
	}
	if !m.cfg.Uploads.PrioritizeBuddies || !m.cfg.Uploads.PrioritizePrivileged || !m.cfg.Uploads.ExemptBuddiesFromQueueLimits {
		t.Fatal("toggle did not edit preferences")
	}
	before, err := m.client.Status(m.ctx)
	if err != nil || before.Config.Uploads.PrioritizeBuddies || before.Config.Uploads.PrioritizePrivileged || before.Config.Uploads.ExemptBuddiesFromQueueLimits {
		t.Fatal("draft affected daemon", before.Config.Uploads, err)
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		screen := m.View().Content
		if lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height {
			t.Fatal("settings overflow", size, screen)
		}
	}
	m.width, m.height = 120, 40
	drainChat(t, &m, m.key(key("s")))
	after, err := m.client.Status(m.ctx)
	if err != nil || !after.Config.Uploads.PrioritizeBuddies || !after.Config.Uploads.PrioritizePrivileged || !after.Config.Uploads.ExemptBuddiesFromQueueLimits {
		t.Fatal("IPC preferences not saved", after.Config.Uploads, err)
	}
}
