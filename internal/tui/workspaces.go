package tui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/go-playground/validator/v10"
)

var settingValidator = validator.New()

func (m model) settingFields() []settingField {
	switch m.settingsSection {
	case settingsStatistics:
		days := m.stats.pruneDays
		if days == "" {
			days = "30"
		}
		return []settingField{
			{settingStatsLogRetention, "Transfer history retention days (0 keep forever)", strconv.Itoa(m.cfg.Statistics.LogRetentionDays), settingInt},
			{settingStatsDailyRetention, "Daily retention days (0 keep forever)", strconv.Itoa(m.cfg.Statistics.DailyRetentionDays), settingInt},
			{settingStatsASCII, "ASCII charts", strconv.FormatBool(m.cfg.Statistics.ASCIICharts), settingBool},
			{settingStatsPrune, "Prune", days + " " + pruneDayUnit(days) + " · Enter to edit", settingAction},
		}
	case settingsLogging:
		return []settingField{{settingLoggingLevel, "Level", m.choiceValue(settingLoggingLevel, m.cfg.Logging.Level), settingChoice}, {settingViewLog, "Diagnostic log", "Press Enter", settingAction}}
	case settingsShares:
		return []settingField{
			{settingAudioMetadata, "Audio metadata (optional ffprobe)", strconv.FormatBool(m.cfg.AudioMetadata), settingBool},
			{settingManageShareExclusions, "Excluded content", fmt.Sprintf("%d rules · Enter to manage", len(m.cfg.ShareExclusions)), settingAction},
		}
	case settingsBrowse:
		return []settingField{
			{settingBrowseMaxEntries, "Max entries (files + folders)", strconv.Itoa(m.cfg.Browse.MaxEntries), settingInt},
			{settingBrowseMaxCompressedMiB, "Max compressed response (MiB)", strconv.Itoa(m.cfg.Browse.MaxCompressedMiB), settingInt},
			{settingBrowseMaxDecompressedMiB, "Max decompressed response (MiB)", strconv.Itoa(m.cfg.Browse.MaxDecompressedMiB), settingInt},
		}
	case settingsAccount:
		return []settingField{
			{settingUsername, "Username", m.cfg.Soulseek.Username, settingText},
			{settingChangePassword, "Change Soulseek password", "Press Enter", settingAction},
			{settingAccountPrivileges, "Supporter privileges / gifting", "Press Enter", settingAction},
		}
	case settingsCommunity:
		s := m.community.summary.Settings
		ctcp := "off"
		if s.CTCPVersion {
			ctcp = "on"
		}
		away := "Off"
		if s.AwaySeconds > 0 {
			away = fmt.Sprintf("%ds idle", s.AwaySeconds)
		}
		if s.AwayReply {
			away += " · reply set"
		}
		aliasWord := "aliases"
		if s.Aliases == 1 {
			aliasWord = "alias"
		}
		return []settingField{
			{settingPrivacyRules, "Privacy / ignore / ban rules", countLabel(s.PrivacyRules, "rule") + " · Enter to manage", settingAction},
			{settingTextTools, "Chat text tools / CTCP", countLabel(s.Keywords+s.Substitutions+s.Censorship, "text rule") + " · CTCP " + ctcp + " · Enter to edit", settingAction},
			{settingChatCommands, "Commands / aliases help", fmt.Sprintf("%d %s · Enter for help", s.Aliases, aliasWord), settingAction},
			{settingAway, "Automatic away / replies", away + " · Enter to edit", settingAction},
		}
	case settingsAPI:
		requests, apps := "0", "0"
		if m.apiEditor != nil {
			requests = strconv.Itoa(len(m.apiEditor.requests))
			apps = strconv.Itoa(len(m.apiEditor.apps))
		} else {
			requests = strconv.Itoa(m.status.pendingAPIAuth)
		}
		pending := requests + " pending · Enter to review"
		if requests == "1" {
			pending = "1 pending · Enter to review"
		}
		return []settingField{
			{settingAPIListenAddress, "API listen address (empty = off, applies on save)", m.cfg.API.ListenAddr, settingText},
			{settingAPIAuthRequests, "Connection requests", pending, settingAction},
			{settingAPIApps, "Connected apps", apps + " apps · Enter to manage", settingAction},
		}
	case settingsConnection:
		publicIP := m.status.publicIP
		if publicIP == "" {
			publicIP = "Unknown"
		}
		portStatus := "Unavailable while offline"
		if m.status.status == daemon.StatusConnected && m.status.publicPort != 0 {
			portStatus = "Press Enter"
			if m.portChecking {
				portStatus = fmt.Sprintf("Checking %d/tcp…", m.portCheckPort)
			} else if m.portCheckPort == m.status.publicPort {
				switch m.portCheckStatus {
				case "unknown":
					portStatus = "Status unknown"
				case "open", "closed":
					portStatus = fmt.Sprintf("%d/tcp %s", m.status.publicPort, m.portCheckStatus)
				}
			}
		}
		return []settingField{
			{settingServer, "Server", m.cfg.Soulseek.Server, settingText},
			{settingListenAddress, "Listen address", m.cfg.Soulseek.ListenAddr, settingText},
			{settingNetworkInterface, "Network interface", m.networkInterfaceValue(), settingChoice},
			{settingPublicIPAddress, "Public IP address", publicIP, settingInfo},
			{settingListeningPortStatus, "Listening port status", portStatus, settingAction},
			{settingConnectOnStartup, "Connect on startup", strconv.FormatBool(m.cfg.Soulseek.ConnectOnStartup), settingBool},
			{settingNATPMPPortMapping, "NAT-PMP port forwarding", strconv.FormatBool(m.cfg.Soulseek.NATPMPPortMapping), settingBool},
			{settingUPnPPortMapping, "UPnP port forwarding", strconv.FormatBool(m.cfg.Soulseek.UPnPPortMapping), settingBool},
		}
	case settingsDownloads:
		return append(append([]settingField{
			{settingDownloadPath, "Download path", m.cfg.DownloadDir, settingText},
			{settingAfterFileCommand, "After file command", m.cfg.Downloads.AfterFileCommand, settingText},
			{settingAfterFolderCommand, "After folder command", m.cfg.Downloads.AfterFolderCommand, settingText},
			{settingFileNotifications, "File notifications", strconv.FormatBool(m.cfg.Downloads.FileNotifications), settingBool},
			{settingFolderNotifications, "Folder notifications", strconv.FormatBool(m.cfg.Downloads.FolderNotifications), settingBool},
			{settingAutoClearDownloads, "Auto-clear new completed downloads", strconv.FormatBool(m.cfg.Downloads.AutoClearCompleted), settingBool},
			{settingAutoClearFilteredDownloads, "Auto-clear new filtered downloads", strconv.FormatBool(m.cfg.Downloads.AutoClearFiltered), settingBool},
		}, m.downloadFilterFields()...), settingField{settingReceiving, "Consented received files", "Press Enter", settingAction})
	case settingsBandwidth:
		profile := m.cfg.Bandwidth.ActiveProfileLimits()
		return []settingField{
			{settingBandwidthProfile, "Active profile", m.choiceValue(settingBandwidthProfile, profile.Name), settingChoice},
			{settingBandwidthProfileName, "Profile name", profile.Name, settingText},
			{settingUploadSpeedLimit, "Upload speed limit (KiB/s)", strconv.Itoa(profile.UploadSpeedLimitKiB), settingInt},
			{settingDownloadSpeedLimit, "Download speed limit (KiB/s)", strconv.Itoa(profile.DownloadSpeedLimitKiB), settingInt},
			{settingDeleteBandwidthProfile, "Delete profile", "Press Enter", settingAction},
		}
	case settingsUploads:
		return []settingField{
			{settingUploadLimitScope, "Limit applies to", m.choiceValue(settingUploadLimitScope, uploadScopeLabel(m.cfg.Uploads.LimitScope)), settingChoice},
			{settingUploadScheduling, "Scheduling", m.choiceValue(settingUploadScheduling, uploadSchedulingLabel(m.cfg.Uploads.Scheduling)), settingChoice},
			{settingPrioritizeBuddies, "Prioritize all buddies", strconv.FormatBool(m.cfg.Uploads.PrioritizeBuddies), settingBool},
			{settingPrioritizePrivileged, "Prioritize supporter users", strconv.FormatBool(m.cfg.Uploads.PrioritizePrivileged), settingBool},
			{settingExemptBuddiesFromQueueLimits, "Exempt buddies from queue limits", strconv.FormatBool(m.cfg.Uploads.ExemptBuddiesFromQueueLimits), settingBool},
			{settingAutoClearUploads, "Auto-clear new completed uploads", strconv.FormatBool(m.cfg.Uploads.AutoClearCompleted), settingBool},
			{settingAutoClearCancelledUploads, "Auto-clear new cancelled uploads", strconv.FormatBool(m.cfg.Uploads.AutoClearCancelled), settingBool},
			{settingWaitForActiveUploadsOnQuit, "Wait for active uploads on quit", strconv.FormatBool(m.cfg.Uploads.WaitForActiveUploadsOnQuit), settingBool},
			{settingUploadFileCap, "Per-user files (queued + active, 0 unlimited)", strconv.FormatUint(m.cfg.Uploads.MaxQueuedFilesPerUser, 10), settingInt},
			{settingUploadByteCap, "Per-user bytes (queued + active, 0 unlimited)", strconv.FormatUint(m.cfg.Uploads.MaxQueuedBytesPerUser, 10) + " B", settingText},
			{settingUploadSlotBandwidth, "Slot bandwidth threshold (KiB/s, 0 fixed slots)", strconv.Itoa(m.cfg.Uploads.SlotBandwidthKiB), settingInt},
			{settingUploadSlots, "Upload slots (fixed count / bandwidth ceiling)", strconv.Itoa(m.cfg.UploadSlots), settingInt},
		}
	default:
		return []settingField{
			{settingRespondToIncomingSearches, "Respond to incoming searches", strconv.FormatBool(m.cfg.Search.RespondToIncomingSearches), settingBool},
			{settingMinimumIncomingSearchLength, "Minimum incoming search length", strconv.Itoa(m.cfg.Search.MinimumIncomingSearchLength), settingInt},
			{settingMaximumIncomingSearchResults, "Maximum incoming search results", strconv.Itoa(m.cfg.Search.MaximumIncomingSearchResults), settingInt},
			{settingRememberSearches, "Remember searches", strconv.FormatBool(m.cfg.Search.RememberSearches), settingBool},
			{settingSearchHistoryLimit, "Search history limit", strconv.Itoa(m.cfg.Search.SearchHistoryLimit), settingInt},
			{settingRememberFilters, "Remember filters", strconv.FormatBool(m.cfg.Search.RememberFilters), settingBool},
			{settingFilterHistoryLimit, "Filter history limit", strconv.Itoa(m.cfg.Search.FilterHistoryLimit), settingInt},
			{settingWishlistInterval, "Wishlist interval (minutes)", strconv.Itoa(m.cfg.Search.WishlistIntervalMinutes), settingInt},
			{settingWishlistNotifications, "Wishlist notifications", strconv.FormatBool(m.cfg.Search.WishlistNotifications), settingBool},
			{settingClearSearchHistory, "Clear search history", "Press Enter", settingAction},
			{settingClearFilterHistory, "Clear filter history", "Press Enter", settingAction},
			{settingDefaultFilter, "Default result filter", m.cfg.Search.DefaultFilter, settingText},
		}
	}
}

func (m *model) setSettingValue(value string) error {
	field := m.settingFields()[m.cursor]
	cfg := m.cfg
	var target *int
	var namespace string
	switch field.id {
	case settingStatsLogRetention:
		target, namespace = &cfg.Statistics.LogRetentionDays, "Statistics.LogRetentionDays"
	case settingStatsDailyRetention:
		target, namespace = &cfg.Statistics.DailyRetentionDays, "Statistics.DailyRetentionDays"
	case settingBrowseMaxEntries:
		target, namespace = &cfg.Browse.MaxEntries, "Browse.MaxEntries"
	case settingBrowseMaxCompressedMiB:
		target, namespace = &cfg.Browse.MaxCompressedMiB, "Browse.MaxCompressedMiB"
	case settingBrowseMaxDecompressedMiB:
		target, namespace = &cfg.Browse.MaxDecompressedMiB, "Browse.MaxDecompressedMiB"
	case settingMinimumIncomingSearchLength:
		target, namespace = &cfg.Search.MinimumIncomingSearchLength, "Search.MinimumIncomingSearchLength"
	case settingMaximumIncomingSearchResults:
		target, namespace = &cfg.Search.MaximumIncomingSearchResults, "Search.MaximumIncomingSearchResults"
	case settingSearchHistoryLimit:
		target, namespace = &cfg.Search.SearchHistoryLimit, "Search.SearchHistoryLimit"
	case settingFilterHistoryLimit:
		target, namespace = &cfg.Search.FilterHistoryLimit, "Search.FilterHistoryLimit"
	case settingWishlistInterval:
		target, namespace = &cfg.Search.WishlistIntervalMinutes, "Search.WishlistIntervalMinutes"
	case settingUploadSlots:
		target, namespace = &cfg.UploadSlots, "UploadSlots"
	case settingUploadSlotBandwidth:
		target, namespace = &cfg.Uploads.SlotBandwidthKiB, "Uploads.SlotBandwidthKiB"
	case settingUploadSpeedLimit, settingDownloadSpeedLimit:
		cfg.Bandwidth.Profiles = slices.Clone(cfg.Bandwidth.Profiles)
		i := m.activeBandwidthProfileIndex()
		target, namespace = &cfg.Bandwidth.Profiles[i].UploadSpeedLimitKiB, fmt.Sprintf("Bandwidth.Profiles[%d].UploadSpeedLimitKiB", i)
		if field.id == settingDownloadSpeedLimit {
			target, namespace = &cfg.Bandwidth.Profiles[i].DownloadSpeedLimitKiB, fmt.Sprintf("Bandwidth.Profiles[%d].DownloadSpeedLimitKiB", i)
		}
	}
	if target != nil {
		n, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		*target = n
		if err := settingValidator.StructPartial(cfg, namespace); err != nil {
			return err
		}
		m.cfg = cfg
		return nil
	}
	switch field.id {
	case settingUploadFileCap:
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return err
		}
		m.cfg.Uploads.MaxQueuedFilesPerUser = n
	case settingUploadByteCap:
		n, err := parseByteLimit(value)
		if err != nil {
			return err
		}
		m.cfg.Uploads.MaxQueuedBytesPerUser = n
	case settingDownloadRule, settingAddDownloadRule:
		return m.setDownloadRule(value, field.id == settingAddDownloadRule)
	case settingUsername:
		m.cfg.Soulseek.Username = value
	case settingServer:
		m.cfg.Soulseek.Server = value
	case settingListenAddress:
		m.cfg.Soulseek.ListenAddr = value
	case settingAPIListenAddress:
		m.cfg.API.ListenAddr = value
	case settingNetworkInterface:
		if value == "" {
			return errors.New("network interface cannot be empty; choose Automatic to clear it")
		}
		m.cfg.Soulseek.NetworkInterface = value
	case settingDownloadPath:
		m.cfg.DownloadDir = value
	case settingAfterFileCommand:
		m.cfg.Downloads.AfterFileCommand = value
	case settingAfterFolderCommand:
		m.cfg.Downloads.AfterFolderCommand = value
	case settingDefaultFilter:
		if err := daemon.ValidateSearchFilter(value); err != nil {
			return err
		}
		m.cfg.Search.DefaultFilter = value
	case settingBandwidthProfile:
		if !m.addingBandwidthProfile {
			return nil
		}
		if err := m.validateBandwidthProfileName(value, -1); err != nil {
			return err
		}
		m.cfg.Bandwidth.Profiles = append(m.cfg.Bandwidth.Profiles, config.BandwidthProfile{Name: value})
		m.cfg.Bandwidth.ActiveProfile = value
		m.addingBandwidthProfile = false
	case settingBandwidthProfileName:
		i := m.activeBandwidthProfileIndex()
		if err := m.validateBandwidthProfileName(value, i); err != nil {
			return err
		}
		m.cfg.Bandwidth.Profiles[i].Name = value
		m.cfg.Bandwidth.ActiveProfile = value
	}
	return nil
}

func (m model) activeBandwidthProfileIndex() int {
	for i, profile := range m.cfg.Bandwidth.Profiles {
		if profile.Name == m.cfg.Bandwidth.ActiveProfile {
			return i
		}
	}
	return 0
}

func (m model) validateBandwidthProfileName(name string, except int) error {
	if err := config.ValidateBandwidthProfileName(name); err != nil {
		return err
	}
	for i, profile := range m.cfg.Bandwidth.Profiles {
		if i != except && strings.EqualFold(profile.Name, name) {
			return fmt.Errorf("bandwidth profile %q already exists", name)
		}
	}
	return nil
}

func uploadScopeLabel(scope config.UploadLimitScope) string {
	if scope == config.UploadLimitPerTransfer {
		return "Each transfer"
	}
	return "All transfers"
}

func uploadSchedulingLabel(scheduling config.UploadScheduling) string {
	switch scheduling {
	case config.UploadSchedulingRoundRobin:
		return "Round-robin"
	case config.UploadSchedulingRandom:
		return "Random"
	case config.UploadSchedulingSmallestFirst:
		return "Smallest first"
	default:
		return "FIFO"
	}
}

func (m model) choiceOptions(id settingID) []string {
	switch id {
	case settingNetworkInterface:
		return append(append([]string{"Automatic"}, m.networkInterfaces...), "Custom…")
	case settingBandwidthProfile:
		options := make([]string, 0, len(m.cfg.Bandwidth.Profiles)+1)
		for _, profile := range m.cfg.Bandwidth.Profiles {
			options = append(options, profile.Name)
		}
		return append(options, "New…")
	case settingUploadLimitScope:
		return []string{"All transfers", "Each transfer"}
	case settingLoggingLevel:
		return []string{"DEBUG", "INFO", "WARN", "ERROR"}
	case settingUploadScheduling:
		return []string{"FIFO", "Round-robin", "Random", "Smallest first"}
	default:
		return nil
	}
}

func (m model) choiceValue(id settingID, current string) string {
	if m.choiceChoosing && m.choiceSetting == id {
		options := m.choiceOptions(id)
		if m.choiceIndex >= 0 && m.choiceIndex < len(options) {
			return options[m.choiceIndex]
		}
	}
	return current
}

func (m model) configuredChoice(id settingID) int {
	options := m.choiceOptions(id)
	current := ""
	switch id {
	case settingNetworkInterface:
		if m.cfg.Soulseek.NetworkInterface == "" {
			current = "Automatic"
		} else {
			current = m.cfg.Soulseek.NetworkInterface
		}
	case settingBandwidthProfile:
		current = m.cfg.Bandwidth.ActiveProfile
	case settingUploadLimitScope:
		current = uploadScopeLabel(m.cfg.Uploads.LimitScope)
	case settingUploadScheduling:
		current = uploadSchedulingLabel(m.cfg.Uploads.Scheduling)
	case settingLoggingLevel:
		current = m.cfg.Logging.Level
	}
	for i, option := range options {
		if option == current {
			return i
		}
	}
	return len(options) - 1
}

func (m model) networkInterfaceValue() string {
	if m.choiceChoosing && m.choiceSetting == settingNetworkInterface {
		return m.choiceValue(settingNetworkInterface, "")
	}
	if m.cfg.Soulseek.NetworkInterface == "" {
		return "Automatic"
	}
	for _, name := range m.networkInterfaces {
		if name == m.cfg.Soulseek.NetworkInterface {
			return name
		}
	}
	return "Custom: " + m.cfg.Soulseek.NetworkInterface
}
