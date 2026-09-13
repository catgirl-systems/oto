package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

type shareAccessEditor struct {
	req                    daemon.ShareAccessRequest
	request                uint64
	row, scroll            int
	preview, confirm, busy bool
	err                    string
}
type shareAccessMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	result   daemon.ShareAccessResult
	err      error
}

func (m *model) openShareAccess() tea.Cmd {
	if m.client == nil || !m.community.supports("share-access") {
		m.setNotice("Share permissions unavailable; refresh Community or restart daemon")
		return nil
	}
	_, node := m.shareTree.node(m.cursor)
	if node == nil || node.kind != treeShareRoot || node.source < 0 || node.source >= len(m.shares) {
		m.setNotice("Select a share root first")
		return nil
	}
	root := m.shares[node.source]
	m.shareAccessRequest++
	access := root.access
	if access == "" {
		access = "public"
	}
	m.shareAccess = &shareAccessEditor{request: m.shareAccessRequest, req: daemon.ShareAccessRequest{CommunityIdentity: m.community.summary.CommunityIdentity, Expected: config.Share{Name: root.name, Path: root.path, Access: root.access, Reveal: root.reveal}, Revision: m.status.shareIndexRevision, Access: access, Reveal: root.reveal}}
	return nil
}

func (m *model) shareAccessKey(k tea.KeyPressMsg) tea.Cmd {
	e := m.shareAccess
	if k.String() == "ctrl+c" {
		if m.confirmChatDraftQuit() {
			return nil
		}
		if m.transient && m.active() {
			m.confirm = true
			return nil
		}
		return tea.Quit
	}
	if k.String() == "esc" {
		if e.preview {
			e.preview = false
		} else {
			m.shareAccess = nil
		}
		return nil
	}
	if e.busy {
		return nil
	}
	if e.req.CommunityIdentity != m.community.summary.CommunityIdentity {
		e.err = "Session changed; Esc, then reopen"
		return nil
	}
	if k.String() == "pgup" || k.String() == "pgdown" {
		width, height := max(1, min(80, m.width-8)), max(1, m.height-4)
		if m.width < 36 || m.height < 8 {
			width, height = max(1, m.width), max(1, m.height)
		}
		step := max(1, height-3)
		if k.String() == "pgup" {
			e.scroll = max(0, e.scroll-step)
		} else {
			e.scroll = min(max(0, len(m.shareAccessLines(width))-step), e.scroll+step)
		}
		return nil
	}
	if e.preview {
		switch k.String() {
		case "left", "right", "tab", "shift+tab":
			e.confirm = !e.confirm
		case "y":
			e.confirm = true
		case "enter":
			if !e.confirm {
				e.preview = false
				return nil
			}
			m.shareAccessRequest++
			e.request, e.busy = m.shareAccessRequest, true
			req, request, client, base := e.req, e.request, m.client, m.ctx
			req.Confirm = true
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(base, 5*time.Second)
				defer cancel()
				result, err := client.SetShareAccess(ctx, req)
				return shareAccessMsg{request, req.CommunityIdentity, result, err}
			}
		}
		return nil
	}
	switch k.String() {
	case "up", "down", "tab", "shift+tab":
		e.row = 1 - e.row
		e.scroll = 0
	case "left", "right", "space", " ":
		if e.row == 1 {
			e.req.Reveal = !e.req.Reveal
		} else {
			choices := []string{"public", "buddy", "trusted"}
			step := 1
			if k.String() == "left" {
				step = 2
			}
			e.req.Access = choices[(slices.Index(choices, e.req.Access)+step)%3]
		}
	case "enter":
		e.preview, e.confirm = true, false
	}
	return nil
}

func (m *model) applyShareAccess(x shareAccessMsg) tea.Cmd {
	e := m.shareAccess
	if e == nil || e.request != x.request || e.req.CommunityIdentity != x.identity || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	e.busy = false
	if x.err != nil {
		e.err = x.err.Error()
		return nil
	}
	if x.result.CommunityIdentity != x.identity {
		e.err = daemon.ErrCommunitySession.Error()
		return nil
	}
	m.shareAccess = nil
	m.setNotice("Share access " + x.result.State)
	return m.loadStatus()
}

func (m model) shareAccessLines(width int) []string {
	e := m.shareAccess
	lines := []string{selectedRow("Access: "+e.req.Access, e.row == 0), selectedRow(fmt.Sprintf("Reveal: %t", e.req.Reveal), e.row == 1)}
	for _, text := range []string{fmt.Sprintf("Root: %q", e.req.Expected.Name), fmt.Sprintf("Path: %q", e.req.Expected.Path), "Public → buddy → trusted access is cumulative.", "Revealing locked entries does not grant downloads.", "Bans override trust. Self remains public.", "Soulseek identities are not cryptographically authenticated."} {
		lines = append(lines, strings.Split(ansi.Wrap(text, width, ""), "\n")...)
	}
	message := e.err
	if e.busy {
		message = "Saving…"
	}
	if e.req.CommunityIdentity != m.community.summary.CommunityIdentity {
		message = "Session changed; Esc, then reopen"
	}
	if message != "" {
		lines = append(strings.Split(ansi.Wrap(fmt.Sprintf("! %q", message), width, ""), "\n"), lines...)
	}
	return lines
}

func (m model) shareAccessView() string {
	e := m.shareAccess
	width, inner, height := max(1, min(84, m.width-4)), max(1, min(80, m.width-8)), max(1, m.height-4)
	tiny := m.width < 36 || m.height < 8
	if tiny {
		width, inner, height = max(1, m.width), max(1, m.width), max(1, m.height)
	}
	title, choice := "Share access", "Tab field · ←→ change · Enter preview"
	if e.preview {
		title = "Confirm share access"
		choice = "[Cancel] Confirm"
		if e.confirm {
			choice = "Cancel [Confirm]"
		}
	}
	content := m.shareAccessLines(inner)
	space := max(1, height-3)
	start := min(e.scroll, max(0, len(content)-space))
	if e.req.CommunityIdentity != m.community.summary.CommunityIdentity {
		start = 0
	}
	lines := append([]string{title}, content[start:min(len(content), start+space)]...)
	lines = append(lines, choice, "Esc back · PgUp/Dn details")
	for i := range lines {
		lines[i] = trunc(lines[i], inner)
	}
	body := strings.Join(lines[:min(len(lines), height)], "\n")
	if tiny {
		return body
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panelStyle().Width(width).Padding(0, 1).Render(body))
}
