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
	if !m.shareExclusions.open || !strings.Contains(m.renderSettings(116, 24), "Applied") {
		t.Fatal("manager did not open with the applied rules")
	}
	stage := func(value string) {
		m.shareExclusions.value, m.shareExclusions.caret = value, len([]rune(value))
		m.key(key("enter"))
	}
	m.key(key("enter"))
	stage(`Music\Cache/*`)
	if m.cfg.ShareExclusions[0] != "Music/Cache/*" || cfg.ShareExclusions[0] != ".*" {
		t.Fatal("edit not normalized or not staged independently")
	}
	m.key(key("enter"))
	stage("../bad")
	if !m.shareExclusions.editing || m.shareExclusions.err == "" || m.cfg.ShareExclusions[0] != "Music/Cache/*" {
		t.Fatal("invalid edit committed")
	}
	m.key(key("esc"))
	if !m.shareExclusions.open || m.shareExclusions.editing {
		t.Fatal("cancelling an edit closed the manager")
	}
	m.key(key("a"))
	stage("*.scratch")
	m.key(key("d"))
	if len(m.cfg.ShareExclusions) != len(cfg.ShareExclusions) {
		t.Fatal("add/delete failed")
	}
	m.key(key("R"))
	if !m.uploadConfirm || m.uploadConfirmChoice != 0 || !strings.Contains(m.uploadConfirmView(), "Restore share exclusions") {
		t.Fatal("restore confirmation not default No")
	}
	m.key(key("enter"))
	if m.cfg.ShareExclusions[0] != "Music/Cache/*" {
		t.Fatal("No restored defaults")
	}
	m.key(key("R"))
	m.key(key("y"))
	if !slices.Equal(m.cfg.ShareExclusions, config.DefaultShareExclusions()) {
		t.Fatal("Yes did not restore defaults")
	}
	for len(m.cfg.ShareExclusions) > 0 {
		m.key(key("d"))
	}
	if m.cfg.ShareExclusions == nil || !strings.Contains(m.renderSettings(116, 24), "No configurable exclusions") {
		t.Fatal("empty editor lost explicit empty policy")
	}
	m.key(key("esc"))
	if m.shareExclusions.open || m.cursor != 1 {
		t.Fatal("back did not restore summary focus")
	}
	m.key(key("enter"))
	if !strings.Contains(m.renderSettings(116, 24), "Unsaved changes") {
		t.Fatal("back/reopen lost staged changes")
	}
	m.key(key("esc"))
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
	if len(m.cfg.ShareExclusions) != 1 || m.shareExclusions.editing {
		t.Fatal("rules changed while saving")
	}
	m.status.shareScan = &daemon.ShareScan{State: "scanning"}
	if !strings.Contains(m.renderSettings(116, 24), "Share scan: scanning") {
		t.Fatal("scan progress missing")
	}
	updated, _ := m.Update(settingsMsg{err: errors.New("scan cancelled")})
	m = updated.(model)
	if m.settingsSaving || !slices.Equal(m.shareExclusions.saved, config.DefaultShareExclusions()) || !strings.Contains(m.renderSettings(116, 24), "scan cancelled") {
		t.Fatal("failed save lost applied rules or its error")
	}
	m.key(key("s"))
	updated, _ = m.Update(settingsMsg{search: m.cfg.Search, exclusions: []string{"*.scratch"}})
	m = updated.(model)
	if m.settingsSaving || m.shareExclusions.err != "" || !strings.Contains(m.renderSettings(116, 24), "Applied") {
		t.Fatal("successful save not reflected in manager")
	}
	for _, width := range []int{1, 12, 40, 80, 120} {
		for _, height := range []int{8, 16, 30} {
			for _, editing := range []bool{false, true} {
				m.shareExclusions.editing = editing
				m.shareExclusions.value = strings.Repeat("音楽", 60) + "*.tmp"
				m.shareExclusions.caret = len([]rune(m.shareExclusions.value))
				view := m.renderSettings(width, height)
				lines := strings.Split(view, "\n")
				if len(lines) > height || strings.Contains(view, "\x1b") {
					t.Fatalf("height/color overflow at %dx%d", width, height)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("width overflow at %d: %q", width, line)
					}
				}
				if editing && width >= 12 && !strings.Contains(view, "*.tmp█") {
					t.Fatal("long rule hid the caret")
				}
			}
		}
	}
	m.shareExclusions.editing = false
	m.cfg.ShareExclusions = config.DefaultShareExclusions()
	m.key(key("end"))
	if !strings.Contains(m.renderSettings(80, 12), "*~") {
		t.Fatal("last rule not reachable in a short terminal")
	}
	for rule, want := range map[string]string{"*.partial": "ending in", "@eaDir/": "and their contents", "Thumbs.db": "Files named", "Music/*/cover.jpg": "File paths matching"} {
		if !strings.Contains(shareExclusionDescription(rule), want) {
			t.Fatalf("missing explanation for %s", rule)
		}
	}
}
