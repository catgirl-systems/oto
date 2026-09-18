package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

func TestStagedShareExclusionEditorAndAutoClear(t *testing.T) {
	cfg := config.Default()
	m := model{workspace: workspaceSettings, settingsSection: settingsShares, cfg: cfg, width: 120, height: 30}
	if fields := m.settingFields(); len(fields) != 2 || fields[1].id != settingManageShareExclusions {
		t.Fatalf("Shares is not a compact summary: %+v", fields)
	}
	m.key(key("d")) // Summary rows must not delete rules.
	m.cursor = 1
	m.key(key("enter"))
	failIf(t, !m.shareExclusions.open || !strings.Contains(m.renderSettings(116, 24), "Applied"), "manager did not open with the applied rules")
	stage := func(value string) {
		m.shareExclusions.value, m.shareExclusions.caret = value, len([]rune(value))
		m.key(key("enter"))
	}
	m.key(key("enter"))
	stage(`Music\Cache/*`)
	failIf(t, m.cfg.ShareExclusions[0] != "Music/Cache/*" || cfg.ShareExclusions[0] != ".*", "edit not normalized or not staged independently")
	m.key(key("enter"))
	stage("../bad")
	failIf(t, !m.shareExclusions.editing || m.shareExclusions.err == "" || m.cfg.ShareExclusions[0] != "Music/Cache/*", "invalid edit committed")
	m.key(key("esc"))
	failIf(t, !m.shareExclusions.open || m.shareExclusions.editing, "cancelling an edit closed the manager")
	m.key(key("a"))
	stage("*.scratch")
	m.key(key("d"))
	failIf(t, len(m.cfg.ShareExclusions) != len(cfg.ShareExclusions), "add/delete failed")
	m.key(key("R"))
	failIf(t, !m.uploadConfirm || m.uploadConfirmChoice != 0 || !strings.Contains(m.uploadConfirmView(), "Restore share exclusions"), "restore confirmation not default No")
	m.key(key("enter"))
	failIf(t, m.cfg.ShareExclusions[0] != "Music/Cache/*", "No restored defaults")
	m.key(key("R"))
	m.key(key("y"))
	failIf(t, !slices.Equal(m.cfg.ShareExclusions, config.DefaultShareExclusions()), "Yes did not restore defaults")
	for len(m.cfg.ShareExclusions) > 0 {
		m.key(key("d"))
	}
	failIf(t, m.cfg.ShareExclusions == nil || !strings.Contains(m.renderSettings(116, 24), "No configurable exclusions"), "empty editor lost explicit empty policy")
	m.key(key("esc"))
	failIf(t, m.shareExclusions.open || m.cursor != 1, "back did not restore summary focus")
	m.key(key("enter"))
	failIf(t, !strings.Contains(m.renderSettings(116, 24), "Unsaved changes"), "back/reopen lost staged changes")
	m.key(key("esc"))
	for _, section := range []settingsSection{settingsDownloads, settingsUploads} {
		m.settingsSection = section
		for i, field := range m.settingFields() {
			if field.id == settingAutoClearDownloads || field.id == settingAutoClearUploads || field.id == settingAutoClearCancelledUploads {
				m.cursor = i
				m.key(key("enter"))
			}
		}
	}
	failIf(t, !m.cfg.Downloads.AutoClearCompleted || !m.cfg.Uploads.AutoClearCompleted || !m.cfg.Uploads.AutoClearCancelled, "independent auto-clear toggles missing")
	for i, field := range m.settingFields() {
		if field.id == settingAutoClearCancelledUploads {
			m.cursor = i
			m.key(key("enter"))
		}
	}
	failIf(t, m.cfg.Uploads.AutoClearCancelled || !m.cfg.Uploads.AutoClearCompleted || !m.cfg.Downloads.AutoClearCompleted, "cancelled toggle changed another setting")
	if cmd := m.key(key("s")); cmd == nil || !m.settingsSaving {
		t.Fatal("upload cleanup settings did not use the normal save path")
	}
}

func TestShareScanCancelHints(t *testing.T) {
	m := model{workspace: workspaceShares, status: snapshot{shareScan: &daemon.ShareScan{ID: 7, State: "scanning"}}}
	failIf(t, !strings.Contains(strings.Join(m.footerHints(), " "), "c cancel scan") || m.key(key("c")) == nil, "scan cancellation control missing")
	for _, state := range []string{"cancelling", "cancelled", "publishing", "completed"} {
		m.status.shareScan.State = state
		failIfFmt(t, strings.Contains(strings.Join(m.footerHints(), " "), "c cancel scan") || m.key(key("c")) != nil, "cancellation offered during %s", state)
	}
}

func TestShareExclusionSaveAndLayout(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{workspace: workspaceSettings, settingsSection: settingsShares, cfg: config.Default(), width: 120, height: 30}
	m.openShareExclusions()
	m.cfg.ShareExclusions = []string{"*.scratch"}
	if cmd := m.key(key("s")); cmd == nil || !m.settingsSaving {
		t.Fatal("save not started")
	}
	if cmd := m.key(key("s")); cmd != nil {
		t.Fatal("duplicate save started")
	}
	m.key(key("d"))
	m.key(key("a"))
	failIf(t, len(m.cfg.ShareExclusions) != 1 || m.shareExclusions.editing, "rules changed while saving")
	m.status.shareScan = &daemon.ShareScan{State: "scanning"}
	failIf(t, !strings.Contains(m.renderSettings(116, 24), "Share scan: scanning"), "scan progress missing")
	updated, _ := m.Update(settingsMsg{err: errors.New("scan cancelled")})
	m = updated.(model)
	failIf(t, m.settingsSaving || !slices.Equal(m.shareExclusions.saved, config.DefaultShareExclusions()) || !strings.Contains(m.renderSettings(116, 24), "scan cancelled"), "failed save lost applied rules or its error")
	m.key(key("s"))
	updated, _ = m.Update(settingsMsg{search: m.cfg.Search, exclusions: []string{"*.scratch"}})
	m = updated.(model)
	failIf(t, m.settingsSaving || m.shareExclusions.err != "" || !strings.Contains(m.renderSettings(116, 24), "Applied"), "successful save not reflected in manager")
	for _, width := range []int{1, 12, 40, 80, 120} {
		for _, height := range []int{8, 16, 30} {
			for _, editing := range []bool{false, true} {
				m.shareExclusions.editing = editing
				m.shareExclusions.value = strings.Repeat("音楽", 60) + "*.tmp"
				m.shareExclusions.caret = len([]rune(m.shareExclusions.value))
				view := m.renderSettings(width, height)
				lines := strings.Split(view, "\n")
				failIfFmt(t, len(lines) > height || strings.Contains(view, "\x1b"), "height/color overflow at %dx%d", width, height)
				for _, line := range lines {
					failIfFmt(t, ansi.StringWidth(line) > width, "width overflow at %d: %q", width, line)
				}
				failIf(t, editing && width >= 12 && !strings.Contains(view, "*.tmp█"), "long rule hid the caret")
			}
		}
	}
	m.shareExclusions.editing = false
	m.cfg.ShareExclusions = config.DefaultShareExclusions()
	m.key(key("end"))
	failIf(t, !strings.Contains(m.renderSettings(80, 12), "*~"), "last rule not reachable in a short terminal")
	for rule, want := range map[string]string{"*.partial": "ending in", "@eaDir/": "and their contents", "Thumbs.db": "Files named", "Music/*/cover.jpg": "File paths matching"} {
		failIfFmt(t, !strings.Contains(shareExclusionDescription(rule), want), "missing explanation for %s", rule)
	}
}
