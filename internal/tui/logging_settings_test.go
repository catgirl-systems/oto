package tui

import (
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestLoggingSettingChoice(t *testing.T) {
	m := model{cfg: config.Default(), workspace: workspaceSettings, settingsSection: settingsLogging}
	fields := m.settingFields()
	if len(fields) != 1 || fields[0].id != settingLoggingLevel || fields[0].value != "INFO" || fields[0].kind != settingChoice {
		t.Fatalf("logging settings: %+v", fields)
	}
	m.cursor = 0
	m.key(key("enter"))
	if !m.choiceChoosing || m.choiceSetting != settingLoggingLevel {
		t.Fatalf("logging level did not open choice: %+v", m)
	}
	m.key(key("right"))
	m.key(key("right"))
	m.key(key("enter"))
	if m.cfg.Logging.Level != "ERROR" {
		t.Fatalf("logging level choice = %q, want ERROR", m.cfg.Logging.Level)
	}
	if m.settingsSaving {
		t.Fatal("choosing a level saved automatically")
	}
}
