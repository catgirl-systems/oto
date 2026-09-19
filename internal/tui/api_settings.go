package tui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

// apiEditor reviews pending pairing requests and manages connected apps.
type apiEditor struct {
	mode          string // requests, apps
	requests      []daemon.APIAuthRequest
	apps          []daemon.APIApp
	cursor        int
	busy          bool
	err           string
	revoke        string // app ID awaiting confirm
	confirm       bool
	approve       string // request ID awaiting expiry choice
	approveDays   string
	approveCursor int
	approveNever  bool
	cancel        context.CancelFunc
}

type apiListMsg struct {
	owner    *apiEditor
	requests []daemon.APIAuthRequest
	apps     []daemon.APIApp
	err      error
}

type apiActionMsg struct {
	owner *apiEditor
	err   error
}

func (m *model) openAPIEditor(mode string) tea.Cmd {
	if m.client == nil {
		return nil
	}
	m.closeAPIEditor()
	m.apiEditor = &apiEditor{mode: mode}
	return m.apiListRequest()
}

func (m *model) closeAPIEditor() {
	if e := m.apiEditor; e != nil && e.cancel != nil {
		e.cancel()
	}
	m.apiEditor = nil
}

func (m *model) apiListRequest() tea.Cmd {
	e := m.apiEditor
	if e == nil || e.busy || m.client == nil {
		return nil
	}
	e.busy, e.err = true, ""
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	e.cancel = cancel
	client := m.client
	return func() tea.Msg {
		defer cancel()
		requests, err := client.ListAPIAuthRequests(ctx)
		var apps []daemon.APIApp
		if err == nil {
			apps, err = client.ListAPIApps(ctx)
		}
		return apiListMsg{owner: e, requests: requests, apps: apps, err: err}
	}
}

func (m *model) apiActionRequest(id string, approve bool, days int) tea.Cmd {
	e := m.apiEditor
	if e == nil || e.busy || m.client == nil {
		return nil
	}
	e.busy, e.err = true, ""
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	e.cancel = cancel
	client := m.client
	return func() tea.Msg {
		defer cancel()
		var err error
		if e.mode == "apps" {
			err = client.RevokeAPIApp(ctx, id)
		} else if approve {
			err = client.ApproveAPIAuthRequest(ctx, id, days)
		} else {
			err = client.RejectAPIAuthRequest(ctx, id)
		}
		return apiActionMsg{owner: e, err: err}
	}
}

func (m *model) applyAPIList(x apiListMsg) tea.Cmd {
	e := m.apiEditor
	if e == nil || e != x.owner {
		return nil
	}
	e.busy, e.cancel = false, nil
	if x.err != nil {
		e.err = x.err.Error()
		return nil
	}
	e.requests, e.apps = x.requests, x.apps
	if e.cursor >= e.entries() {
		e.cursor = max(0, e.entries()-1)
	}
	return nil
}

func (m *model) applyAPIAction(x apiActionMsg) tea.Cmd {
	e := m.apiEditor
	if e == nil || e != x.owner {
		return nil
	}
	e.busy, e.cancel = false, nil
	if x.err != nil {
		e.err = x.err.Error()
	}
	return m.apiListRequest()
}

func (e *apiEditor) entries() int {
	if e.mode == "apps" {
		return len(e.apps)
	}
	return len(e.requests)
}

func (m *model) apiEditorKey(k tea.KeyPressMsg) tea.Cmd {
	e := m.apiEditor
	key := k.String()
	if e.approve != "" {
		switch key {
		case "esc":
			e.approve, e.err = "", ""
		case "tab", "shift+tab":
			e.approveNever = !e.approveNever
		case "enter":
			days := 0
			if !e.approveNever {
				parsed, err := strconv.Atoi(e.approveDays)
				if err != nil || parsed < 1 || parsed > 3650 {
					e.err = "Enter days 1–3650, or Tab for never"
					return nil
				}
				days = parsed
			}
			id := e.approve
			e.approve, e.err = "", ""
			return m.apiActionRequest(id, true, days)
		default:
			e.approveDays, e.approveCursor, _ = editText(e.approveDays, e.approveCursor, k)
			e.err = ""
		}
		return nil
	}
	if e.revoke != "" {
		switch key {
		case "esc":
			e.revoke = ""
		case "tab", "shift+tab", "left", "right":
			e.confirm = !e.confirm
		case "enter":
			id, yes := e.revoke, e.confirm
			e.revoke, e.confirm = "", false
			if yes {
				return m.apiActionRequest(id, false, 0)
			}
		}
		return nil
	}
	switch key {
	case "esc":
		m.closeAPIEditor()
	case "tab", "shift+tab":
		if e.mode == "requests" {
			e.mode = "apps"
		} else {
			e.mode = "requests"
		}
		e.cursor = 0
		return m.apiListRequest()
	case "up", "k":
		if e.cursor > 0 {
			e.cursor--
		}
	case "down", "j":
		if e.cursor < e.entries()-1 {
			e.cursor++
		}
	case "a":
		if e.mode == "requests" && !e.busy && e.cursor < len(e.requests) {
			e.approve = e.requests[e.cursor].ID
			e.approveDays, e.approveCursor, e.approveNever, e.err = "30", len("30"), false, ""
		}
	case "r":
		if e.busy {
			return nil
		}
		if e.mode == "apps" && e.cursor < len(e.apps) {
			e.revoke, e.confirm = e.apps[e.cursor].ID, false
		} else if e.mode == "requests" && e.cursor < len(e.requests) {
			return m.apiActionRequest(e.requests[e.cursor].ID, false, 0)
		}
	case "enter":
		return m.apiListRequest()
	}
	return nil
}

func (m model) apiEditorView() string {
	if m.width < 20 || m.height < 6 {
		return trunc("API connections: enlarge; Esc", max(1, m.width))
	}
	e := m.apiEditor
	title, footer := "API connection requests", "a approve · r reject · Tab apps · Enter refresh · Esc close"
	if e.mode == "apps" {
		title, footer = "Connected apps", "r revoke · Tab requests · Enter refresh · Esc close"
	}
	if e.revoke != "" {
		footer = "choose: arrows, Enter accept, Esc cancel"
	}
	if e.approve != "" {
		footer = "Tab never, Enter approve, Esc cancel"
	}
	return m.cardView(title, footer, func(width, rows int) []string {
		lines := make([]string, 0, rows)
		if e.approve != "" {
			name := ""
			for _, request := range e.requests {
				if request.ID == e.approve {
					name = request.Name
				}
			}
			days := renderInputWindow(e.approveDays, e.approveCursor, max(1, width-30)) + " days"
			if e.approveNever {
				days = "never"
			}
			never := "[ ] Never expires"
			if e.approveNever {
				never = "[x] Never expires"
			}
			return append(lines, "Approve "+name+"? Its token expires after:", "  "+days, "  "+never, "", muted("Tab toggles never; the token can also be revoked later."))
		}
		if e.revoke != "" {
			name := ""
			for _, app := range e.apps {
				if app.ID == e.revoke {
					name = app.Name
				}
			}
			choice := "[Cancel]  Confirm"
			if e.confirm {
				choice = "Cancel  [Confirm]"
			}
			return append(lines, "Revoke "+name+"'s access? Its token stops working immediately.", choice)
		}
		if e.mode == "requests" {
			if len(e.requests) == 0 {
				lines = append(lines, muted("No pending connection requests."))
			}
			for i, request := range e.requests {
				row := fmt.Sprintf("%s from %s · %s · %s ago", request.Name, request.SourceIP, request.UserAgent, time.Since(request.CreatedAt).Round(time.Second))
				lines = append(lines, selectedRow(trunc(row, max(4, width-2)), i == e.cursor))
			}
			if len(lines) > 0 {
				lines = append(lines, "", muted(trunc("The source IP may be hidden or spoofed by proxies.", width)))
			}
		} else {
			if len(e.apps) == 0 {
				lines = append(lines, muted("No connected apps."))
			}
			for i, app := range e.apps {
				used := "never used"
				if app.LastUsedAt != nil {
					used = "used " + time.Since(*app.LastUsedAt).Round(time.Second).String() + " ago"
				}
				expiry := "never expires"
				if app.ExpiresAt != nil {
					if remaining := time.Until(*app.ExpiresAt); remaining > 0 {
						expiry = fmt.Sprintf("expires in %.0fd", remaining.Hours()/24)
					} else {
						expiry = "expired"
					}
				}
				row := fmt.Sprintf("%s · %s · %s · %s · added %s ago", app.Name, app.UserAgent, used, expiry, time.Since(app.CreatedAt).Round(time.Second))
				lines = append(lines, selectedRow(trunc(row, max(4, width-2)), i == e.cursor))
			}
		}
		if e.busy {
			lines = append(lines, "Working…")
		}
		if e.err != "" {
			lines = append(lines, e.err)
		}
		return lines
	})
}
