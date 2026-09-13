package tui

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

type privilegeEditor struct {
	identity                  daemon.CommunityIdentity
	username, days, mode, err string
	field, cursor, scroll     int
	busy, confirm             bool
	request                   uint64
	requestID                 string
	balance                   daemon.AccountPrivileges
	result                    daemon.AccountPrivilegeGiftResult
}
type privilegeMsg struct {
	editor  *privilegeEditor
	request uint64
	balance daemon.AccountPrivileges
	result  daemon.AccountPrivilegeGiftResult
	gift    bool
	err     error
}

func (m *model) openPrivileges(username string) tea.Cmd {
	if m.client == nil || !m.community.supports("account-privileges") {
		m.setNotice("Privilege management unavailable; refresh Community or restart daemon")
		return nil
	}
	e := &privilegeEditor{identity: m.community.summary.CommunityIdentity, username: username, days: "1", mode: "balance", requestID: "gift-" + rand.Text()}
	if username != "" {
		e.mode = "edit"
		e.field = 1
		e.cursor = 1
	}
	m.privileges = e
	return m.loadPrivileges(false)
}
func (m *model) loadPrivileges(refresh bool) tea.Cmd {
	e := m.privileges
	if e == nil || e.busy {
		return nil
	}
	e.busy = true
	e.err = ""
	e.request++
	request, client, base, id := e.request, m.client, m.ctx, e.identity
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(base, 7*time.Second)
		defer cancel()
		out, err := client.AccountPrivileges(ctx, daemon.AccountPrivilegesRequest{CommunityIdentity: id, Refresh: refresh})
		return privilegeMsg{editor: e, request: request, balance: out, err: err}
	}
}
func (m *model) submitPrivilegeGift(confirm bool) tea.Cmd {
	e := m.privileges
	days, err := strconv.ParseUint(e.days, 10, 32)
	if err != nil || days == 0 {
		e.err = "Enter positive whole days"
		return nil
	}
	req := daemon.AccountPrivilegeGiftRequest{CommunityIdentity: e.identity, RequestID: e.requestID, Username: e.username, Days: uint32(days), Revision: e.balance.Revision, Confirm: confirm}
	e.busy = true
	e.err = ""
	e.request++
	request, client, base := e.request, m.client, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(base, 7*time.Second)
		defer cancel()
		out, err := client.GiftAccountPrivileges(ctx, req)
		return privilegeMsg{editor: e, request: request, result: out, gift: true, err: err}
	}
}
func (m *model) applyPrivilegeMsg(x privilegeMsg) {
	e := m.privileges
	if e == nil || e != x.editor || e.request != x.request {
		return
	}
	e.busy = false
	if e.identity != m.community.summary.CommunityIdentity {
		e.err = "Session changed; close and reopen privileges"
		return
	}
	if x.err != nil {
		e.err = x.err.Error()
		return
	}
	if !x.gift {
		e.balance = x.balance
		return
	}
	e.result = x.result
	e.balance = x.result.Balance
	e.scroll = 0
	e.confirm = false
	if x.result.State == "preview" {
		e.mode = "preview"
	} else {
		e.mode = "result"
	}
}
func (m *model) privilegesKey(k tea.KeyPressMsg) tea.Cmd {
	e := m.privileges
	if k.String() == "esc" {
		if e.mode == "preview" && !e.busy {
			e.mode = "edit"
			e.confirm = false
			e.scroll = 0
		} else {
			m.privileges = nil
		}
		return nil
	}
	if e.identity != m.community.summary.CommunityIdentity {
		e.err = "Session changed; close and reopen privileges"
		return nil
	}
	if e.busy {
		return nil
	}
	if k.String() == "ctrl+r" {
		if e.mode == "preview" {
			e.mode = "edit"
			e.confirm = false
		}
		return m.loadPrivileges(true)
	}
	if e.mode == "preview" {
		switch k.String() {
		case "left", "right":
			e.confirm = !e.confirm
		case "up":
			e.scroll = max(0, e.scroll-1)
		case "down":
			e.scroll++
		case "enter":
			if e.confirm {
				return m.submitPrivilegeGift(true)
			}
			e.mode = "edit"
			e.scroll = 0
		}
		return nil
	}
	if e.mode == "balance" || e.mode == "result" {
		switch k.String() {
		case "r":
			return m.loadPrivileges(true)
		case "g":
			if e.mode == "balance" {
				e.mode = "edit"
				e.field = 0
				e.cursor = len([]rune(e.username))
			}
		case "up":
			e.scroll = max(0, e.scroll-1)
		case "down":
			e.scroll++
		}
		return nil
	}
	switch k.String() {
	case "tab", "shift+tab":
		e.field = 1 - e.field
		e.cursor = len([]rune(e.username))
		if e.field == 1 {
			e.cursor = len([]rune(e.days))
		}
		return nil
	case "enter":
		return m.submitPrivilegeGift(false)
	}
	value := e.username
	if e.field == 1 {
		value = e.days
	}
	value, cursor, changed := editText(value, e.cursor, k)
	if changed {
		m.setPrivilegeInput(value, cursor)
	} else {
		e.cursor = cursor
	}
	return nil
}
func (m *model) setPrivilegeInput(value string, cursor int) {
	e := m.privileges
	limit := soulseek.MaxUsernameBytes
	if e.field == 1 {
		limit = 10
	}
	if len(value) > limit || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		e.err = "Invalid or oversized input; nothing changed"
		return
	}
	if e.field == 0 {
		e.username = value
	} else {
		e.days = value
	}
	e.cursor = cursor
	e.err = ""
}
func (m *model) pastePrivileges(text string) {
	e := m.privileges
	if e == nil || e.mode != "edit" || e.busy || e.identity != m.community.summary.CommunityIdentity {
		return
	}
	value := e.username
	if e.field == 1 {
		value = e.days
	}
	value, cursor := insertText(value, text, e.cursor)
	m.setPrivilegeInput(value, cursor)
}
func (m model) privilegesView() string {
	e := m.privileges
	balance := "Balance unknown"
	if e.balance.Known {
		balance = fmt.Sprintf("Balance: %d seconds (%d whole days)", e.balance.Seconds, e.balance.Seconds/86400)
	}
	if !e.balance.Fresh || !m.community.summary.Connected || e.identity != m.community.summary.CommunityIdentity || time.Since(e.balance.UpdatedAt) >= time.Minute {
		balance += " (stale)"
	}
	lines := []string{"Supporter privileges", balance, e.balance.Error}
	if e.balance.Known {
		lines = append(lines, "Observed: "+e.balance.UpdatedAt.Local().Format(time.RFC3339))
	}
	footer := "r refresh · g gift · Esc close"
	switch e.mode {
	case "edit":
		name, days := e.username, e.days
		if e.field == 0 {
			name = renderInputWindow(name, e.cursor, max(1, m.width-12))
		} else {
			days = renderInputWindow(days, e.cursor, max(1, m.width-12))
		}
		lines = append(lines, "Recipient: "+name, "Whole days: "+days, "Gift only after explicit confirmation.")
		footer = "Tab fields · Enter preview · Ctrl+R refresh · Esc close"
		if m.width < 60 || m.height < 14 {
			label, value := "Recipient", e.username
			if e.field == 1 {
				label, value = "Whole days", e.days
			}
			lines = []string{"Gift · " + label, renderInputWindow(value, e.cursor, m.width), trunc(balance, m.width)}
		}
	case "preview":
		lines = append(lines, "Confirm privilege gift", fmt.Sprintf("Recipient: %q", e.result.Username), fmt.Sprintf("Whole days: %d", e.result.Days), "Gifts have no server acknowledgement.")
		choice := "[Cancel]  Gift"
		if e.confirm {
			choice = "Cancel  [Gift]"
		}
		footer = choice + " · ←/→ · Enter · ↑/↓ scroll"
	case "result":
		lines = append(lines, "Outcome: "+e.result.State, e.result.Message)
		footer = "r refresh balance · ↑/↓ scroll · Esc close"
	}
	if e.busy {
		lines = append(lines, "Working…")
	}
	if e.identity != m.community.summary.CommunityIdentity {
		lines = append(lines, "Session changed; close and reopen")
	}
	lines = append(lines, e.err)
	lines = communityPane(lines, m.width, max(0, m.height-1), e.scroll)
	return strings.Join(append(lines, trunc(footer, m.width)), "\n")
}
