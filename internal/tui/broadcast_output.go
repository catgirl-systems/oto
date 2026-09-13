package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

type broadcastOutput struct {
	page             daemon.CommunityBroadcastPage
	cursor, reviewed int
	busy, confirm    bool
	dialog, err      string
	cancel           context.CancelFunc
	shared           *daemon.SharedSendPage
}
type broadcastOutputMsg struct {
	owner  *commandOutput
	cursor int
	page   daemon.CommunityBroadcastPage
	err    error
	shared *daemon.SharedSendPage
}

func (m *model) showBroadcastOutput(page daemon.CommunityBroadcastPage) {
	start := page.Total - len(page.Recipients)
	if page.NextCursor != 0 {
		start = page.NextCursor - len(page.Recipients)
	}
	b := &broadcastOutput{page: page, cursor: start}
	if start == 0 {
		b.reviewed = len(page.Recipients)
	}
	m.commandOutput = &commandOutput{broadcast: b}
	m.renderBroadcastOutput()
}
func (m *model) renderBroadcastOutput() {
	o := m.commandOutput
	b := o.broadcast
	p := b.page
	lines := []string{fmt.Sprintf("Broadcast · %s · %s · %d recipients", p.Audience, p.State, p.Total), "Request: " + p.RequestID}
	if b.shared != nil {
		p := b.shared
		lines = []string{fmt.Sprintf("Shared send · %s · %d files · %d permitted", p.State, p.Total, p.Eligible), "Recipient: " + strconv.Quote(p.Username), "Request: " + p.RequestID}
	}
	if b.dialog != "" {
		choice := "[Cancel]  Confirm"
		if b.confirm {
			choice = "Cancel  [Confirm]"
		}
		lines = append([]string{"Confirm " + b.dialog + "?", choice, "←→ choose · Enter accept · Esc cancel"}, lines...)
	}
	if b.shared != nil {
		p := b.shared
		lines = append(lines, "s send permitted files · x stop submissions · r refresh · ] next · [ first", fmt.Sprintf("Selected bytes: %d · all sizes known: %t", p.Bytes, p.SizesKnown), "Batch completion means submission finished, NOT file delivery.", "Already admitted files retain their individual upload controls.", fmt.Sprintf("Files %d–%d of %d:", b.cursor+1, b.cursor+len(p.Files), p.Total))
		for _, row := range p.Files {
			line := fmt.Sprintf("[%s] %s · %d bytes", row.State, strconv.Quote(row.Filename), row.Size)
			if row.UploadID != "" {
				line += " · " + row.UploadID
			}
			if row.Error != "" {
				line += " · " + strconv.Quote(row.Error)
			}
			lines = append(lines, line)
		}
	} else {
		lines = append(lines, "s send preview · x stop remaining · r refresh · ] next · [ first", "Sent = written to server, NOT recipient delivery.", "Stop does not withdraw already queued PMs; use their individual controls.", "Text (quoted, exact): "+strconv.Quote(p.Text), fmt.Sprintf("Recipients %d–%d of %d:", b.cursor+1, b.cursor+len(p.Recipients), p.Total))
		for _, row := range p.Recipients {
			line := fmt.Sprintf("[%s] %s", row.State, strconv.Quote(row.Username))
			if row.MessageID != 0 {
				line += fmt.Sprintf(" · message %d", row.MessageID)
			}
			if row.Error != "" {
				line += " · " + strconv.Quote(row.Error)
			}
			lines = append(lines, line)
		}
	}
	if b.busy {
		lines = append([]string{"Working…"}, lines...)
	}
	if b.err != "" {
		lines = append([]string{b.err}, lines...)
	}
	o.text = strings.Join(lines, "\n")
}
func (m *model) requestBroadcastOutput(action string, cursor int) tea.Cmd {
	o := m.commandOutput
	b := o.broadcast
	if b.busy || m.client == nil {
		return nil
	}
	if b.page.CommunityIdentity != m.community.summary.CommunityIdentity {
		b.err = "Session changed; close and reopen this saved operation"
		m.renderBroadcastOutput()
		return nil
	}
	req := daemon.CommunityBroadcastAction{CommunityIdentity: b.page.CommunityIdentity, RequestID: b.page.RequestID, Token: b.page.Token, Action: action, Confirm: true}
	isShared := b.shared != nil
	b.busy = true
	b.err = ""
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	b.cancel = cancel
	client := m.client
	m.renderBroadcastOutput()
	return func() tea.Msg {
		defer cancel()
		var out daemon.CommunityBroadcastPage
		var err error
		if isShared {
			var page daemon.SharedSendPage
			if action == "" {
				page, err = client.SharedSend(ctx, req.CommunityIdentity, req.RequestID, cursor)
			} else {
				page, err = client.ActSharedSend(ctx, daemon.SharedSendAction{CommunityIdentity: req.CommunityIdentity, RequestID: req.RequestID, Token: req.Token, Action: action, Confirm: true})
				cursor = 0
			}
			return broadcastOutputMsg{owner: o, cursor: cursor, page: sharedSendOutputPage(page), shared: &page, err: err}
		}
		if action == "" {
			out, err = client.CommunityBroadcast(ctx, req.CommunityIdentity, req.RequestID, cursor)
		} else {
			out, err = client.ActCommunityBroadcast(ctx, req)
			cursor = 0
		}
		return broadcastOutputMsg{owner: o, cursor: cursor, page: out, err: err}
	}
}
func (m *model) applyBroadcastOutput(x broadcastOutputMsg) tea.Cmd {
	o := m.commandOutput
	if o == nil || o != x.owner || o.broadcast == nil {
		return nil
	}
	b := o.broadcast
	b.busy = false
	b.cancel = nil
	if b.page.CommunityIdentity != m.community.summary.CommunityIdentity {
		b.err = "Session changed; close and reopen this saved operation"
	} else if x.err != nil {
		b.err = x.err.Error()
	} else if (b.shared != nil) != (x.shared != nil) || x.page.CommunityIdentity != b.page.CommunityIdentity || x.page.RequestID != b.page.RequestID {
		b.err = "Stale saved-operation response ignored"
	} else {
		b.page = x.page
		b.shared = x.shared
		b.cursor = x.cursor
		if x.cursor <= b.reviewed {
			b.reviewed = max(b.reviewed, x.cursor+len(x.page.Recipients))
		}
		o.scroll = 0
	}
	m.renderBroadcastOutput()
	return nil
}
func (m *model) broadcastOutputKey(k tea.KeyPressMsg) (bool, tea.Cmd) {
	o := m.commandOutput
	b := o.broadcast
	key := k.String()
	if b.dialog != "" {
		switch key {
		case "esc":
			b.dialog = ""
		case "tab", "shift+tab", "left", "right":
			b.confirm = !b.confirm
		case "enter":
			action, confirmed := b.dialog, b.confirm
			b.dialog = ""
			if confirmed {
				return true, m.requestBroadcastOutput(action, 0)
			}
		}
		m.renderBroadcastOutput()
		return true, nil
	}
	if key == "esc" || key == "q" {
		if b.cancel != nil {
			b.cancel()
		}
		command := "/broadcast-show "
		if b.shared != nil {
			command = "/send-show "
		}
		m.setNotice("Saved operation: " + command + b.page.RequestID)
		m.commandOutput = nil
		return true, nil
	}
	switch key {
	case "r":
		return true, m.requestBroadcastOutput("", b.cursor)
	case "]":
		if b.page.NextCursor != 0 {
			return true, m.requestBroadcastOutput("", b.page.NextCursor)
		}
		return true, nil
	case "[":
		return true, m.requestBroadcastOutput("", 0)
	case "s", "x":
		if b.busy {
			return true, nil
		}
		if b.page.CommunityIdentity != m.community.summary.CommunityIdentity {
			b.err = "Session changed; reopen this saved operation"
		} else if key == "s" && b.page.State == "preview" {
			if b.shared != nil && b.shared.Eligible == 0 {
				b.err = "No permitted files; refresh shares and recipient permissions"
			} else if b.reviewed < b.page.Total {
				b.err = "Review all pages with ] before confirming"
			} else {
				b.err = ""
				b.dialog = "send"
				b.confirm = false
				o.scroll = 0
			}
		} else if key == "x" && (b.page.State == "running" || b.page.State == "preview") {
			b.err = ""
			b.dialog = "stop"
			b.confirm = false
			o.scroll = 0
		}
		m.renderBroadcastOutput()
		return true, nil
	}
	return false, nil
}
