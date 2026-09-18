package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

type textToolsEditor struct {
	value       daemon.CommunityTextSettings
	group, row  int
	busy, dirty bool
	err, dialog string
	confirm     bool
	form        *textToolForm
	cancel      context.CancelFunc
}
type textToolForm struct {
	values        [2]string
	field, cursor int
	add           bool
}
type textToolsMsg struct {
	owner *textToolsEditor
	value daemon.CommunityTextSettings
	err   error
}

var textToolGroups = []string{"Keywords", "Substitutions", "Censorship", "CTCP VERSION"}

func (m *model) openTextTools() tea.Cmd {
	if m.client == nil {
		return nil
	}
	m.closeTextTools()
	e := &textToolsEditor{value: daemon.CommunityTextSettings{CommunityIdentity: m.community.summary.CommunityIdentity}}
	m.textTools = e
	return m.textToolsRequest(false)
}
func (m *model) closeTextTools() {
	if e := m.textTools; e != nil && e.cancel != nil {
		e.cancel()
	}
	m.textTools = nil
}
func (m *model) textToolsRequest(save bool) tea.Cmd {
	e := m.textTools
	if e == nil || e.busy || m.client == nil {
		return nil
	}
	e.busy = true
	e.err = ""
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	e.cancel = cancel
	req, client := e.value, m.client
	return func() tea.Msg {
		defer cancel()
		var out daemon.CommunityTextSettings
		var err error
		if save {
			out, err = client.SetCommunityTextSettings(ctx, req)
		} else {
			out, err = client.CommunityTextSettings(ctx, req.CommunityIdentity)
		}
		return textToolsMsg{owner: e, value: out, err: err}
	}
}
func (m *model) applyTextTools(x textToolsMsg) tea.Cmd {
	e := m.textTools
	if e == nil || e != x.owner {
		return nil
	}
	e.busy = false
	e.cancel = nil
	if e.value.CommunityIdentity != m.community.summary.CommunityIdentity {
		e.err = "Session changed; reload settings"
		return nil
	}
	if x.err != nil {
		e.err = x.err.Error()
		return nil
	}
	if x.value.CommunityIdentity != e.value.CommunityIdentity {
		e.err = "Session changed; reload settings"
		return nil
	}
	e.value = x.value
	e.dirty = false
	e.row = min(e.row, max(0, len(e.items())-1))
	return nil
}
func (e *textToolsEditor) items() []string {
	s := e.value.Settings
	switch e.group {
	case 0:
		return s.Keywords
	case 1:
		out := make([]string, len(s.Substitutions))
		for i, r := range s.Substitutions {
			out[i] = r.From + " → " + r.To
		}
		return out
	case 2:
		return s.Censorship
	default:
		return []string{fmt.Sprintf("Automatic replies: %t (off by default)", s.CTCPVersion)}
	}
}
func (e *textToolsEditor) setFormText(value string, cursor int) {
	if len(value) > 1024 || !utf8.ValidString(value) || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) >= 0 {
		e.err = "Single-line UTF-8 within 1024 bytes; nothing inserted"
		return
	}
	e.form.values[e.form.field] = value
	e.form.cursor = cursor
	e.err = ""
}
func (m *model) pasteTextTools(text string) {
	e := m.textTools
	if e == nil || e.busy || e.form == nil || e.dialog != "" || e.value.CommunityIdentity != m.community.summary.CommunityIdentity {
		return
	}
	v, c := insertText(e.form.values[e.form.field], text, e.form.cursor)
	e.setFormText(v, c)
}
func (m *model) textToolsKey(k tea.KeyPressMsg) tea.Cmd {
	e := m.textTools
	key := k.String()
	if e.dialog != "" {
		switch key {
		case "esc":
			e.dialog = ""
		case "left", "right", "tab", "shift+tab":
			e.confirm = !e.confirm
		case "enter":
			action, yes := e.dialog, e.confirm
			e.dialog = ""
			if !yes {
				return nil
			}
			switch action {
			case "save":
				return m.textToolsRequest(true)
			case "close":
				m.closeTextTools()
			case "reload":
				return m.openTextTools()
			case "remove":
				i := e.row
				s := &e.value.Settings
				switch e.group {
				case 0:
					s.Keywords = append(s.Keywords[:i], s.Keywords[i+1:]...)
				case 1:
					s.Substitutions = append(s.Substitutions[:i], s.Substitutions[i+1:]...)
				case 2:
					s.Censorship = append(s.Censorship[:i], s.Censorship[i+1:]...)
				}
				e.dirty = true
				e.row = max(0, e.row-1)
			}
		}
		return nil
	}
	if key == "esc" {
		if e.form != nil {
			e.form = nil
			e.err = ""
			return nil
		}
		if e.dirty {
			e.dialog = "close"
			e.confirm = false
		} else {
			m.closeTextTools()
		}
		return nil
	}
	if e.busy {
		return nil
	}
	if key == "r" && e.form == nil {
		if e.dirty {
			e.dialog = "reload"
			e.confirm = false
			return nil
		}
		return m.openTextTools()
	}
	if e.value.CommunityIdentity != m.community.summary.CommunityIdentity {
		e.err = "Session changed; r reloads"
		return nil
	}
	if e.form != nil {
		f := e.form
		switch key {
		case "esc":
			e.form = nil
			e.err = ""
		case "tab", "shift+tab":
			if e.group == 1 {
				f.field = 1 - f.field
				f.cursor = utf8.RuneCountInString(f.values[f.field])
			}
		case "enter":
			if f.values[0] == "" || e.group != 1 && strings.TrimSpace(f.values[0]) == "" {
				e.err = "A nonempty keyword, source or pattern is required"
				return nil
			}
			if e.group == 2 && strings.IndexFunc(f.values[0], unicode.IsSpace) >= 0 {
				e.err = "Censorship patterns cannot contain spaces"
				return nil
			}
			s := &e.value.Settings
			i := e.row
			switch e.group {
			case 0:
				if f.add {
					s.Keywords = append(s.Keywords, f.values[0])
				} else {
					s.Keywords[i] = f.values[0]
				}
			case 1:
				v := daemon.CommunitySubstitution{From: f.values[0], To: f.values[1]}
				if f.add {
					s.Substitutions = append(s.Substitutions, v)
				} else {
					s.Substitutions[i] = v
				}
			case 2:
				if f.add {
					s.Censorship = append(s.Censorship, f.values[0])
				} else {
					s.Censorship[i] = f.values[0]
				}
			}
			e.dirty = true
			e.form = nil
			e.err = ""
		default:
			v, c, changed := editText(f.values[f.field], f.cursor, k)
			if changed {
				e.setFormText(v, c)
			}
		}
		return nil
	}
	items := e.items()
	switch key {
	case "tab", "shift+tab":
		delta := 1
		if key == "shift+tab" {
			delta = -1
		}
		e.group = (e.group + delta + 4) % 4
		e.row = 0
		e.err = ""
	case "up", "k":
		e.row = max(0, e.row-1)
	case "down", "j":
		e.row = min(max(0, len(items)-1), e.row+1)
	case "ctrl+up", "ctrl+down":
		if e.group == 1 {
			delta := 1
			if key == "ctrl+up" {
				delta = -1
			}
			next := e.row + delta
			rules := e.value.Settings.Substitutions
			if next >= 0 && next < len(rules) {
				rules[e.row], rules[next] = rules[next], rules[e.row]
				e.row = next
				e.dirty = true
			}
		}
	case "s":
		if e.dirty {
			e.dialog = "save"
			e.confirm = false
		}
	case "d", "delete":
		if e.group < 3 && len(items) > 0 {
			e.dialog = "remove"
			e.confirm = false
		}
	case "n", "a", "enter":
		if e.group == 3 {
			if key == "enter" {
				e.value.Settings.CTCPVersion = !e.value.Settings.CTCPVersion
				e.dirty = true
			}
			return nil
		}
		add := key != "enter" || len(items) == 0
		if add && len(items) >= 32 {
			e.err = "At most 32 rules per group"
			return nil
		}
		f := &textToolForm{add: add}
		if !add {
			if e.group == 1 {
				r := e.value.Settings.Substitutions[e.row]
				f.values = [2]string{r.From, r.To}
			} else {
				f.values[0] = items[e.row]
			}
		}
		f.cursor = utf8.RuneCountInString(f.values[0])
		e.form = f
	}
	return nil
}
func (m model) textToolsView() string {
	if m.width < 20 || m.height < 6 {
		return trunc("Text tools: enlarge; Esc closes", max(1, m.width))
	}
	e := m.textTools
	title := "Chat text tools · " + textToolGroups[e.group]
	if e.dirty {
		title += " · unsaved"
	}
	footer := "s save · r reload · Ctrl↑↓ reorder · Esc close"
	if e.dialog != "" {
		footer = "←→ select · Enter accept · Esc cancel"
	} else if e.form != nil {
		footer = "Enter keeps draft · Tab field · Esc cancels edit"
	}
	return m.cardView(title, footer, func(width, rows int) []string {
		lines := make([]string, 0, rows)
		switch {
		case e.dialog != "":
			label := map[string]string{"save": "Save rules for future messages only?", "close": "Discard unsaved changes and close?", "reload": "Discard changes and reload?", "remove": "Remove selected rule from this draft?"}[e.dialog]
			choices := "[Cancel]  Confirm"
			if e.confirm {
				choices = "Cancel  [Confirm]"
			}
			lines = append(lines, label, choices)
		case e.form != nil:
			f := e.form
			fields := 1
			if e.group == 1 {
				fields = 2
			}
			for i := 0; i < fields; i++ {
				label := "Value"
				if e.group == 1 {
					label = []string{"From", "To"}[i]
				}
				cursor := "  "
				value := f.values[i]
				if i == f.field {
					cursor = "> "
					value = renderInputWindow(value, f.cursor, max(1, width-len(label)-4))
				}
				lines = append(lines, cursor+label+": "+value)
			}
		default:
			help := []string{"", "Ordered literal replacements; after normalization.", "Whole tokens; * any, ? one; case-insensitive; → ***.", "One reply at a time; 10s globally, 60s per sender."}[e.group]
			if help != "" {
				lines = append(lines, help, "")
			}
			items := e.items()
			if len(items) == 0 {
				lines = append(lines, "No rules. Press a to add.")
			}
			window := max(1, rows-len(lines)-2)
			start := max(0, e.row-window+1)
			for i := start; i < min(len(items), start+window); i++ {
				mark := "  "
				if i == e.row {
					mark = "> "
				}
				lines = append(lines, mark+items[i])
			}
			lines = append(lines, "", "Tab group · a/n add · Enter edit · d remove")
		}
		if e.busy {
			lines = append(lines, "Working…")
		}
		if e.value.CommunityIdentity != m.community.summary.CommunityIdentity {
			lines = append(lines, "Session changed; r reloads")
		}
		if e.err != "" {
			lines = append(lines, e.err)
		}
		return lines
	})
}
