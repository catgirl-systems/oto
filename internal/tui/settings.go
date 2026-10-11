package tui

import (
	"charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

// settingsKey handles keys specific to the Settings workspace. It reports
// false for keys left to the shared navigation and global bindings.
func (m *model) settingsKey(s string) (tea.Cmd, bool) {
	switch s {
	case "right", "l":
		return m.cycleTab(1), true
	case "left", "h":
		return m.cycleTab(-1), true
	case "enter":
		return m.activateSetting(), true
	case "s":
		return m.saveSettings(), true
	}
	return nil, false
}

// activateSetting toggles, opens or edits the setting under the cursor.
func (m *model) activateSetting() tea.Cmd {
	fields := m.settingFields()
	if m.cursor >= len(fields) {
		return nil
	}
	switch fields[m.cursor].id {
	case settingAudioMetadata:
		m.cfg.AudioMetadata = !m.cfg.AudioMetadata
	case settingStatsASCII:
		m.cfg.Statistics.ASCIICharts = !m.cfg.Statistics.ASCIICharts
	case settingViewLog:
		m.switchWorkspace(workspaceStats)
		m.stats.page = len(statsPages) - 1
		return m.refreshStats()
	case settingStatsPrune:
		m.openStatsPrune()
	case settingDownloadFilters:
		m.cfg.Downloads.FiltersEnabled = !m.cfg.Downloads.FiltersEnabled
	case settingRestoreDownloadRules:
		m.restoreDownloadRules = true
		m.uploadConfirm, m.uploadConfirmChoice = true, 0
		m.uploadConfirmLabel = "Restore default download filters"
	case settingManageShareExclusions:
		m.openShareExclusions()
	case settingNetworkInterface, settingBandwidthProfile, settingUploadLimitScope, settingUploadScheduling, settingLoggingLevel:
		m.choiceChoosing = true
		m.choiceSetting = fields[m.cursor].id
		m.choiceIndex = m.configuredChoice(m.choiceSetting)
	case settingDeleteBandwidthProfile:
		if len(m.cfg.Bandwidth.Profiles) == 1 {
			m.setNotice("At least one bandwidth profile is required")
			break
		}
		i := m.activeBandwidthProfileIndex()
		m.cfg.Bandwidth.Profiles = append(m.cfg.Bandwidth.Profiles[:i], m.cfg.Bandwidth.Profiles[i+1:]...)
		m.cfg.Bandwidth.ActiveProfile = m.cfg.Bandwidth.Profiles[min(i, len(m.cfg.Bandwidth.Profiles)-1)].Name
	case settingListeningPortStatus:
		if m.status.status != daemon.StatusConnected || m.status.publicPort == 0 {
			m.setNotice("Connect to check listening port")
			break
		}
		if m.portChecking {
			break
		}
		m.portCheckPort, m.portCheckStatus, m.portChecking, m.err = m.status.publicPort, "", true, ""
		return m.checkListeningPort()
	case settingConnectOnStartup:
		m.cfg.Soulseek.ConnectOnStartup = !m.cfg.Soulseek.ConnectOnStartup
	case settingNATPMPPortMapping:
		m.cfg.Soulseek.NATPMPPortMapping = !m.cfg.Soulseek.NATPMPPortMapping
	case settingUPnPPortMapping:
		m.cfg.Soulseek.UPnPPortMapping = !m.cfg.Soulseek.UPnPPortMapping
	case settingRespondToIncomingSearches:
		m.cfg.Search.RespondToIncomingSearches = !m.cfg.Search.RespondToIncomingSearches
	case settingRememberSearches:
		m.cfg.Search.RememberSearches = !m.cfg.Search.RememberSearches
	case settingRememberFilters:
		m.cfg.Search.RememberFilters = !m.cfg.Search.RememberFilters
	case settingWishlistNotifications:
		m.cfg.Search.WishlistNotifications = !m.cfg.Search.WishlistNotifications
	case settingFileNotifications:
		m.cfg.Downloads.FileNotifications = !m.cfg.Downloads.FileNotifications
	case settingFolderNotifications:
		m.cfg.Downloads.FolderNotifications = !m.cfg.Downloads.FolderNotifications
	case settingAutoClearDownloads:
		m.cfg.Downloads.AutoClearCompleted = !m.cfg.Downloads.AutoClearCompleted
	case settingAutoClearFilteredDownloads:
		m.cfg.Downloads.AutoClearFiltered = !m.cfg.Downloads.AutoClearFiltered
	case settingAutoClearUploads:
		m.cfg.Uploads.AutoClearCompleted = !m.cfg.Uploads.AutoClearCompleted
	case settingAutoClearCancelledUploads:
		m.cfg.Uploads.AutoClearCancelled = !m.cfg.Uploads.AutoClearCancelled
	case settingWaitForActiveUploadsOnQuit:
		m.cfg.Uploads.WaitForActiveUploadsOnQuit = !m.cfg.Uploads.WaitForActiveUploadsOnQuit
	case settingPrioritizeBuddies:
		m.cfg.Uploads.PrioritizeBuddies = !m.cfg.Uploads.PrioritizeBuddies
	case settingPrioritizePrivileged:
		m.cfg.Uploads.PrioritizePrivileged = !m.cfg.Uploads.PrioritizePrivileged
	case settingExemptBuddiesFromQueueLimits:
		m.cfg.Uploads.ExemptBuddiesFromQueueLimits = !m.cfg.Uploads.ExemptBuddiesFromQueueLimits
	case settingChangePassword:
		m.openPasswordForm()
	case settingReceiving:
		return m.openReceivingSettings()
	case settingAway:
		return m.openAwaySettings()
	case settingAPIAuthRequests:
		return m.openAPIEditor("requests")
	case settingAPIApps:
		return m.openAPIEditor("apps")
	case settingChatCommands:
		m.showChatCommandHelp()
		return nil
	case settingTextTools:
		return m.openTextTools()
	case settingPrivacyRules:
		return m.openPrivacyRules("", "")
	case settingAccountPrivileges:
		return m.openPrivileges("")
	case settingClearSearchHistory:
		m.clearHistory(false)
	case settingClearFilterHistory:
		m.clearHistory(true)
	default:
		m.beginEdit()
	}
	return nil
}

func (m model) settingsHints() []string {
	if m.shareExclusions.open {
		if m.shareExclusions.editing {
			return []string{"enter stage rule", "esc cancel edit", "←/→ move caret"}
		}
		if m.settingsSaving {
			return []string{"saving settings", "esc back"}
		}
		return []string{"a add", "enter edit", "x remove", "s save", "esc back", "R restore defaults"}
	}
	hints := []string{"s save", "[/] section"}
	fields := m.settingFields()
	if m.cursor < 0 || m.cursor >= len(fields) {
		return hints
	}
	field := fields[m.cursor]
	action := "edit"
	switch field.kind {
	case settingInfo:
		return hints
	case settingBool:
		action = "toggle"
	case settingChoice:
		action = "choose"
	case settingAction:
		switch field.id {
		case settingStatsPrune:
			action = "edit prune days"
		case settingManageShareExclusions:
			action = "manage exclusions"
		case settingChangePassword:
			action = "change password"
		case settingListeningPortStatus:
			action = "check port"
		case settingClearSearchHistory:
			action = "clear searches"
		case settingClearFilterHistory:
			action = "clear filters"
		default:
			action = "run"
		}
	}
	hints = append([]string{"enter " + action}, hints...)
	if m.downloadRuleIndex() >= 0 && m.settingsSection == settingsDownloads {
		hints = append(hints, "x remove rule")
	}
	return hints
}
