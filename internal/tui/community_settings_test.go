package tui

import (
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestCommunitySettingsRowsShowState(t *testing.T) {
	m := communityViewModel()
	m.workspace = workspaceSettings
	m.settingsSection = settingsCommunity
	m.community.summary.Settings = daemon.CommunitySettingsSummary{
		PrivacyRules:  3,
		Aliases:       2,
		Keywords:      1,
		Substitutions: 4,
		Censorship:    2,
		CTCPVersion:   true,
		AwaySeconds:   600,
		AwayReply:     true,
	}
	fields := m.settingFields()
	wantIDs := []settingID{settingPrivacyRules, settingTextTools, settingChatCommands, settingAway}
	wantLabels := []string{"Privacy / ignore / ban rules", "Chat text tools / CTCP", "Commands / aliases help", "Automatic away / replies"}
	failIf(t, len(fields) != len(wantIDs), fields)
	for i, field := range fields {
		failIf(t, field.id != wantIDs[i] || field.label != wantLabels[i], i, field)
		failIf(t, field.kind != settingAction, i, field)
		failIf(t, field.value == "" || strings.Contains(field.value, "Press Enter"), "row has no state", i, field.value)
	}
	failIf(t, !strings.Contains(fields[0].value, "3 rules") || !strings.Contains(fields[0].value, "Enter to manage"), fields[0].value)
	failIf(t, !strings.Contains(fields[1].value, "7 text rules") || !strings.Contains(fields[1].value, "CTCP on"), fields[1].value)
	failIf(t, !strings.Contains(fields[2].value, "2 aliases"), fields[2].value)
	failIf(t, !strings.Contains(fields[3].value, "600s idle") || !strings.Contains(fields[3].value, "reply set"), fields[3].value)
}
