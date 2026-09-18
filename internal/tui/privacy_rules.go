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

const privacyRulesPageSize = 200

type privacyRulesEditor struct {
	identity           daemon.CommunityIdentity
	rules              []daemon.CommunityRule
	cursor, nextCursor string
	back               []string
	row                int
	revision           uint64
	request            uint64
	loading, busy      bool
	err                string
	form               *privacyRuleForm
	dialog             *privacyRuleDialog
}

type privacyRuleForm struct {
	rule, old daemon.CommunityRule
	field     int
	cursor    int
	dirty     bool
	err       string
}

type privacyRuleDialog struct {
	kind     string
	label    string
	rule     daemon.CommunityRule
	identity daemon.CommunityIdentity
	revision uint64
	confirm  bool
	scroll   int
}

type privacyRulesPageMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	cursor   string
	page     daemon.CommunityRulesPage
	err      error
}

type privacyRulesActionMsg struct {
	request  uint64
	identity daemon.CommunityIdentity
	page     daemon.CommunityRulesPage
	err      error
}

func (m *model) openPrivacyRules(action, selected string) tea.Cmd {
	if m.client == nil || !m.community.supports("privacy-rules") {
		m.setNotice("Privacy rules unavailable; refresh Community or restart the daemon")
		return nil
	}
	e := &privacyRulesEditor{identity: m.community.summary.CommunityIdentity}
	m.privacyRules = e
	if action == "ignore" || action == "ban" {
		e.form = &privacyRuleForm{rule: daemon.CommunityRule{Action: action, Kind: "username", Value: selected}, cursor: len([]rune(selected))}
	}
	return m.loadPrivacyRulesPage("")
}

func (m *model) closePrivacyRules() {
	if m.privacyRules != nil {
		m.privacyRules.request++
	}
	m.privacyRules = nil
}

func (m *model) loadPrivacyRulesPage(cursor string) tea.Cmd {
	e := m.privacyRules
	if e == nil || m.client == nil {
		return nil
	}
	m.privacyRulesRequest++
	e.request = m.privacyRulesRequest
	e.loading, e.err = true, ""
	request, identity, client, base := e.request, e.identity, m.client, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(base, 5*time.Second)
		defer cancel()
		page, err := client.CommunityRules(ctx, daemon.CommunityRulesRequest{CommunityIdentity: identity, Cursor: cursor, Limit: privacyRulesPageSize})
		return privacyRulesPageMsg{request: request, identity: identity, cursor: cursor, page: page, err: err}
	}
}

func (m *model) applyPrivacyRulesPage(x privacyRulesPageMsg) tea.Cmd {
	e := m.privacyRules
	if e == nil || x.request != e.request || x.identity != e.identity || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	e.loading = false
	e.err = errText(x.err)
	if x.err != nil {
		return nil
	}
	if x.page.CommunityIdentity != x.identity {
		e.err = daemon.ErrCommunitySession.Error()
		return nil
	}
	if x.cursor != e.cursor {
		if e.nextCursor != "" && x.cursor == e.nextCursor {
			e.back = append(e.back, e.cursor)
		} else if len(e.back) > 0 && x.cursor == e.back[len(e.back)-1] {
			e.back = e.back[:len(e.back)-1]
		}
	}
	e.rules, e.revision, e.nextCursor = x.page.Rules, x.page.Revision, x.page.NextCursor
	e.cursor = x.cursor
	e.row = max(0, min(e.row, len(e.rules)-1))
	return nil
}

func (m *model) privacyRulesAction(d *privacyRuleDialog) tea.Cmd {
	e := m.privacyRules
	if e == nil || m.client == nil {
		return nil
	}
	if d.identity != e.identity || e.identity != m.community.summary.CommunityIdentity {
		e.err = "Account/session changed; close and reopen privacy rules"
		return nil
	}
	m.privacyRulesRequest++
	e.request = m.privacyRulesRequest
	e.busy, e.err = true, ""
	request, identity, client, base := e.request, e.identity, m.client, m.ctx
	req := daemon.CommunityRuleRequest{CommunityIdentity: identity, Rule: d.rule, Revision: d.revision, Remove: d.kind == "delete", Confirm: true}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(base, 5*time.Second)
		defer cancel()
		page, err := client.SetCommunityRule(ctx, req)
		return privacyRulesActionMsg{request: request, identity: identity, page: page, err: err}
	}
}

func (m *model) applyPrivacyRulesAction(x privacyRulesActionMsg) tea.Cmd {
	e := m.privacyRules
	if e == nil || x.request != e.request || x.identity != e.identity || x.identity != m.community.summary.CommunityIdentity {
		return nil
	}
	e.busy = false
	e.err = errText(x.err)
	if x.err != nil {
		return nil
	}
	if x.page.CommunityIdentity != x.identity {
		e.err = daemon.ErrCommunitySession.Error()
		return nil
	}
	e.revision = x.page.Revision
	e.form, e.dialog = nil, nil
	m.setNotice("Privacy rule saved")
	return m.loadPrivacyRulesPage(e.cursor)
}

func (m *model) privacyRulesKey(k tea.KeyPressMsg) tea.Cmd {
	e := m.privacyRules
	if e == nil {
		return nil
	}
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
	if e.busy {
		return nil
	}
	if e.loading && k.String() != "esc" {
		return nil
	}
	if e.dialog != nil {
		return m.privacyRulesDialogKey(k)
	}
	if e.form != nil {
		return m.privacyRulesFormKey(k)
	}
	s := k.String()
	switch s {
	case "esc", "q":
		m.closePrivacyRules()
	case "r":
		return m.loadPrivacyRulesPage(e.cursor)
	case "n", "pgdown":
		if e.nextCursor != "" {
			return m.loadPrivacyRulesPage(e.nextCursor)
		}
	case "p", "pgup":
		if len(e.back) > 0 {
			return m.loadPrivacyRulesPage(e.back[len(e.back)-1])
		}
	case "up", "k", "down", "j", "home", "end":
		navKey(s, &e.row, max(0, len(e.rules)-1), m.pageRows())
	case "a":
		e.form = &privacyRuleForm{rule: daemon.CommunityRule{Action: "ignore", Kind: "username"}}
	case "e", "enter":
		if e.row >= 0 && e.row < len(e.rules) {
			rule := e.rules[e.row]
			e.form = &privacyRuleForm{rule: rule, old: rule, cursor: len([]rune(rule.Value))}
		}
	case "d", "delete":
		if e.row >= 0 && e.row < len(e.rules) {
			rule := e.rules[e.row]
			e.dialog = &privacyRuleDialog{kind: "delete", label: fmt.Sprintf("Delete %s %s %q?", rule.Action, rule.Kind, rule.Value), rule: rule, identity: e.identity, revision: e.revision}
		}
	}
	return nil
}

func (m *model) privacyRulesFormKey(k tea.KeyPressMsg) tea.Cmd {
	e, f := m.privacyRules, m.privacyRules.form
	if e == nil || f == nil || e.busy {
		return nil
	}
	s := k.String()
	if s == "esc" {
		if f.dirty {
			e.dialog = &privacyRuleDialog{kind: "discard", label: "Discard this unsaved privacy rule draft?", identity: e.identity}
		} else {
			e.form = nil
		}
		return nil
	}
	if s == "tab" || s == "shift+tab" {
		delta := 1
		if s == "shift+tab" {
			delta = 3
		}
		f.field = (f.field + delta) % 4
		f.cursor = privacyRuleFieldCursor(*f)
		return nil
	}
	if f.field < 2 && (s == "left" || s == "right" || s == "space" || s == " ") {
		privacyRuleCycle(f, s == "left")
		return nil
	}
	if s == "enter" {
		if f.field < 3 {
			f.field++
			f.cursor = privacyRuleFieldCursor(*f)
			return nil
		}
		normalized, err := daemon.NormalizeCommunityRule(f.rule)
		if err != nil {
			f.err = err.Error()
			return nil
		}
		f.rule = normalized
		e.dialog = &privacyRuleDialog{kind: "save", label: privacyRuleLabel(f.rule, f.old), rule: f.rule, identity: e.identity, revision: e.revision}
		return nil
	}
	if f.field >= 2 {
		value := privacyRuleFieldValue(f)
		before := value
		value, cursor, changed := editText(value, f.cursor, k)
		if changed {
			if len(value) > 1024 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
				f.err = "Value cannot exceed 1024 bytes or contain terminal controls"
				return nil
			}
			privacyRuleSetField(f, value, cursor)
			f.dirty, f.err = f.dirty || value != before, ""
		}
	}
	return nil
}

func privacyRuleFieldCursor(f privacyRuleForm) int {
	if f.field == 2 {
		return len([]rune(f.rule.Value))
	}
	if f.field == 3 {
		return len([]rune(f.rule.Message))
	}
	return 0
}

func privacyRuleFieldValue(f *privacyRuleForm) string {
	if f.field == 2 {
		return f.rule.Value
	}
	return f.rule.Message
}

func privacyRuleSetField(f *privacyRuleForm, value string, cursor int) {
	if f.field == 2 {
		f.rule.Value, f.cursor = value, cursor
	} else {
		f.rule.Message, f.cursor = value, cursor
	}
}

func privacyRuleCycle(f *privacyRuleForm, reverse bool) {
	if f.field == 0 {
		if f.rule.Action == "ignore" {
			f.rule.Action = "ban"
		} else {
			f.rule.Action = "ignore"
		}
		if f.rule.Action == "ignore" {
			f.rule.Message = ""
		}
		if f.rule.Kind == "country" {
			f.rule.Kind = "username"
		}
	} else {
		kinds := []string{"username", "ip", "country"}
		for i, kind := range kinds {
			if kind == f.rule.Kind {
				if reverse {
					i = (i + len(kinds) - 1) % len(kinds)
				} else {
					i = (i + 1) % len(kinds)
				}
				f.rule.Kind = kinds[i]
				if f.rule.Kind == "country" {
					f.rule.Action = "ban"
				}
				break
			}
		}
	}
	f.dirty = true
	f.err = ""
}

func (m *model) privacyRulesDialogKey(k tea.KeyPressMsg) tea.Cmd {
	e, d := m.privacyRules, m.privacyRules.dialog
	if e == nil || d == nil || e.busy {
		return nil
	}
	switch k.String() {
	case "esc", "n":
		e.dialog = nil
	case "pgdown":
		d.scroll += 3
	case "pgup":
		d.scroll = max(0, d.scroll-3)
	case "left", "right", "tab", "shift+tab", "up", "down", "j", "k":
		d.confirm = !d.confirm
	case "y":
		d.confirm = true
	case "enter":
		if !d.confirm {
			e.dialog = nil
			return nil
		}
		e.dialog = nil
		if d.kind == "discard" {
			e.form = nil
			return nil
		}
		return m.privacyRulesAction(d)
	}
	return nil
}

func (m *model) pastePrivacyRules(text string) {
	e := m.privacyRules
	if e == nil || e.form == nil || e.dialog != nil || e.busy || e.loading || e.form.field < 2 {
		return
	}
	f := e.form
	value := privacyRuleFieldValue(f)
	candidate, cursor := insertText(value, text, f.cursor)
	if len(candidate) > 1024 || !utf8.ValidString(candidate) || strings.IndexFunc(candidate, unicode.IsControl) >= 0 {
		f.err = "Invalid or oversized paste; nothing pasted"
		return
	}
	privacyRuleSetField(f, candidate, cursor)
	f.dirty, f.err = true, ""
}

func privacyRuleLabel(rule, old daemon.CommunityRule) string {
	verb := "Add"
	if old.ID != 0 {
		verb = "Save"
	}
	label := fmt.Sprintf("%s %s %s %q", verb, rule.Action, rule.Kind, rule.Value)
	if rule.Message != "" {
		label += fmt.Sprintf(" with message %q", rule.Message)
	}
	return label + "?"
}

func (m model) privacyRulesView() string {
	e := m.privacyRules
	if e == nil {
		return m.mainView()
	}
	short := m.width < 48
	title := fmt.Sprintf("Privacy rules · page %d", len(e.back)+1)
	footer := "a add · e edit · d delete · Esc close"
	if short {
		footer = "a+ · e · d · Esc"
	}
	switch {
	case e.dialog != nil:
		title = "Confirm privacy change"
		footer = "←→ choose · Enter · Esc · PgUp/Dn text"
		if short {
			footer = "←→ · Enter · Esc"
		}
	case e.form != nil:
		title = "Privacy rule editor"
		footer = "Tab fields · Enter next/preview · Esc cancel"
		if short {
			footer = "Tab · Enter · Esc"
		}
	}
	return m.cardView(title, footer, func(width, rows int) []string {
		var lines []string
		switch {
		case e.dialog != nil:
			d := e.dialog
			choices := "[Cancel] Confirm"
			if d.confirm {
				choices = "Cancel [Confirm]"
			}
			lines = append(lines, communityPane([]string{d.label}, width, max(0, rows-1), d.scroll)...)
			lines = append(lines, choices)
		case e.form != nil:
			f := e.form
			values := []string{f.rule.Action, f.rule.Kind, f.rule.Value, f.rule.Message}
			labels := []string{"Action", "Kind", "Value", "Ban message"}
			if width < 36 {
				labels[3] = "Message"
			}
			start, end := 0, 4
			if rows < 8 {
				start, end = f.field, f.field+1
			}
			for i := start; i < end; i++ {
				value := values[i]
				if i >= 2 && i == f.field {
					value = renderInputWindow(value, f.cursor, max(1, width-len(labels[i])-4))
				}
				if i < 2 && i == f.field {
					value = "‹ " + value + " ›"
				}
				lines = append(lines, selectedRow(trunc(labels[i]+": "+value, max(1, width-2)), i == f.field))
			}
			if rows >= 8 {
				lines = append(lines, "", "Ignore: chat only. Ban: sharing/uploads.")
			}
			message := f.err
			if e.err != "" {
				message = e.err
			}
			if e.identity != m.community.summary.CommunityIdentity {
				message = "Session changed; Esc to close/reopen"
			}
			if e.busy {
				message = "Saving…"
			} else if e.loading {
				message = "Loading…"
			}
			if message != "" {
				lines = append(lines, danger("! "+message))
			}
		default:
			if rows >= 7 {
				lines = append(lines, "Ignore: chat only. Ban: sharing/uploads.")
			}
			message := e.err
			if !m.community.summary.Connected && message == "" {
				message = "Offline; rules are saved locally"
			}
			if e.loading {
				message = "Loading…"
			}
			if e.identity != m.community.summary.CommunityIdentity {
				message = "Session changed; Esc to close/reopen"
			}
			if message != "" {
				lines = append(lines, "! "+message)
			}
			if rows >= 7 {
				lines = append(lines, "")
			}
			available := max(1, rows-len(lines)-1)
			start, end := visibleRange(len(e.rules), e.row, available)
			for i := start; i < end; i++ {
				rule := e.rules[i]
				lines = append(lines, selectedRow(trunc(fmt.Sprintf("%s %s %q", rule.Action, rule.Kind, rule.Value), max(1, width-2)), i == e.row))
			}
			if len(e.rules) == 0 && !e.loading {
				lines = append(lines, "No privacy rules.")
			}
			paging := "p previous"
			if e.nextCursor != "" {
				paging += " · n next"
			}
			lines = append(lines, paging+" · r reload")
		}
		return lines
	})
}
