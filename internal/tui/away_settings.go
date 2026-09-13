package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

type awayEditor struct {
	loaded               daemon.CommunityAwaySettings
	form                 textToolForm
	busy, dirty, confirm bool
	dialog, err          string
	cancel               context.CancelFunc
}
type awaySettingsMsg struct {
	owner *awayEditor
	value daemon.CommunityAwaySettings
	err   error
}

func (m *model) openAwaySettings() tea.Cmd {
	if m.client == nil {
		return nil
	}
	m.closeAwaySettings()
	m.awayEditor = &awayEditor{loaded: daemon.CommunityAwaySettings{CommunityIdentity: m.community.summary.CommunityIdentity}}
	return m.awaySettingsRequest(false)
}
func (m *model) closeAwaySettings() {
	if e := m.awayEditor; e != nil && e.cancel != nil {
		e.cancel()
	}
	m.awayEditor = nil
}
func (m *model) awaySettingsRequest(save bool) tea.Cmd {
	e := m.awayEditor
	if e == nil || e.busy || m.client == nil {
		return nil
	}
	req := daemon.CommunityAwaySettingsRequest{CommunityIdentity: e.loaded.CommunityIdentity, Expected: e.loaded.Settings, Settings: e.loaded.Settings}
	if save {
		seconds, err := strconv.Atoi(e.form.values[0])
		if err != nil || seconds < 0 || seconds > 86400 {
			e.err = "Idle seconds must be 0 (off) through 86400"
			return nil
		}
		req.Settings.AutoAwaySeconds = seconds
		req.Settings.AutoReply = e.form.values[1]
	}
	e.busy = true
	e.err = ""
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	e.cancel = cancel
	client := m.client
	return func() tea.Msg {
		defer cancel()
		var out daemon.CommunityAwaySettings
		var err error
		if save {
			out, err = client.SetCommunityAwaySettings(ctx, req)
		} else {
			out, err = client.CommunityAwaySettings(ctx, req.CommunityIdentity)
		}
		return awaySettingsMsg{owner: e, value: out, err: err}
	}
}
func (m *model) applyAwaySettings(x awaySettingsMsg) tea.Cmd {
	e := m.awayEditor
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
		e.err = "Session changed; close and reopen"
		return nil
	}
	e.loaded = x.value
	e.form.values = [2]string{strconv.Itoa(x.value.Settings.AutoAwaySeconds), strings.ReplaceAll(strings.ReplaceAll(x.value.Settings.AutoReply, "\r\n", " "), "\n", " ")}
	e.form.cursor = utf8.RuneCountInString(e.form.values[e.form.field])
	e.dirty = false
	return nil
}
func (e *awayEditor) setText(text string, cursor int) {
	if len(text) > 1024 || !utf8.ValidString(text) || strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) >= 0 {
		e.err = "Single-line UTF-8 within 1024 bytes; nothing inserted"
		return
	}
	if text != e.form.values[e.form.field] {
		e.dirty = true
	}
	e.form.values[e.form.field] = text
	e.form.cursor = cursor
	e.err = ""
}
func (m *model) pasteAwaySettings(text string) {
	e := m.awayEditor
	if e == nil || e.busy || e.dialog != "" || e.loaded.CommunityIdentity != m.community.summary.CommunityIdentity {
		return
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", " "), "\n", " ")
	value, cursor := insertText(e.form.values[e.form.field], text, e.form.cursor)
	e.setText(value, cursor)
}
func (m *model) awaySettingsKey(k tea.KeyPressMsg) tea.Cmd {
	e := m.awayEditor
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
					return m.awaySettingsRequest(true)
				}
				m.closeAwaySettings()
			}
		}
		return nil
	}
	if key == "esc" {
		if e.dirty || e.busy {
			e.dialog = "close"
			e.confirm = false
		} else {
			m.closeAwaySettings()
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
		e.form.field = 1 - e.form.field
		e.form.cursor = utf8.RuneCountInString(e.form.values[e.form.field])
	case "enter", "ctrl+s":
		if e.dirty {
			e.dialog = "save"
			e.confirm = false
		}
	default:
		value, cursor, changed := editText(e.form.values[e.form.field], e.form.cursor, k)
		if changed {
			e.setText(value, cursor)
		}
	}
	return nil
}
func (m model) awaySettingsView() string {
	if m.width < 20 || m.height < 6 {
		return trunc("Away settings: enlarge; Esc", max(1, m.width))
	}
	e := m.awayEditor
	lines := []string{"Away settings"}
	if e.dirty {
		lines[0] += " · unsaved"
	}
	if e.dialog != "" {
		label := "Close and discard local edits? An in-flight save may already be committed."
		if e.dialog == "save" {
			label = "Save idle timeout and automatic reply for this account?"
		}
		choice := "[Cancel]  Confirm"
		if e.confirm {
			choice = "Cancel  [Confirm]"
		}
		lines = append(lines, label, choice, "←→ choose · Enter accept · Esc cancel")
	} else {
		labels := []string{"Idle seconds (0 off)", "Reply (empty off)"}
		if m.width < 40 {
			labels = []string{"Seconds (0 off)", "Reply"}
		}
		for i, label := range labels {
			prefix := "  "
			value := e.form.values[i]
			if i == e.form.field {
				prefix = "> "
				value = renderInputWindow(value, e.form.cursor, max(1, m.width-len(label)-4))
			}
			lines = append(lines, prefix+label+": "+value)
		}
		lines = append(lines, "Manual Away stays until explicit Online.", "Replies once per sender per away period; no offline/CTCP replies.", fmt.Sprintf("Automatic Away now: %t", m.community.summary.AutomaticAway), "Tab field · Enter save preview · Esc close")
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
