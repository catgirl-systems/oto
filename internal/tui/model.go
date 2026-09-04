package tui

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

type snapshot struct {
	status              daemon.Status
	presence            daemon.Presence
	user, publicIP, err string
	publicPort          uint16
	shareScan           *daemon.ShareScan
	shareIndexRevision  uint64
}
type result struct {
	user, path, extension, country string
	size                           uint64
	directory, free                bool
	speed, queue                   uint32
	bitrate, duration              uint32
	vbr                            bool
	sampleRate, bitDepth           uint32
	public                         bool
}
type entry struct {
	name, extension      string
	size                 uint64
	directory, private   bool
	bitrate, duration    uint32
	vbr                  bool
	sampleRate, bitDepth uint32
}

type activityKind uint8

const (
	activitySearch activityKind = iota + 1
	activityBrowse
)

type workspace int

const (
	workspaceSearch workspace = iota
	workspaceWishlist
	workspaceBrowse
	workspaceTransfers
	workspaceShares
	workspaceSettings
	workspaceCount
)

type settingsSection int

const (
	settingsAccount settingsSection = iota
	settingsConnection
	settingsDownloads
	settingsUploads
	settingsSearch
	settingsSectionCount
)

type transferTab int

const (
	transferDownloads transferTab = iota
	transferUploads
	transferTabCount
)

var transferDirections = [transferTabCount]string{"download", "upload"}

type activity struct {
	kind                     activityKind
	label                    string
	request, received, total uint64
}

type searchTab struct {
	query, id, filter, filterUndo, err string
	results                            []result
	total, found, next, cursor         int
	selected                           map[int]bool
	loading, loadingMore, searching    bool
	request, operation                 uint64
	tree                               treeState
}

type browseTab struct {
	user, target, filter, err string
	entries                   []entry
	cursor                    int
	selected                  map[int]bool
	loading, loaded           bool
	cached                    bool
	request, revision         uint64
	received, total           uint64
	savedAt                   time.Time
	tree                      treeState
}
type transfer struct {
	id, user, filename, direction, state, err string
	done, total, speed                        uint64
	queue                                     uint32
}
type share struct{ name, path string }
type download struct {
	filename string
	size     uint64
}

type settingKind uint8

type settingID uint8

const (
	settingText settingKind = iota
	settingSecret
	settingBool
	settingInt
	settingAction
	settingInfo
	settingChoice
)

const (
	settingUsername settingID = iota
	settingChangePassword
	settingServer
	settingListenAddress
	settingNetworkInterface
	settingPublicIPAddress
	settingListeningPortStatus
	settingConnectOnStartup
	settingNATPMPPortMapping
	settingUPnPPortMapping
	settingDownloadPath
	settingAfterFileCommand
	settingAfterFolderCommand
	settingUploadProfile
	settingUploadProfileName
	settingUploadSpeedLimit
	settingDeleteUploadProfile
	settingUploadLimitScope
	settingUploadScheduling
	settingRespondToIncomingSearches
	settingMinimumIncomingSearchLength
	settingMaximumIncomingSearchResults
	settingRememberSearches
	settingSearchHistoryLimit
	settingRememberFilters
	settingFilterHistoryLimit
	settingWishlistInterval
	settingWishlistNotifications
	settingClearSearchHistory
	settingClearFilterHistory
	settingDefaultFilter
	settingFileNotifications
	settingFolderNotifications
)

type settingField struct {
	id           settingID
	label, value string
	kind         settingKind
}

type model struct {
	ctx                                    context.Context
	client                                 *ipc.Client
	configPath, historyPath                string
	cfg                                    config.Config
	activeSearch                           config.Search
	acceptedSearchDefault                  string
	downloadNotification                   daemon.DownloadNotification
	history                                historyState
	historyCursor                          historyCursor
	transient                              bool
	setup, help, confirm, editing, loading bool
	choiceChoosing, addingUploadProfile    bool
	details, folderMenu, statusMenu        bool
	folderMenuEditing                      bool
	loadingMore, filterEditing             bool
	browseFindEditing                      bool
	passwordForm, passwordChanging         bool
	width, height                          int
	workspace                              workspace
	settingsSection                        settingsSection
	transferTab                            transferTab
	cursor, statusMenuChoice               int
	searchTotal, searchFound, searchNext   int
	input, query, browseUser, searchID     string
	searchFilter, searchFilterUndo         string
	searchPreFilterSet                     bool
	browseFilter                           string
	folderMenuUser, folderMenuPath         string
	folderMenuDownloadDir                  string
	folderMenuSubfolders                   []string
	folderMenuFiles                        [2][]download
	folderMenuChoice, inputCursor          int
	choiceIndex                            int
	choiceSetting                          settingID
	networkInterfaces                      []string
	setupField, passwordField              int
	setupVals                              [6]string
	passwordVals                           [2]string
	setupErr, passwordUser, passwordErr    string
	portCheckStatus                        string
	portCheckPort                          uint16
	portChecking                           bool
	status                                 snapshot
	results                                []result
	searchTabs                             []searchTab
	searchTabIndex                         int
	searchRequest                          uint64
	searchOperation                        uint64
	searchTree                             treeState
	wishlist                               []daemon.WishlistItem
	wishlistCursor                         int
	wishlistNotified                       map[string]uint64
	entries                                []entry
	browseTabs                             []browseTab
	browseTabIndex                         int
	browseRequest, browseRevision          uint64
	browseLoaded, browseCached             bool
	browseSavedAt                          time.Time
	browseTree                             treeState
	savedBrowses                           []daemon.SavedBrowse
	savedBrowseCursor                      int
	savedBrowseLoading                     bool
	transfers                              []transfer
	transferSampleAt                       time.Time
	noticeUntil                            time.Time
	transferCursors                        [transferTabCount]int
	spinner, activityFrame                 int
	activityRunning                        bool
	transferTrees                          [transferTabCount]treeState
	shares                                 []share
	shareTree                              treeState
	shareCursor                            int
	shareGeneration, shareRequest          uint64
	selected                               map[int]bool
	err, historyErr, notice                string
}

func (m model) rows() int {
	switch m.workspace {
	case workspaceSearch:
		return len(m.searchTree.visible)
	case workspaceWishlist:
		return len(m.wishlist)
	case workspaceBrowse:
		if len(m.browseTabs) == 0 {
			return len(m.savedBrowses)
		}
		return len(m.browseTree.visible)
	case workspaceTransfers:
		return len(m.transferTrees[m.transferTab].visible)
	case workspaceShares:
		return len(m.shareTree.visible)
	default:
		return len(m.settingFields())
	}
}

func (m model) pageRows() int { return max(1, m.height-8) }

func newModel(ctx context.Context, c *ipc.Client, path string, transient bool, cfg config.Config) model {
	m := model{ctx: ctx, client: c, configPath: path, historyPath: config.HistoryPath(), cfg: cfg, activeSearch: cfg.Search, acceptedSearchDefault: cfg.Search.DefaultFilter, transient: transient, width: 80, height: 24, selected: map[int]bool{}, wishlistNotified: map[string]uint64{}, setupVals: [6]string{cfg.Soulseek.Username, cfg.Soulseek.Password, cfg.Soulseek.ListenAddr, cfg.Soulseek.NetworkInterface, cfg.DownloadDir, ""}, inputCursor: utf8.RuneCountInString(cfg.Soulseek.Username), savedBrowseLoading: c != nil}
	m.historyCursor.reset("")
	return m
}

func toResults(x []daemon.SearchResult) []result {
	r := make([]result, len(x))
	for i, v := range x {
		r[i] = result{user: v.Username, path: v.Path, extension: v.Extension, country: v.CountryCode, size: v.Size, directory: v.Directory, free: v.SlotFree, speed: v.Speed, queue: v.Queue, bitrate: v.Bitrate, duration: v.Duration, vbr: v.VBR, sampleRate: v.SampleRate, bitDepth: v.BitDepth, public: v.Public}
	}
	return r
}
func toEntries(x []soulseek.ShareEntry) []entry {
	r := make([]entry, len(x))
	for i, v := range x {
		r[i] = entry{name: v.Name, extension: v.Extension, size: v.Size, directory: v.Directory, private: v.Private, bitrate: v.Bitrate, duration: v.Duration, vbr: v.VBR, sampleRate: v.SampleRate, bitDepth: v.BitDepth}
	}
	return r
}
func toTransfers(x []daemon.Transfer) []transfer {
	r := make([]transfer, len(x))
	for i, v := range x {
		r[i] = transfer{id: v.ID, user: v.Username, filename: v.Filename, direction: v.Direction, state: v.State, err: v.Error, done: v.Done, total: v.Total, queue: v.Queue}
	}
	return r
}
func toShares(x []config.Share) []share {
	r := make([]share, len(x))
	for i, v := range x {
		r[i] = share{v.Name, v.Path}
	}
	return r
}
