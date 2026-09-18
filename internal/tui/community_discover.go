package tui

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/charmbracelet/x/ansi"
)

var discoverModes = []struct {
	label, kind string
}{
	{"Interests", "interests"},
	{"Personal recommendations", "personal"},
	{"Global recommendations", "global"},
	{"Similar users", "similar"},
	{"Item recommendations", "item"},
	{"Users by item", "item-users"},
	{"User interests", "user-interests"},
	{"My profile", "profile"},
}

type discoverDraft struct {
	text     string
	cursor   int
	opinion  string
	original string
	revision uint64
}

type discoverDialog struct {
	kind, label, item string
	identity          daemon.CommunityIdentity
	revision          uint64
	confirm           bool
	scroll            int
}

type communityDiscoverModel struct {
	mode, row                   int
	interests                   []daemon.CommunityInterest
	rows                        []daemon.CommunityDiscoveryRow
	next, cursor, query, target string
	state, err                  string
	revision, generation        uint64
	request                     uint64
	loading                     bool
	cancel                      context.CancelFunc
	frontend                    string
	selected                    string
	form, input, inputErr       string
	inputCursor                 int
	interestOpinion             string
	interestOriginal            string
	interestRevision            uint64
	profile                     daemon.CommunitySelfProfile
	profileLoading              bool
	profileRequest              uint64
	profileDraft                string
	profileCursor               int
	profileScroll               int
	profileDirty                bool
	drafts                      map[chatKey]discoverDraft
	dialog                      *discoverDialog
	operation                   uint64
	busy                        bool
	profileBaseRevision         uint64
	profileOriginal             string
	interestOriginalOpinion     string
}

type discoverPageMsg struct {
	request      uint64
	identity     daemon.CommunityIdentity
	kind, target string
	interests    daemon.CommunityInterestsPage
	discovery    daemon.CommunityDiscoveryPage
	err          error
}
type discoverInterestActionMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	item     string
	result   daemon.CommunityInterestResult
	remove   bool
	opinion  string
	draftKey chatKey
	err      error
}
type discoverProfileMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	profile  daemon.CommunitySelfProfile
	err      error
}

type discoverProfileSaveMsg struct {
	request   uint64
	identity  daemon.CommunityIdentity
	profile   daemon.CommunitySelfProfile
	draftKey  chatKey
	submitted string
	err       error
}

func (d *communityDiscoverModel) cancelLoad() {
	if d.cancel != nil {
		d.cancel()
	}
	d.cancel, d.loading = nil, false
	d.request++
	d.profileRequest++
	d.profileLoading = false
}
func (d *communityDiscoverModel) reset(accountChanged bool) {
	d.cancelLoad()
	frontend, drafts := d.frontend, d.drafts
	if accountChanged {
		frontend = ""
	}
	*d = communityDiscoverModel{request: d.request, frontend: frontend, drafts: drafts}
}
func (d communityDiscoverModel) available(c communityModel) bool {
	return c.supports("discovery") || c.supports("interests") || c.supports("self-profile")
}
func (d communityDiscoverModel) kind() string {
	if d.mode < 0 || d.mode >= len(discoverModes) {
		return "interests"
	}
	return discoverModes[d.mode].kind
}
func (m model) discoverDraftKey() chatKey {
	d := m.community.discover
	target := d.interestOriginal
	if d.form == "profile" {
		target = "self"
	}
	return chatDraftKey(m.community.summary.Account, target, "discover-"+d.form)
}
func (m *model) saveDiscoverDraft() {
	d := &m.community.discover
	if d.form != "interest" && d.form != "profile" {
		return
	}
	if d.drafts == nil {
		d.drafts = map[chatKey]discoverDraft{}
	}
	draft := discoverDraft{text: d.input, cursor: d.inputCursor, opinion: d.interestOpinion, original: d.interestOriginal, revision: d.interestRevision}
	dirty := d.input != d.interestOriginal || d.interestOriginal != "" && d.interestOpinion != d.interestOriginalOpinion
	if d.form == "profile" {
		draft = discoverDraft{text: d.profileDraft, cursor: d.profileCursor, original: d.profileOriginal, revision: d.profileBaseRevision}
		dirty = d.profileDraft != d.profileOriginal
		d.profileDirty = dirty
	}
	if dirty {
		d.drafts[m.discoverDraftKey()] = draft
	} else {
		delete(d.drafts, m.discoverDraftKey())
	}
}
func (m *model) restoreDiscoverDraft() {
	d := &m.community.discover
	draft, ok := d.drafts[m.discoverDraftKey()]
	if !ok {
		return
	}
	if d.form == "profile" {
		d.profileDraft, d.profileCursor, d.profileOriginal, d.profileBaseRevision, d.profileDirty = draft.text, draft.cursor, draft.original, draft.revision, true
	} else {
		d.input, d.inputCursor, d.interestOpinion, d.interestRevision = draft.text, draft.cursor, draft.opinion, draft.revision
	}
}
func (m *model) loadCommunityDiscover(force bool) tea.Cmd {
	d := &m.community.discover
	if m.client == nil || m.workspace != workspaceCommunity || m.community.view != 3 || d.form != "" || d.busy || d.loading || d.profileLoading {
		return nil
	}
	kind := d.kind()
	capability := "discovery"
	if kind == "interests" {
		capability = "interests"
	}
	if kind == "profile" {
		capability = "self-profile"
	}
	if !m.community.supports(capability) {
		d.err = "Unavailable in this daemon; refresh Community or restart daemon"
		return nil
	}
	if d.frontend == "" {
		d.frontend = rand.Text()
	}
	if kind == "profile" {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		d.cancel = cancel
		d.profileLoading = true
		d.profileRequest++
		request, id, client := d.profileRequest, m.community.summary.CommunityIdentity, m.client
		return func() tea.Msg {
			defer cancel()
			out, err := client.CommunitySelfProfile(ctx, id)
			return discoverProfileMsg{request: request, identity: id, profile: out, err: err}
		}
	}
	if kind == "interests" && !force && d.revision == m.community.summary.Revision && d.interests != nil {
		return nil
	}
	if (kind == "item" || kind == "item-users" || kind == "user-interests") && d.target == "" {
		d.state = "idle"
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	d.cancel, d.loading, d.request = cancel, true, d.request+1
	request, id, client, target := d.request, m.community.summary.CommunityIdentity, m.client, d.target
	if kind == "interests" {
		req := daemon.CommunityInterestsRequest{CommunityIdentity: id, Query: d.query, Cursor: d.cursor, Limit: 50}
		return func() tea.Msg {
			defer cancel()
			out, err := client.CommunityInterests(ctx, req)
			return discoverPageMsg{request: request, identity: id, kind: kind, interests: out, err: err}
		}
	}
	start := m.community.summary.Connected && (d.state == "" || force && d.state != "pending" && d.state != "unknown")
	req := daemon.CommunityDiscoveryRequest{CommunityIdentity: id, Kind: kind, Target: target, Frontend: d.frontend, Cursor: d.cursor, Limit: 50, Refresh: force && start}
	if start {
		req.Cursor = ""
		d.cursor = ""
	}
	return func() tea.Msg {
		defer cancel()
		var out daemon.CommunityDiscoveryPage
		var err error
		if start {
			out, err = client.StartCommunityDiscovery(ctx, req)
		} else {
			out, err = client.CommunityDiscovery(ctx, req)
		}
		return discoverPageMsg{request: request, identity: id, kind: kind, target: target, discovery: out, err: err}
	}
}

func (m *model) applyDiscoverPage(x discoverPageMsg) tea.Cmd {
	d := &m.community.discover
	if x.request != d.request || x.identity != m.community.summary.CommunityIdentity || x.kind != d.kind() || x.kind != "interests" && x.target != d.target {
		return nil
	}
	d.loading, d.cancel = false, nil
	if x.err == nil {
		if x.kind == "interests" {
			if x.interests.CommunityIdentity != x.identity {
				x.err = daemon.ErrCommunitySession
			}
		} else if x.discovery.CommunityIdentity != x.identity || x.discovery.Kind != x.kind || x.discovery.Target != x.target {
			x.err = daemon.ErrCommunitySession
		}
	}
	if x.err != nil {
		d.err = errText(x.err)
		return nil
	}
	if x.kind == "interests" {
		if x.interests.Revision < d.revision {
			return nil
		}
		d.interests, d.next, d.revision, d.err = x.interests.Interests, x.interests.NextCursor, x.interests.Revision, ""
		d.row = max(0, min(d.row, len(d.interests)-1))
		for i, row := range d.interests {
			if row.Item == d.selected {
				d.row = i
			}
		}
		d.selected = ""
		if d.row < len(d.interests) {
			d.selected = d.interests[d.row].Item
		}
		return nil
	}
	p := x.discovery
	if p.Generation != 0 && p.Generation < d.generation || p.Revision < d.revision {
		return nil
	}
	d.rows, d.next, d.state, d.err = p.Rows, p.NextCursor, p.State, p.Error
	d.generation, d.revision = p.Generation, p.Revision
	for i, row := range m.discoverVisibleRows() {
		name := row.Item
		if row.User != nil {
			name = row.User.Username
		}
		if name == d.selected {
			d.row = i
			break
		}
	}
	m.keepDiscoverSelection()
	return nil
}
func (m *model) applyDiscoverProfile(x discoverProfileMsg) {
	d := &m.community.discover
	if x.request != d.profileRequest || x.identity != m.community.summary.CommunityIdentity {
		return
	}
	d.profileLoading = false
	if x.err != nil {
		d.err = errText(x.err)
		return
	}
	if x.profile.CommunityIdentity != x.identity {
		d.err = daemon.ErrCommunitySession.Error()
		return
	}
	if x.profile.Revision < d.profile.Revision {
		return
	}
	d.profile, d.err = x.profile, ""
	if !d.profileDirty {
		d.profileDraft, d.profileCursor = x.profile.Description, utf8.RuneCountInString(x.profile.Description)
	}
}
func (m *model) keepDiscoverSelection() {
	d := &m.community.discover
	rows := m.discoverVisibleRows()
	d.row = max(0, min(d.row, len(rows)-1))
	d.selected = ""
	if d.row < len(rows) {
		if rows[d.row].User != nil {
			d.selected = rows[d.row].User.Username
		} else {
			d.selected = rows[d.row].Item
		}
	}
}

func (m *model) setDiscoverMode(mode int) tea.Cmd {
	d := &m.community.discover
	if mode < 0 || mode >= len(discoverModes) || mode == d.mode {
		return nil
	}
	m.saveDiscoverDraft()
	d.cancelLoad()
	d.mode, d.row, d.cursor, d.next, d.state, d.err, d.selected = mode, 0, "", "", "", "", ""
	d.query, d.target, d.form = "", "", ""
	d.interests, d.rows = nil, nil
	d.revision, d.generation = 0, 0
	return m.loadCommunityDiscover(false)
}
func (m *model) discoverTarget() string {
	d := m.community.discover
	rows := m.discoverVisibleRows()
	if d.row >= 0 && d.row < len(rows) && rows[d.row].User != nil {
		return rows[d.row].User.Username
	}
	return ""
}
func (m *model) discoverMatches(row daemon.CommunityDiscoveryRow) bool {
	q := strings.ToLower(strings.TrimSpace(m.community.discover.query))
	if q == "" {
		return true
	}
	if row.User != nil {
		return strings.Contains(strings.ToLower(row.User.Username), q)
	}
	return strings.Contains(strings.ToLower(row.Item), q)
}
func (m *model) discoverVisibleRows() []daemon.CommunityDiscoveryRow {
	var out []daemon.CommunityDiscoveryRow
	for _, row := range m.community.discover.rows {
		if m.discoverMatches(row) {
			out = append(out, row)
		}
	}
	return out
}

func (m *model) sendDiscoverInterest(remove bool, opinion, item string, revision *uint64) tea.Cmd {
	d := &m.community.discover
	if m.client == nil || !m.community.supports("interests") || d.busy {
		return nil
	}
	normalized, err := soulseek.NormalizeInterest(item)
	if err != nil {
		d.inputErr = err.Error()
		return nil
	}
	id := m.community.summary.CommunityIdentity
	req := daemon.CommunityInterestRequest{CommunityIdentity: id, Item: normalized, Opinion: opinion, Revision: revision, Remove: remove, Confirm: remove}
	m.saveDiscoverDraft()
	d.cancelLoad()
	d.operation++
	d.busy = true
	d.inputErr = "Saving…"
	operation, client, key, root := d.operation, m.client, m.discoverDraftKey(), m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(root, 5*time.Second)
		defer cancel()
		out, err := client.SetCommunityInterest(ctx, req)
		return discoverInterestActionMsg{request: operation, identity: id, item: normalized, result: out, err: err, remove: remove, opinion: opinion, draftKey: key}
	}
}
func (m *model) applyDiscoverInterest(x discoverInterestActionMsg) tea.Cmd {
	d := &m.community.discover
	if x.request != d.operation || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	d.busy = false
	if x.err == nil && (x.result.CommunityIdentity != x.identity || x.result.Interest.Item != x.item || x.result.Removed != x.remove || !x.remove && x.result.Interest.Opinion != x.opinion) {
		x.err = daemon.ErrCommunitySession
	}
	if x.err != nil {
		d.err = errText(x.err)
		d.inputErr = d.err
		return nil
	}
	if !x.remove {
		delete(d.drafts, x.draftKey)
		if d.form == "interest" && m.discoverDraftKey() == x.draftKey {
			d.form, d.input, d.inputErr = "", "", ""
		}
	}
	d.err = ""
	m.setNotice("Interest saved")
	return tea.Batch(m.loadCommunitySummary(), m.loadCommunityDiscover(false))
}
func (m *model) sendDiscoverProfile() tea.Cmd {
	d := &m.community.discover
	if m.client == nil || !m.community.supports("self-profile") || d.busy {
		return nil
	}
	if err := soulseek.ValidateSelfDescription(d.profileDraft); err != nil {
		d.inputErr = err.Error()
		return nil
	}
	id := m.community.summary.CommunityIdentity
	req := daemon.CommunitySelfProfile{CommunityIdentity: id, Description: d.profileDraft, Revision: d.profileBaseRevision}
	m.saveDiscoverDraft()
	d.cancelLoad()
	d.operation++
	d.busy = true
	d.inputErr = "Saving…"
	operation, client, key, root := d.operation, m.client, m.discoverDraftKey(), m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(root, 5*time.Second)
		defer cancel()
		profile, err := client.SetCommunitySelfProfile(ctx, req)
		return discoverProfileSaveMsg{request: operation, identity: id, profile: profile, err: err, draftKey: key, submitted: req.Description}
	}
}
func (m *model) applyDiscoverProfileSave(x discoverProfileSaveMsg) tea.Cmd {
	d := &m.community.discover
	if x.request != d.operation || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	d.busy = false
	if x.err == nil && (x.profile.CommunityIdentity != x.identity || x.profile.Description != x.submitted) {
		x.err = daemon.ErrCommunitySession
	}
	if x.err != nil {
		d.err = errText(x.err)
		d.inputErr = d.err
		return nil
	}
	if x.profile.Revision >= d.profile.Revision {
		d.profile = x.profile
	}
	delete(d.drafts, x.draftKey)
	if d.form == "profile" && m.discoverDraftKey() == x.draftKey {
		d.form = ""
		d.profileDirty = false
	}
	d.err = ""
	m.setNotice("Profile description saved")
	return m.loadCommunitySummary()
}

func (m *model) discoverKey(k tea.KeyPressMsg) tea.Cmd {
	d := &m.community.discover
	if d.busy {
		return nil
	}
	if d.dialog != nil {
		return m.discoverDialogKey(k)
	}
	if d.form == "profile" {
		return m.discoverProfileFormKey(k)
	}
	if d.form != "" {
		return m.discoverFormKey(k)
	}
	if k.String() == "esc" || k.String() == "left" {
		if m.community.pane > 0 {
			m.community.pane--
		} else {
			d.row = max(0, d.row-1)
		}
		return nil
	}
	if m.community.pane == 0 {
		switch k.String() {
		case "up", "k":
			return m.setDiscoverMode((d.mode + len(discoverModes) - 1) % len(discoverModes))
		case "down", "j":
			return m.setDiscoverMode((d.mode + 1) % len(discoverModes))
		case "enter", "right":
			m.community.pane = 1
			return nil
		case "r":
			return tea.Batch(m.loadCommunitySummary(), m.loadCommunityDiscover(true))
		}
		return nil
	}
	if d.kind() == "profile" {
		return m.discoverProfileKey(k)
	}
	if m.community.pane == 2 {
		if target := m.community.target; target != "" {
			switch k.String() {
			case "U":
				m.openUserActions()
			case "r":
				return m.loadCommunityUser()
			case "up", "k":
				m.community.inspectorScroll = max(0, m.community.inspectorScroll-1)
			case "down", "j":
				m.community.inspectorScroll = min(m.communityInspectorEnd(), m.community.inspectorScroll+1)
			}
			return nil
		}
		return nil
	}
	if d.kind() == "interests" {
		return m.discoverInterestListKey(k)
	}
	return m.discoverResultKey(k)
}
func (m *model) discoverInterestListKey(k tea.KeyPressMsg) tea.Cmd {
	d := &m.community.discover
	if !m.community.supports("interests") {
		return nil
	}
	if handled, cmd := m.discoverItemAction(k.String()); handled {
		return cmd
	}
	switch k.String() {
	case "up", "k":
		d.row = max(0, d.row-1)
	case "down", "j":
		d.row = min(max(0, len(d.interests)-1), d.row+1)
	case "pgup":
		d.row = max(0, d.row-m.pageRows())
	case "pgdown":
		d.row = min(max(0, len(d.interests)-1), d.row+m.pageRows())
	case "home":
		d.row = 0
	case "end":
		d.row = max(0, len(d.interests)-1)
	case "n":
		if d.next != "" {
			d.cursor, d.next, d.row = d.next, "", 0
			return m.loadCommunityDiscover(true)
		}
	case "p":
		if d.cursor != "" {
			d.cursor, d.row = "", 0
			return m.loadCommunityDiscover(true)
		}
	case "f", "/":
		d.form, d.input, d.inputCursor, d.inputErr = "filter", d.query, utf8.RuneCountInString(d.query), ""
		d.cancelLoad()
	case "a":
		d.form, d.input, d.inputCursor, d.inputErr, d.interestOpinion = "interest", "", 0, "", "like"
		d.cancelLoad()
		d.interestOriginal, d.interestOriginalOpinion, d.interestRevision = "", "", 0
		m.restoreDiscoverDraft()
	case "e":
		if d.row < len(d.interests) {
			i := d.interests[d.row]
			d.form, d.input, d.inputCursor, d.inputErr, d.interestOriginal, d.interestRevision, d.interestOpinion = "interest", i.Item, utf8.RuneCountInString(i.Item), "", i.Item, i.Revision, i.Opinion
			d.cancelLoad()
			d.interestOriginalOpinion = i.Opinion
			m.restoreDiscoverDraft()
		}
	case "D":
		if d.row < len(d.interests) {
			i := d.interests[d.row]
			d.dialog = &discoverDialog{kind: "remove", item: i.Item, revision: i.Revision, identity: m.community.summary.CommunityIdentity, label: fmt.Sprintf("Remove interest %q?", i.Item)}
		}
	case "r":
		return tea.Batch(m.loadCommunitySummary(), m.loadCommunityDiscover(true))
	case "right", "enter":
		m.community.pane = 1
	}
	if d.row < len(d.interests) {
		d.selected = d.interests[d.row].Item
	}
	return nil
}
func (m *model) discoverResultKey(k tea.KeyPressMsg) tea.Cmd {
	d := &m.community.discover
	if handled, cmd := m.discoverItemAction(k.String()); handled {
		return cmd
	}
	visible := m.discoverVisibleRows()
	switch k.String() {
	case "up", "k":
		d.row = max(0, d.row-1)
	case "down", "j":
		d.row = min(max(0, len(visible)-1), d.row+1)
	case "pgup":
		d.row = max(0, d.row-m.pageRows())
	case "pgdown":
		d.row = min(max(0, len(visible)-1), d.row+m.pageRows())
	case "home":
		d.row = 0
	case "end":
		d.row = max(0, len(visible)-1)
	case "n":
		if d.next != "" {
			d.cursor, d.next, d.row = d.next, "", 0
			d.cancelLoad()
			d.selected = ""
			return m.loadCommunityDiscover(false)
		}
	case "p":
		if d.cursor != "" {
			d.cursor, d.row = "", 0
			d.cancelLoad()
			d.selected = ""
			return m.loadCommunityDiscover(false)
		}
	case "f", "/":
		d.form, d.input, d.inputCursor, d.inputErr = "filter", d.query, utf8.RuneCountInString(d.query), ""
		d.cancelLoad()
	case "t":
		if d.kind() == "item" || d.kind() == "item-users" || d.kind() == "user-interests" {
			d.form, d.input, d.inputCursor, d.inputErr = "target", d.target, utf8.RuneCountInString(d.target), ""
			d.cancelLoad()
		}
	case "r":
		return tea.Batch(m.loadCommunitySummary(), m.loadCommunityDiscover(true))
	case "U":
		if user := m.discoverTarget(); user != "" {
			d.selected = user
			m.openUserActions()
		}
	case "enter":
		if d.row >= 0 && d.row < len(visible) {
			row := visible[d.row]
			if row.User != nil {
				return m.openUserInspector(row.User.Username)
			}
			if row.Item != "" && (d.kind() == "personal" || d.kind() == "global") {
				_, cmd := m.discoverItemAction("u")
				return cmd
			}
		}
	case "b":
		if user := m.discoverTarget(); user != "" {
			return m.openBrowse(user, "", false)
		}
	case "m":
		if user := m.discoverTarget(); user != "" {
			return m.openCommunityChat(user, true)
		}
	}
	m.keepDiscoverSelection()
	return nil
}
func (m *model) discoverFormKey(k tea.KeyPressMsg) tea.Cmd {
	d := &m.community.discover
	switch k.String() {
	case "esc":
		m.saveDiscoverDraft()
		d.form, d.input, d.inputErr = "", "", ""
	case "ctrl+r":
		if d.form == "interest" {
			d.dialog = &discoverDialog{kind: "reload", identity: m.community.summary.CommunityIdentity, label: "Reload saved interests and discard this local draft?"}
		}
	case "tab", "shift+tab":
		if d.form == "interest" {
			if d.interestOpinion == "like" {
				d.interestOpinion = "dislike"
			} else {
				d.interestOpinion = "like"
			}
			m.saveDiscoverDraft()
		}
	case "enter":
		switch d.form {
		case "filter":
			d.query, d.form, d.row, d.selected = d.input, "", 0, ""
			if d.kind() == "interests" {
				d.cursor, d.next, d.interests = "", "", nil
				d.cancelLoad()
				return m.loadCommunityDiscover(true)
			}
			m.keepDiscoverSelection()
		case "target":
			target := d.input
			var err error
			if d.kind() == "user-interests" {
				err = soulseek.ValidateUsername(target)
			} else {
				target, err = soulseek.NormalizeInterest(target)
			}
			if err != nil {
				d.inputErr = err.Error()
				return nil
			}
			d.cancelLoad()
			d.target, d.form, d.cursor, d.next, d.row, d.state, d.selected = target, "", "", "", 0, "", ""
			d.rows = nil
			d.revision, d.generation = 0, 0
			return m.loadCommunityDiscover(false)
		case "interest":
			item, err := soulseek.NormalizeInterest(d.input)
			if err != nil {
				d.inputErr = err.Error()
				return nil
			}
			if d.interestOriginal != "" && item != d.interestOriginal {
				d.inputErr = "Edit the opinion here; use Add for a different interest"
				return nil
			}
			var revision *uint64
			if d.interestOriginal != "" {
				v := d.interestRevision
				revision = &v
			}
			return m.sendDiscoverInterest(false, d.interestOpinion, item, revision)
		}
	default:
		value, cursor, _ := editText(d.input, d.inputCursor, k)
		if len(value) > 1024 || !utf8.ValidString(value) || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) >= 0 {
			d.inputErr = "Text must fit 1024 bytes without terminal controls"
			return nil
		}
		d.input, d.inputCursor, d.inputErr = value, cursor, ""
		m.saveDiscoverDraft()
	}
	return nil
}
func (m *model) discoverProfileKey(k tea.KeyPressMsg) tea.Cmd {
	d := &m.community.discover
	switch k.String() {
	case "e", "enter":
		if !m.community.supports("self-profile") || d.profileLoading || d.profile.CommunityIdentity != m.community.summary.CommunityIdentity {
			d.err = "Wait for the saved profile to load"
			return nil
		}
		d.cancelLoad()
		d.form, d.inputErr, d.profileDirty = "profile", "", false
		d.profileDraft, d.profileCursor, d.profileOriginal, d.profileBaseRevision = d.profile.Description, utf8.RuneCountInString(d.profile.Description), d.profile.Description, d.profile.Revision
		m.restoreDiscoverDraft()
	case "r":
		return tea.Batch(m.loadCommunitySummary(), m.loadCommunityDiscover(true))
	case "esc", "left":
		m.community.pane = max(0, m.community.pane-1)
	case "up", "k":
		d.profileScroll = max(0, d.profileScroll-1)
	case "down", "j":
		d.profileScroll++
	case "pgup":
		d.profileScroll = max(0, d.profileScroll-m.pageRows())
	case "pgdown":
		d.profileScroll += m.pageRows()
	case "home":
		d.profileScroll = 0
	case "end":
		d.profileScroll = 1 << 20
	}
	return nil
}
func (m *model) discoverProfileFormKey(k tea.KeyPressMsg) tea.Cmd {
	d := &m.community.discover
	if k.String() == "esc" {
		m.saveDiscoverDraft()
		d.form, d.inputErr = "", ""
		return nil
	}
	if k.String() == "ctrl+r" {
		d.dialog = &discoverDialog{kind: "reload", identity: m.community.summary.CommunityIdentity, label: "Reload saved description and discard this local draft?"}
		return nil
	}
	if k.String() == "enter" {
		return m.sendDiscoverProfile()
	}
	value, cursor, _ := editText(d.profileDraft, d.profileCursor, k)
	if k.String() == "ctrl+j" {
		value, cursor = insertText(d.profileDraft, "\n", d.profileCursor)
	}
	if err := soulseek.ValidateSelfDescription(value); err != nil {
		d.inputErr = err.Error()
		return nil
	}
	d.profileDraft, d.profileCursor, d.inputErr = value, cursor, ""
	m.saveDiscoverDraft()
	return nil
}
func (m *model) discoverDialogKey(k tea.KeyPressMsg) tea.Cmd {
	d, dialog := &m.community.discover, m.community.discover.dialog
	switch k.String() {
	case "esc":
		d.dialog = nil
	case "left", "right", "tab", "shift+tab":
		dialog.confirm = !dialog.confirm
	case "up", "k":
		dialog.scroll = max(0, dialog.scroll-1)
	case "down", "j":
		dialog.scroll++
	case "pgup":
		dialog.scroll = max(0, dialog.scroll-m.pageRows())
	case "pgdown":
		dialog.scroll += m.pageRows()
	case "home":
		dialog.scroll = 0
	case "end":
		dialog.scroll = 1 << 20
	case "enter":
		d.dialog = nil
		if !dialog.confirm || dialog.identity != m.community.summary.CommunityIdentity {
			return nil
		}
		if dialog.kind == "reload" {
			delete(d.drafts, m.discoverDraftKey())
			d.form = ""
			d.profileDirty = false
			d.cancelLoad()
			return m.loadCommunityDiscover(true)
		}
		return m.sendDiscoverInterest(true, "", dialog.item, &dialog.revision)
	}
	return nil
}

func (m model) discoverSidebar(width, height int) []string {
	d := m.community.discover
	lines := []string{strong("Discover modes"), muted("↑↓ choose · Enter open")}
	scroll := 0
	for i, mode := range discoverModes {
		lines = append(lines, selectedRow(mode.label, i == d.mode))
		if i == d.mode {
			scroll = max(0, strings.Count(ansi.Wrap(strings.Join(lines, "\n"), max(1, width), ""), "\n")+1-height)
		}
	}
	return communityPane(lines, width, height, scroll)
}
func (m model) discoverRowsPane(width, height int) []string {
	d := m.community.discover
	if d.kind() == "profile" {
		return communityPane([]string{strong("My profile") + muted(" · e edit · r refresh"), muted("↑↓/PgUp/PgDown scroll"), danger(d.err), strong("Description:"), strings.ReplaceAll(d.profile.Description, "\t", "⇥")}, width, height, d.profileScroll)
	}
	lines := []string{strong(discoverModes[d.mode].label)}
	if d.kind() == "interests" {
		lines = append(lines, muted("a add · e edit · D remove · f filter"), muted("p/n pages"))
		if d.query != "" {
			lines = append(lines, "Find: "+d.query)
		}
		if d.err != "" {
			lines = append(lines, danger("! "+browseErrorText(d.err)))
		}
		if len(d.interests) == 0 {
			lines = append(lines, muted("No interests saved."))
		}
		lines = communityPane(lines, width, max(1, height-2), 0)
		rows := max(0, height-len(lines)-1)
		start := max(0, d.row-rows+1)
		for i := start; i < min(len(d.interests), start+rows); i++ {
			x := d.interests[i]
			state := x.Opinion + " · " + x.State
			lines = append(lines, selectedRow(ansi.Truncate(x.Item+" ("+state+")", width-2, "…"), i == d.row))
		}
		lines = append(lines, muted("s search · i recs · u users"))
		return lines[:min(len(lines), max(0, height))]
	}
	lines = append(lines, muted("Enter user/item · U actions · f filter"), muted("t target · r refresh · p/n pages"))
	if d.target != "" {
		lines = append(lines, "Target: "+d.target)
	}
	if d.state != "" {
		state := d.state
		if d.err != "" {
			state += ": " + d.err
		}
		lines = append(lines, "State: "+state)
	}
	visible := m.discoverVisibleRows()
	if len(visible) == 0 {
		lines = append(lines, muted("No results yet; r refreshes."))
	}
	lines = communityPane(lines, width, max(1, height-2), 0)
	rows := max(0, height-len(lines)-1)
	start := max(0, d.row-rows+1)
	for i := start; i < min(len(visible), start+rows); i++ {
		x := visible[i]
		label := x.Item
		if x.User != nil {
			label = x.User.Username
			if x.Rating > 0 {
				label += fmt.Sprintf(" · rating %d", x.Rating)
			}
		} else if x.Score != 0 {
			label += fmt.Sprintf(" · score %d", x.Score)
		}
		lines = append(lines, selectedRow(ansi.Truncate(label, width-2, "…"), i == d.row))
	}
	lines = append(lines, muted("p/n pages · Enter inspect/expand · b browse · m message"))
	return lines[:min(len(lines), max(0, height))]
}
func (m model) discoverFormView(width, height int) []string {
	d := m.community.discover
	if d.form == "profile" {
		return communityPane([]string{"Edit self-description", renderInputWindow(strings.ReplaceAll(strings.ReplaceAll(d.profileDraft, "\n", "↵"), "\t", "⇥"), d.profileCursor, width), d.inputErr, "Enter save · Ctrl+J newline · Ctrl+R reload · Esc keep draft"}, width, height, 0)
	}
	label := "Filter results"
	if d.form == "target" {
		label = "Discovery target (interest or exact username)"
	}
	if d.form == "interest" {
		label = "Interest · " + d.interestOpinion + " (Tab toggles like/dislike)"
	}
	return communityPane([]string{label, renderInputWindow(d.input, d.inputCursor, width), d.inputErr, "Enter submit · Esc keep draft · paste never submits"}, width, height, 0)
}

func (m *model) discoverItemAction(key string) (bool, tea.Cmd) {
	if key != "s" && key != "i" && key != "u" {
		return false, nil
	}
	d := &m.community.discover
	item := ""
	if d.kind() == "interests" {
		if d.row < len(d.interests) {
			item = d.interests[d.row].Item
		}
	} else {
		rows := m.discoverVisibleRows()
		if d.row < len(rows) {
			item = rows[d.row].Item
		}
	}
	if item == "" {
		return true, nil
	}
	if key == "s" {
		return true, m.openSearch(item)
	}
	normalized, err := soulseek.NormalizeInterest(item)
	if err != nil {
		d.err = err.Error()
		return true, nil
	}
	d.cancelLoad()
	d.mode = 4
	if key == "u" {
		d.mode = 5
	}
	d.target, d.query, d.cursor, d.next, d.state, d.selected = normalized, "", "", "", "", ""
	d.rows = nil
	d.row = 0
	d.revision, d.generation = 0, 0
	return true, m.loadCommunityDiscover(false)
}
func (m model) discoverDialogView() string {
	d := m.community.discover.dialog
	return communityConfirmationView(d.label, d.confirm, d.scroll, m.width, m.height)
}

func (m *model) pasteCommunityDiscover(text string) {
	d := &m.community.discover
	if d.busy || d.dialog != nil || d.form == "" {
		return
	}
	if !utf8.ValidString(text) {
		d.inputErr = "Invalid UTF-8 paste; nothing pasted"
		return
	}
	if d.form == "profile" {
		value, cursor := insertText(d.profileDraft, strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), d.profileCursor)
		if len(value) <= soulseek.MaxProfileDescriptionBytes && strings.IndexFunc(value, func(r rune) bool {
			return unicode.Is(unicode.Bidi_Control, r) || unicode.IsControl(r) && r != '\n' && r != '\t'
		}) < 0 {
			d.profileDraft, d.profileCursor, d.inputErr, d.profileDirty = value, cursor, "", true
			m.saveDiscoverDraft()
		} else {
			d.inputErr = "Invalid or oversized description paste; nothing pasted"
		}
		return
	}
	value, cursor := insertText(d.input, strings.ReplaceAll(text, "\r\n", "\n"), d.inputCursor)
	if len(value) <= 1024 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) < 0 {
		d.input, d.inputCursor, d.inputErr = value, cursor, ""
		m.saveDiscoverDraft()
	} else {
		d.inputErr = "Invalid paste; nothing pasted"
	}
}
