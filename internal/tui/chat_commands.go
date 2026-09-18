package tui

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/charmbracelet/x/ansi"
)

type chatCommandMsg struct {
	operation uint64
	identity  daemon.CommunityIdentity
	key       chatKey
	requestID string
	result    daemon.CommandResult
	err       error
}
type commandOutput struct {
	text         string
	title        string
	scroll       int
	broadcast    *broadcastOutput
	sharedPrompt *sharedSendPrompt
}

func (m *model) showChatCommandHelp() {
	text := "Chat: /COMMAND · headless: oto command COMMAND · interactive: oto console\nUse /aliases to list, /alias NAME EXPANSION to create, /unalias to remove.\nQuote arguments with spaces. $1..$128 positional, $* rest, $$ literal dollar.\nExample: /alias wave 'me waves at $1' then /wave Alice\nNo shell evaluation or plugins. Unknown commands are never sent as chat.\n\n"
	for _, spec := range daemon.CommandSpecs() {
		text += spec.Usage + "\n  " + spec.Description + "\n\n"
	}
	m.commandOutput = &commandOutput{title: "Chat commands & aliases", text: text}
}

func (m *model) sendChatCommand(key chatKey, d chatDraft) tea.Cmd {
	name, args, err := daemon.ParseCommand(d.text)
	if err != nil {
		m.community.chats.err = err.Error()
		return nil
	}
	if d.requestID == "" {
		d.requestID = rand.Text()
		m.community.chats.drafts[key] = d
	}
	ctx, cancel, op := m.beginChatOperation()
	identity, client := m.community.summary.CommunityIdentity, m.client
	req := daemon.CommandRequest{CommunityIdentity: identity, Name: name, Args: args, RequestID: d.requestID}
	if key.kind == "room" {
		req.Room = key.target
	} else {
		req.Username = key.target
	}
	return func() tea.Msg {
		defer cancel()
		out, err := client.RunCommand(ctx, req)
		return chatCommandMsg{operation: op, identity: identity, key: key, requestID: d.requestID, result: out, err: err}
	}
}
func (m *model) applyChatCommand(x chatCommandMsg) tea.Cmd {
	c := &m.community.chats
	if c.operation != x.operation || m.community.summary.CommunityIdentity != x.identity {
		return nil
	}
	c.busy, c.err = false, errText(x.err)
	if x.err != nil {
		var rejection *ipc.HTTPError
		if errors.As(x.err, &rejection) && (rejection.StatusCode == 400 || rejection.StatusCode == 409) {
			if d := c.drafts[x.key]; d.requestID == x.requestID {
				d.requestID = ""
				c.drafts[x.key] = d
			}
		}
		return nil
	}
	if x.result.Send != nil {
		return m.applyChatOperation(chatOperationMsg{operation: x.operation, identity: x.identity, key: x.key, requestID: x.requestID, kind: "command-send", result: *x.result.Send})
	}
	if d := c.drafts[x.key]; d.requestID == x.requestID {
		delete(c.drafts, x.key)
	}
	if m.workspace == workspaceCommunity && c.operationNavigation == c.navigation {
		data, err := json.MarshalIndent(x.result, "", "  ")
		if err != nil {
			c.err = err.Error()
			return nil
		}
		m.commandOutput = &commandOutput{text: string(data)}
		if x.result.Broadcast != nil {
			m.showBroadcastOutput(*x.result.Broadcast)
		}
		if x.result.SharedSend != nil {
			m.showSharedSendOutput(*x.result.SharedSend)
		}
	} else {
		m.setNotice("Command completed; refresh its resource to inspect the result")
	}
	return m.loadCommunitySummary()
}
func (m *model) commandOutputKey(k tea.KeyPressMsg) tea.Cmd {
	if m.commandOutput.sharedPrompt != nil {
		return m.sharedSendPromptKey(k)
	}
	if m.commandOutput.broadcast != nil {
		if handled, cmd := m.broadcastOutputKey(k); handled {
			return cmd
		}
	}
	if m.commandOutput == nil {
		return nil
	}
	if key := k.String(); key == "esc" || key == "q" {
		m.commandOutput = nil
		return nil
	}
	scrollKey(k.String(), &m.commandOutput.scroll, max(1, m.height-3))
	m.commandOutput.scroll = min(m.commandOutput.scroll, len(m.commandOutput.text))
	return nil
}
func (m model) commandOutputView() string {
	if m.width < 20 || m.height < 6 {
		return trunc("Command result: enlarge; Esc closes", max(1, m.width))
	}
	title := m.commandOutput.title
	if title == "" {
		title = "Command result"
	}
	return m.cardView(title, "↑↓ / PgUp/PgDn scroll · Esc close", func(width, rows int) []string {
		lines := strings.Split(ansi.Wrap(m.commandOutput.text, max(1, width), ""), "\n")
		start := min(m.commandOutput.scroll, max(0, len(lines)-rows))
		return lines[start:min(len(lines), start+max(1, rows))]
	})
}
