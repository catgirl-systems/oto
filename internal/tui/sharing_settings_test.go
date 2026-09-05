package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestStagedShareExclusionEditorAndAutoClear(t *testing.T) {
	cfg := config.Default()
	m := model{workspace: workspaceSettings, settingsSection: settingsShares, cfg: cfg, width: 120, height: 30}
	m.cursor = 0
	if err := m.setSettingValue(`Music\Cache/*`); err != nil {
		t.Fatal(err)
	}
	if m.cfg.ShareExclusions[0] != "Music/Cache/*" || cfg.ShareExclusions[0] != ".*" {
		t.Fatal("edit not normalized or not staged independently")
	}
	if err := m.setSettingValue("../bad"); err == nil || m.cfg.ShareExclusions[0] != "Music/Cache/*" {
		t.Fatal("invalid edit committed")
	}
	m.cursor = len(m.cfg.ShareExclusions)
	if err := m.setSettingValue("*.scratch"); err != nil {
		t.Fatal(err)
	}
	m.key(key("d"))
	if len(m.cfg.ShareExclusions) != len(cfg.ShareExclusions) {
		t.Fatal("add/delete failed")
	}
	m.cursor = len(m.cfg.ShareExclusions) + 1
	m.key(key("enter"))
	if !m.uploadConfirm || m.uploadConfirmChoice != 0 || !strings.Contains(m.uploadConfirmView(), "Restore share exclusions") {
		t.Fatal("restore confirmation not default No")
	}
	m.key(key("enter"))
	if m.cfg.ShareExclusions[0] != "Music/Cache/*" {
		t.Fatal("No restored defaults")
	}
	m.key(key("enter"))
	m.key(key("y"))
	if !slices.Equal(m.cfg.ShareExclusions, config.DefaultShareExclusions()) {
		t.Fatal("Yes did not restore defaults")
	}
	for len(m.cfg.ShareExclusions) > 0 {
		m.cursor = 0
		m.key(key("d"))
	}
	if m.cfg.ShareExclusions == nil || len(m.settingFields()) != 2 {
		t.Fatal("empty editor lost explicit empty policy")
	}
	for _, section := range []settingsSection{settingsDownloads, settingsUploads} {
		m.settingsSection = section
		for i, field := range m.settingFields() {
			if field.id == settingAutoClearDownloads || field.id == settingAutoClearUploads {
				m.cursor = i
				m.key(key("enter"))
			}
		}
	}
	if !m.cfg.Downloads.AutoClearCompleted || !m.cfg.Uploads.AutoClearCompleted {
		t.Fatal("independent auto-clear toggles missing")
	}
}

func TestShareScanCancelHints(t *testing.T) {
	m := model{workspace: workspaceShares, status: snapshot{shareScan: &daemon.ShareScan{ID: 7, State: "scanning"}}}
	if !strings.Contains(strings.Join(m.footerHints(), " "), "c cancel scan") || m.key(key("c")) == nil {
		t.Fatal("scan cancellation control missing")
	}
	for _, state := range []string{"cancelling", "cancelled", "publishing", "completed"} {
		m.status.shareScan.State = state
		if strings.Contains(strings.Join(m.footerHints(), " "), "c cancel scan") || m.key(key("c")) != nil {
			t.Fatalf("cancellation offered during %s", state)
		}
	}
}
