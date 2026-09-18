package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

type receivingEditor struct {
	loaded               daemon.ReceivingSettings
	values               [4]string
	field, cursor        int
	busy, dirty, confirm bool
	dialog, err          string
	cancel               context.CancelFunc
}
type receivingSettingsMsg struct {
	owner *receivingEditor
	value daemon.ReceivingSettings
	err   error
}

func (m *model) openReceivingSettings() tea.Cmd {
	if m.client == nil || !m.community.supports("receiving") {
		m.setNotice("Receiving settings unavailable; refresh Community or restart daemon")
		return nil
	}
	m.closeReceivingSettings()
	m.receivingEditor = &receivingEditor{loaded: daemon.ReceivingSettings{CommunityIdentity: m.community.summary.CommunityIdentity}}
	return m.receivingSettingsRequest(false)
}
func (m *model) closeReceivingSettings() {
	if e := m.receivingEditor; e != nil && e.cancel != nil {
		e.cancel()
	}
	m.receivingEditor = nil
}
func (e *receivingEditor) settings() (config.Receiving, error) {
	p := config.Receiving{Mode: e.values[0], Directory: e.values[2], CompletionHooks: e.values[3] == "on"}
	if err := json.Unmarshal([]byte(e.values[1]), &p.Users); err != nil {
		return p, fmt.Errorf("Users must be a JSON array, e.g. [\"Alice\",\"Bob\"]")
	}
	return p, p.Validate()
}
func (m *model) receivingSettingsRequest(save bool) tea.Cmd {
	e := m.receivingEditor
	if e == nil || e.busy || m.client == nil {
		return nil
	}
	if e.loaded.CommunityIdentity != m.community.summary.CommunityIdentity {
		e.err = "Session changed; close and reopen"
		return nil
	}
	req := daemon.ReceivingSettingsRequest{CommunityIdentity: e.loaded.CommunityIdentity, Expected: e.loaded.Settings, Confirm: save}
	if save {
		var err error
		req.Settings, err = e.settings()
		if err != nil {
			e.err = err.Error()
			return nil
		}
	}
	e.busy = true
	e.err = ""
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	e.cancel = cancel
	client := m.client
	return func() tea.Msg {
		defer cancel()
		var out daemon.ReceivingSettings
		var err error
		if save {
			out, err = client.SetReceivingSettings(ctx, req)
		} else {
			out, err = client.ReceivingSettings(ctx, req.CommunityIdentity)
		}
		return receivingSettingsMsg{e, out, err}
	}
}
func (m *model) applyReceivingSettings(x receivingSettingsMsg) tea.Cmd {
	e := m.receivingEditor
	if e == nil || e != x.owner {
		return nil
	}
	e.busy = false
	e.cancel = nil
	if e.loaded.CommunityIdentity != m.community.summary.CommunityIdentity {
		e.err = "Session changed; close and reopen"
		return nil
	}
	if x.err != nil {
		e.err = x.err.Error()
		return nil
	}
	if x.value.CommunityIdentity != e.loaded.CommunityIdentity {
		e.err = "Stale response ignored"
		return nil
	}
	e.loaded = x.value
	mode := x.value.Settings.Mode
	if mode == "" {
		mode = "off"
	}
	users := x.value.Settings.Users
	if users == nil {
		users = []string{}
	}
	text, _ := json.Marshal(users)
	hooks := "off"
	if x.value.Settings.CompletionHooks {
		hooks = "on"
	}
	e.values = [4]string{mode, string(text), x.value.Settings.Directory, hooks}
	e.cursor = utf8.RuneCountInString(e.values[e.field])
	e.dirty = false
	return nil
}
func (e *receivingEditor) setText(text string, cursor int) {
	limit := 4096
	if e.field == 1 {
		limit = 128 << 10
	}
	if len(text) > limit || !utf8.ValidString(text) || strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) {
		e.err = "Invalid or oversized single-line text; nothing inserted"
		return
	}
	e.dirty = e.dirty || text != e.values[e.field]
	e.values[e.field] = text
	e.cursor = cursor
	e.err = ""
}
func (m *model) pasteReceivingSettings(text string) {
	e := m.receivingEditor
	if e == nil || e.busy || e.dialog != "" || e.loaded.CommunityIdentity != m.community.summary.CommunityIdentity || (e.field != 1 && e.field != 2) {
		return
	}
	value, cursor := insertText(e.values[e.field], text, e.cursor)
	e.setText(value, cursor)
}
func (m *model) receivingSettingsKey(k tea.KeyPressMsg) tea.Cmd {
	e := m.receivingEditor
	key := k.String()
	if e.dialog != "" {
		switch key {
		case "esc":
			e.dialog = ""
		case "tab", "shift+tab", "left", "right":
			e.confirm = !e.confirm
		case "enter":
			action, yes := e.dialog, e.confirm
			e.dialog = ""
			if yes {
				if action == "save" {
					return m.receivingSettingsRequest(true)
				}
				m.closeReceivingSettings()
			}
		}
		return nil
	}
	if key == "esc" {
		if e.busy || e.dirty {
			e.dialog = "close"
			e.confirm = false
		} else {
			m.closeReceivingSettings()
		}
		return nil
	}
	if e.busy {
		return nil
	}
	if e.loaded.CommunityIdentity != m.community.summary.CommunityIdentity {
		e.err = "Session changed; close and reopen"
		return nil
	}
	switch key {
	case "tab", "shift+tab":
		delta := 1
		if key == "shift+tab" {
			delta = 3
		}
		e.field = (e.field + delta) % 4
		e.cursor = utf8.RuneCountInString(e.values[e.field])
	case "enter", "ctrl+s":
		if e.dirty {
			if _, err := e.settings(); err != nil {
				e.err = err.Error()
				return nil
			}
			e.dialog = "save"
			e.confirm = false
		}
	default:
		if e.field == 0 || e.field == 3 {
			if key == "left" || key == "right" || key == "space" {
				values := []string{"off", "users", "buddies", "trusted"}
				if e.field == 3 {
					values = []string{"off", "on"}
				}
				index := slices.Index(values, e.values[e.field])
				delta := 1
				if key == "left" {
					delta = len(values) - 1
				}
				e.setText(values[(max(0, index)+delta)%len(values)], 0)
			}
		} else {
			value, cursor, changed := editText(e.values[e.field], e.cursor, k)
			if changed {
				e.setText(value, cursor)
			}
		}
	}
	return nil
}
func (m model) receivingSettingsView() string {
	if m.width < 20 || m.height < 6 {
		return trunc("Receiving: enlarge; Esc", max(1, m.width))
	}
	e := m.receivingEditor
	lines := []string{"Received files settings"}
	if e.dirty {
		lines[0] += " · unsaved"
	}
	if e.dialog != "" {
		label := "Discard local edits? A pending save may already be committed."
		if e.dialog == "save" {
			p, _ := e.settings()
			label = fmt.Sprintf("Mode: %s · %d users", p.Mode, len(p.Users))
		}
		choice := "[Cancel]  Confirm"
		if e.confirm {
			choice = "Cancel  [Confirm]"
		}
		lines = append(lines, label)
		if e.dialog == "save" {
			lines = append(lines, "Command hooks: "+e.values[3])
		}
		lines = append(lines, choice, "←→ choose · Enter accept · Esc cancel")
	} else {
		labels := []string{"Mode", "Users (JSON)", "Directory", "Hooks"}
		for i, label := range labels {
			prefix := "  "
			value := e.values[i]
			if i == e.field {
				prefix = "> "
				if i == 1 || i == 2 {
					value = renderInputWindow(value, e.cursor, max(1, m.width-len(label)-4))
				}
			}
			lines = append(lines, prefix+label+": "+value)
		}
		lines = append(lines, "Empty directory: "+e.loaded.EffectiveDirectory, "Users: [\"Alice\",\"Bob\"] · exact names, at most 256", "Off by default; no everyone mode. Bans still apply.", "Hooks can run configured commands. Received files never auto-open.", "Tab field · ←→ mode/hooks · Enter review · Esc close")
	}
	if e.busy {
		lines = append(lines, "Working…")
	}
	if e.err != "" {
		lines = append(lines, e.err)
	}
	if len(lines) > m.height {
		lines = append(lines[:m.height-1], lines[len(lines)-1])
	}
	for i := range lines {
		lines[i] = trunc(lines[i], m.width)
	}
	return strings.Join(lines, "\n")
}
