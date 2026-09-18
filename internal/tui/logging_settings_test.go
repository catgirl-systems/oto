package tui

import (
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
)

func TestLoggingSettingChoice(t *testing.T) {
	m := model{cfg: config.Default(), workspace: workspaceSettings, settingsSection: settingsLogging}
	fields := m.settingFields()
	failIfFmt(t, len(fields) != 2 || fields[0].id != settingLoggingLevel || fields[0].value != "INFO" || fields[0].kind != settingChoice, "logging settings: %+v", fields)
	m.cursor = 0
	m.key(key("enter"))
	failIfFmt(t, !m.choiceChoosing || m.choiceSetting != settingLoggingLevel, "logging level did not open choice: %+v", m)
	m.key(key("right"))
	m.key(key("right"))
	m.key(key("enter"))
	failIfFmt(t, m.cfg.Logging.Level != "ERROR", "logging level choice = %q, want ERROR", m.cfg.Logging.Level)
	failIf(t, m.settingsSaving, "choosing a level saved automatically")
}

func TestLoggingViewLogAction(t *testing.T) {
	m := model{cfg: config.Default(), workspace: workspaceSettings, settingsSection: settingsLogging}
	m.cursor = 1
	m.key(key("enter"))
	failIfFmt(t, m.workspace != workspaceStats || m.stats.page != len(statsPages)-1, "view log action went to workspace %d page %d", m.workspace, m.stats.page)
}
