package tui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

type communityPeerModel struct {
	profile                              daemon.CommunityProfile
	interests                            daemon.CommunityDiscoveryPage
	request, operation                   uint64
	cancel, saveCancel                   context.CancelFunc
	frontend, cursor, err, interestErr   string
	loading, form, dialog, confirm, busy bool
	path                                 string
	pathCursor                           int
	dialogScroll                         int
	image                                daemon.CommunityProfilePictureRequest
}
type communityPeerMsg struct {
	request          uint64
	identity         daemon.CommunityIdentity
	username         string
	profile          daemon.CommunityProfile
	interests        daemon.CommunityDiscoveryPage
	err, interestErr error
}
type communityPictureSavedMsg struct {
	operation uint64
	identity  daemon.CommunityIdentity
	path      string
	err       error
}

func (p *communityPeerModel) reset() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.saveCancel != nil {
		p.saveCancel()
	}
	*p = communityPeerModel{request: p.request + 1, operation: p.operation + 1, frontend: p.frontend}
}

func (p *communityPeerModel) cancelLoad() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.loading = false
	p.request++
}
func (m *model) loadCommunityPeer(force bool) tea.Cmd {
	c, p := &m.community, &m.community.peer
	if force && p.loading {
		p.cancelLoad()
	}
	if m.client == nil || m.workspace != workspaceCommunity || c.target == "" || !c.supports("profiles") || p.loading || p.form || p.dialog || p.busy || c.pane != 2 && m.width < 110 {
		return nil
	}
	if p.frontend == "" {
		p.frontend = rand.Text()
	}
	start := c.summary.Connected && (force || p.profile.Username != c.target || p.profile.State == "")
	if force {
		p.cursor = ""
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	p.cancel, p.loading, p.request = cancel, true, p.request+1
	request, id, username, client := p.request, c.summary.CommunityIdentity, c.target, m.client
	req := daemon.CommunityProfileRequest{CommunityIdentity: id, Username: username, Frontend: p.frontend, Refresh: force}
	discovery := daemon.CommunityDiscoveryRequest{CommunityIdentity: id, Kind: "user-interests", Target: username, Frontend: p.frontend, Cursor: p.cursor, Limit: 50, Refresh: force}
	supportsDiscovery := c.supports("discovery")
	return func() tea.Msg {
		defer cancel()
		out := communityPeerMsg{request: request, identity: id, username: username}
		if start {
			out.profile, out.err = client.StartCommunityProfile(ctx, req)
		} else {
			out.profile, out.err = client.CommunityProfile(ctx, req)
		}
		if supportsDiscovery {
			if start {
				discovery.Cursor = ""
				out.interests, out.interestErr = client.StartCommunityDiscovery(ctx, discovery)
			} else {
				out.interests, out.interestErr = client.CommunityDiscovery(ctx, discovery)
			}
		}
		return out
	}
}
func (m *model) applyCommunityPeer(x communityPeerMsg) {
	c, p := &m.community, &m.community.peer
	if x.request != p.request || x.identity != c.summary.CommunityIdentity || x.username != c.target {
		return
	}
	p.loading, p.cancel = false, nil
	if x.err == nil && (x.profile.CommunityIdentity != x.identity || x.profile.Username != x.username) {
		x.err = daemon.ErrCommunitySession
	}
	if x.err != nil {
		p.err = errText(x.err)
	} else if x.profile.Revision >= p.profile.Revision && (x.profile.Generation >= p.profile.Generation || x.profile.Generation == 0) {
		p.profile, p.err = x.profile, ""
	}
	if c.supports("discovery") {
		if x.interestErr == nil && (x.interests.CommunityIdentity != x.identity || x.interests.Kind != "user-interests" || x.interests.Target != x.username) {
			x.interestErr = daemon.ErrCommunitySession
		}
		if x.interestErr != nil {
			p.interestErr = errText(x.interestErr)
		} else if x.interests.Revision >= p.interests.Revision && (x.interests.Generation >= p.interests.Generation || x.interests.Generation == 0) {
			p.interests, p.interestErr = x.interests, ""
		}
	}
}
func (c communityModel) peerLines() []string {
	if !c.supports("profiles") {
		return []string{"Peer profiles unavailable in this daemon"}
	}
	p := c.peer
	state := p.profile.State
	if p.err != "" {
		state = "stale"
	}
	if state == "" {
		state = "loading"
	}
	if !c.summary.Connected {
		state = "offline"
	}
	lines := []string{"Peer profile: " + state}
	if p.err != "" {
		lines = append(lines, "! "+p.err)
	}
	if p.profile.Error != "" {
		lines = append(lines, "! "+p.profile.Error)
	}
	if !p.profile.UpdatedAt.IsZero() {
		lines = append(lines, "Fetched: "+p.profile.UpdatedAt.UTC().Format(time.RFC3339))
		policy := "unknown"
		if p.profile.UploadAllowedKnown {
			policy = fmt.Sprintf("%d (peer reported; not share access)", p.profile.UploadAllowed)
		}
		lines = append(lines, "Unsolicited upload policy: "+policy)
		suffix := ""
		if state != "ready" || p.err != "" {
			suffix = " (stale)"
		}
		lines = append(lines, fmt.Sprintf("Slots%s: %d · available: %t", suffix, p.profile.UploadSlots, p.profile.SlotsAvailable), fmt.Sprintf("Queue%s: %d", suffix, p.profile.QueueLength))
		if p.profile.PictureType != "" {
			lines = append(lines, fmt.Sprintf("Picture%s: %s %dx%d · P save", suffix, p.profile.PictureType, p.profile.PictureWidth, p.profile.PictureHeight))
		}
		lines = append(lines, "Description"+suffix+":", strings.ReplaceAll(p.profile.Description, "\t", "⇥"))
	}
	if c.supports("discovery") {
		interestState := p.interests.State
		if p.interestErr != "" {
			interestState = "stale"
		}
		if !c.summary.Connected {
			interestState = "offline (stale)"
		}
		lines = append(lines, "Interests: "+interestState+" · p first / n next page")
		if p.interestErr != "" {
			lines = append(lines, "! "+p.interestErr)
		}
		if p.interests.Error != "" {
			lines = append(lines, "! "+p.interests.Error)
		}
		for _, row := range p.interests.Rows {
			lines = append(lines, row.Kind+": "+row.Item)
		}
	}
	return lines
}
func (m *model) peerKey(k tea.KeyPressMsg) (bool, tea.Cmd) {
	p := &m.community.peer
	if p.busy {
		return true, nil
	}
	if p.dialog {
		if scrollKey(k.String(), &p.dialogScroll, m.pageRows()) {
			return true, nil
		}
		switch k.String() {
		case "esc":
			p.dialog = false
		case "left", "right", "tab", "shift+tab":
			p.confirm = !p.confirm
		case "enter":
			p.dialog = false
			if p.confirm {
				return true, m.savePeerPicture()
			}
		}
		return true, nil
	}
	if p.form {
		switch k.String() {
		case "esc":
			p.form = false
		case "enter":
			if strings.TrimSpace(p.path) == "" {
				p.err = "Enter a destination path"
			} else {
				p.dialog, p.confirm = true, false
				p.dialogScroll = 0
			}
		default:
			value, cursor, _ := editText(p.path, p.pathCursor, k)
			if singleLineText(value, 4096) {
				p.path, p.pathCursor = value, cursor
			}
		}
		return true, nil
	}
	if m.community.pane != 2 {
		return false, nil
	}
	switch k.String() {
	case "r":
		return true, tea.Batch(m.loadCommunitySummary(), m.loadCommunityUser(), m.loadCommunityPeer(true))
	case "P":
		if !m.community.supports("profiles") {
			m.setNotice("Peer profiles unavailable; refresh Community")
			return true, nil
		}
		if p.profile.PictureType == "" || p.profile.Username != m.community.target {
			p.err = "No cached picture; r refreshes the profile"
			return true, nil
		}
		hash := sha256.Sum256([]byte(p.profile.Username))
		extension := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif"}[p.profile.PictureType]
		p.path = filepath.Join(m.cfg.DownloadDir, fmt.Sprintf("profile-%x.%s", hash[:8], extension))
		p.pathCursor = utf8.RuneCountInString(p.path)
		p.image = daemon.CommunityProfilePictureRequest{CommunityIdentity: m.community.summary.CommunityIdentity, Username: p.profile.Username, Revision: p.profile.PictureRevision}
		p.form, p.err = true, ""
		return true, nil
	case "n":
		if p.interests.NextCursor != "" {
			p.cancelLoad()
			p.cursor = p.interests.NextCursor
			return true, m.loadCommunityPeer(false)
		}
		return true, nil
	case "p":
		p.cancelLoad()
		p.cursor = ""
		return true, m.loadCommunityPeer(false)
	}
	return false, nil
}
func (m *model) savePeerPicture() tea.Cmd {
	p := &m.community.peer
	if p.image.CommunityIdentity != m.community.summary.CommunityIdentity {
		p.err = daemon.ErrCommunitySession.Error()
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	p.saveCancel = cancel
	p.busy = true
	p.operation++
	operation, req, path, client := p.operation, p.image, p.path, m.client
	return func() tea.Msg {
		defer cancel()
		out, err := client.CommunityProfilePicture(ctx, req)
		if err == nil && (out.CommunityIdentity != req.CommunityIdentity || out.Username != req.Username || out.Revision != req.Revision || len(out.Data) > soulseek.MaxProfilePictureBytes) {
			err = daemon.ErrCommunitySession
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			var f *os.File
			f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err == nil {
				_, err = f.Write(out.Data)
				if err == nil {
					err = f.Sync()
				}
				closeErr := f.Close()
				if err == nil {
					err = closeErr
				}
				if err != nil {
					_ = os.Remove(path)
				}
			}
		}
		return communityPictureSavedMsg{operation: operation, identity: req.CommunityIdentity, path: path, err: err}
	}
}
func (m *model) applyPeerPicture(x communityPictureSavedMsg) {
	p := &m.community.peer
	if x.identity != m.community.summary.CommunityIdentity || x.operation != p.operation {
		return
	}
	p.busy, p.saveCancel = false, nil
	if x.err != nil {
		p.err = errText(x.err)
		return
	}
	p.form, p.err = false, ""
	m.setNotice("Saved profile picture: " + x.path)
}
func (m model) peerPictureForm(width, height int) []string {
	p := m.community.peer
	return communityPane([]string{"Save picture for " + p.image.Username, renderInputWindow(p.path, p.pathCursor, width), p.err, "Enter review · Esc cancel · existing files never overwritten"}, width, height, 0)
}

func (m *model) pastePeerPath(text string) {
	p := &m.community.peer
	if !p.form || p.dialog || p.busy {
		return
	}
	value, cursor := insertText(p.path, text, p.pathCursor)
	if !singleLineText(value, 4096) {
		p.err = "Invalid path paste; nothing pasted"
		return
	}
	p.path, p.pathCursor, p.err = value, cursor, ""
}
