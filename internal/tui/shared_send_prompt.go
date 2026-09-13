package tui

import (
	"context"
	"crypto/rand"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

type sharedSendPrompt struct {
	req    daemon.SharedSendRequest
	input  string
	cursor int
	busy   bool
	err    string
	cancel context.CancelFunc
}
type sharedSendPreviewMsg struct {
	owner *commandOutput
	page  daemon.SharedSendPage
	err   error
}

func (m *model) openSharedSendPrompt() tea.Cmd {
	if m.client == nil || !m.community.supports("shared-send") {
		m.setNotice("Shared-file sending unavailable; refresh Community or restart daemon")
		return nil
	}
	_, node := m.shareTree.node(m.cursor)
	if node == nil || (node.kind != treeFile && node.kind != treeFolder && node.kind != treeShareRoot) {
		m.setNotice("Select a shared file or folder first")
		return nil
	}
	p := &sharedSendPrompt{req: daemon.SharedSendRequest{CommunityIdentity: m.community.summary.CommunityIdentity, RequestID: rand.Text()}}
	if target := m.sharedSendTarget; target != nil {
		m.sharedSendTarget = nil
		if target.identity != p.req.CommunityIdentity {
			m.setNotice("Session changed; reopen User actions to select the recipient")
			return nil
		}
		p.input, p.cursor = target.username, utf8.RuneCountInString(target.username)
	}
	if node.kind == treeFile {
		p.req.Files = []string{node.path}
	} else {
		p.req.Folder = node.path
	}
	m.commandOutput = &commandOutput{sharedPrompt: p}
	m.renderSharedSendPrompt()
	return nil
}
func (m *model) renderSharedSendPrompt() {
	o := m.commandOutput
	p := o.sharedPrompt
	target := p.req.Folder
	if len(p.req.Files) > 0 {
		target = p.req.Files[0]
	}
	o.scroll = 0
	o.text = "Send shared selection\nRecipient: " + renderInputWindow(p.input, p.cursor, max(1, m.width-13)) + "\nEnter previews only · Esc closes\nSelected: " + strconv.Quote(target)
	if p.busy {
		o.text += "\nPreparing preview…"
	}
	if p.err != "" {
		o.text += "\n" + p.err
	}
}
func (m *model) editSharedSendRecipient(text string, cursor int) {
	p := m.commandOutput.sharedPrompt
	if len(text) > 1024 || !utf8.ValidString(text) || strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) >= 0 {
		p.err = "Recipient must be single-line UTF-8 within 1024 bytes"
	} else {
		if text != p.input {
			p.req.RequestID = rand.Text()
		}
		p.input, p.cursor = text, cursor
		p.err = ""
	}
	m.renderSharedSendPrompt()
}
func (m *model) sharedSendPromptKey(k tea.KeyPressMsg) tea.Cmd {
	o := m.commandOutput
	p := o.sharedPrompt
	if k.String() == "esc" {
		if p.cancel != nil {
			p.cancel()
		}
		m.commandOutput = nil
		return nil
	}
	if p.busy {
		return nil
	}
	if p.req.CommunityIdentity != m.community.summary.CommunityIdentity {
		p.err = "Session changed; close and reopen"
		m.renderSharedSendPrompt()
		return nil
	}
	if k.String() != "enter" {
		text, cursor, changed := editText(p.input, p.cursor, k)
		if changed {
			m.editSharedSendRecipient(text, cursor)
		}
		return nil
	}
	req := p.req
	req.Username = p.input
	p.busy = true
	p.err = ""
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	p.cancel = cancel
	client := m.client
	m.renderSharedSendPrompt()
	return func() tea.Msg {
		defer cancel()
		out, err := client.PreviewSharedSend(ctx, req)
		return sharedSendPreviewMsg{owner: o, page: out, err: err}
	}
}
func (m *model) applySharedSendPreview(x sharedSendPreviewMsg) tea.Cmd {
	o := m.commandOutput
	if o == nil || o != x.owner || o.sharedPrompt == nil {
		return nil
	}
	p := o.sharedPrompt
	p.busy = false
	p.cancel = nil
	if p.req.CommunityIdentity != m.community.summary.CommunityIdentity {
		p.err = "Session changed; close and reopen"
	} else if x.err != nil {
		p.err = x.err.Error()
	} else if x.page.CommunityIdentity != p.req.CommunityIdentity || x.page.RequestID != p.req.RequestID {
		p.err = "Stale shared-send preview ignored"
	} else {
		m.showSharedSendOutput(x.page)
		return nil
	}
	m.renderSharedSendPrompt()
	return nil
}
func (m *model) pasteSharedSendRecipient(text string) {
	p := m.commandOutput.sharedPrompt
	if p.busy || p.req.CommunityIdentity != m.community.summary.CommunityIdentity {
		return
	}
	value, cursor := insertText(p.input, text, p.cursor)
	m.editSharedSendRecipient(value, cursor)
}
