package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/charmbracelet/x/ansi"
)

type shareExclusionsView struct {
	open, editing bool
	cursor, caret int
	editIndex     int
	value, err    string
	saved         []string
}

func (m *model) openShareExclusions() {
	v := &m.shareExclusions
	if v.saved == nil {
		v.saved = append([]string{}, m.cfg.ShareExclusions...)
	}
	v.open = true
	v.cursor = min(v.cursor, max(0, len(m.cfg.ShareExclusions)-1))
}

func (m *model) shareExclusionsKey(k tea.KeyPressMsg) tea.Cmd {
	v := &m.shareExclusions
	if v.editing {
		switch k.String() {
		case "esc":
			v.editing, v.err = false, ""
		case "enter":
			rules := append([]string{}, m.cfg.ShareExclusions...)
			if v.editIndex == len(rules) {
				rules = append(rules, v.value)
			} else {
				rules[v.editIndex] = v.value
			}
			normalized, err := config.NormalizeShareExclusions(rules)
			if err != nil {
				v.err = err.Error()
				return nil
			}
			m.cfg.ShareExclusions = normalized
			v.cursor, v.editing, v.err = v.editIndex, false, ""
		default:
			v.value, v.caret, _ = editText(v.value, v.caret, k)
		}
		return nil
	}
	if k.String() == "esc" {
		v.open = false
		return nil
	}
	if m.settingsSaving {
		return nil
	}
	last := max(0, len(m.cfg.ShareExclusions)-1)
	switch k.String() {
	case "up", "k":
		v.cursor = max(0, v.cursor-1)
	case "down", "j":
		v.cursor = min(last, v.cursor+1)
	case "pgup":
		v.cursor = max(0, v.cursor-m.pageRows())
	case "pgdown":
		v.cursor = min(last, v.cursor+m.pageRows())
	case "home":
		v.cursor = 0
	case "end":
		v.cursor = last
	case "a", "enter":
		v.editIndex, v.value = len(m.cfg.ShareExclusions), ""
		if k.String() == "enter" {
			if len(m.cfg.ShareExclusions) == 0 {
				return nil
			}
			v.editIndex, v.value = v.cursor, m.cfg.ShareExclusions[v.cursor]
		}
		v.editing, v.caret, v.err = true, len([]rune(v.value)), ""
	case "d", "delete":
		if len(m.cfg.ShareExclusions) > 0 {
			rules := append([]string{}, m.cfg.ShareExclusions...)
			m.cfg.ShareExclusions = slices.Delete(rules, v.cursor, v.cursor+1)
			v.cursor = min(v.cursor, max(0, len(m.cfg.ShareExclusions)-1))
		}
	case "R":
		m.restoreShareExclusions = true
		m.uploadConfirm, m.uploadConfirmChoice = true, 0
		m.uploadConfirmLabel = "Replace all staged rules with defaults (save with s)"
	case "s":
		return m.saveSettings()
	}
	return nil
}

func shareExclusionDescription(rule string) string {
	rule = strings.ReplaceAll(rule, `\`, "/")
	if rule == "" {
		return "Enter a pattern to see what it excludes."
	}
	if rule == "*" {
		return "All otherwise shareable files"
	}
	if strings.HasSuffix(rule, "/") || strings.HasSuffix(rule, "/*") {
		base, folder := strings.CutSuffix(rule, "/*")
		if !folder {
			base = strings.TrimSuffix(rule, "/")
		}
		return fmt.Sprintf("Folders matching %q and their contents", base)
	}
	if strings.HasPrefix(rule, "*") && strings.Count(rule, "*") == 1 {
		return fmt.Sprintf("File paths ending in %q", rule[1:])
	}
	if !strings.ContainsAny(rule, "*/") {
		return fmt.Sprintf("Files named %q", rule)
	}
	return fmt.Sprintf("File paths matching %q", rule)
}

func (m model) renderShareExclusions(width, height int) string {
	width, height = max(1, width), max(1, height)
	v := m.shareExclusions
	status := "Applied"
	if !slices.Equal(v.saved, m.cfg.ShareExclusions) {
		status = "Unsaved changes"
	}
	if m.settingsSaving {
		status = "Saving settings…"
		if scan := m.status.shareScan; scan != nil && (scan.State == "scanning" || scan.State == "publishing" || scan.State == "cancelling") {
			status = "Share scan: " + scan.State
		}
	}
	lines := []string{accent("EXCLUDED CONTENT"), muted(fmt.Sprintf("All shares · %d rules · %s", len(m.cfg.ShareExclusions), status))}
	if v.editing {
		title := "Edit pattern"
		if v.editIndex == len(m.cfg.ShareExclusions) {
			title = "Add pattern"
		}
		input := renderInput("", v.value, v.caret, false, lipgloss.NewStyle())
		start := max(0, ansi.StringWidth(string([]rune(v.value)[:v.caret]))-width+2)
		lines = append(lines, "", strong(title), ansi.Cut(input, start, start+width))
		if v.err != "" {
			lines = append(lines, danger(v.err))
		}
		lines = append(lines, "", shareExclusionDescription(v.value), "",
			"Examples: *.partial   @eaDir/   Thumbs.db",
			"Only * is a wildcard, including across /. Matching ignores case.",
			"Use virtual paths, not local absolute paths. A trailing / or /* excludes folder contents.",
			"Explicit share roots stay shared. Local files are never deleted.",
			"Enter stages the rule; s in the list saves all Settings.")
	} else {
		if v.err != "" {
			lines = append(lines, danger(v.err))
		}
		lines = append(lines, "")
		patternWidth := max(12, min(36, width/3))
		heading := "Pattern"
		if width >= 64 {
			heading = fmt.Sprintf("%-*s  Matches", patternWidth, heading)
		}
		lines = append(lines, strong("  "+heading))
		footer := []string{"", muted("* wildcard · case-insensitive · folder/ includes contents"), muted("Files stay on disk. Hidden entries and descendant symlinks stay omitted."), muted("s saves all Settings; failed/cancelled scans keep the old rules and index.")}
		rows := max(1, height-len(lines)-len(footer))
		start, end := visibleRange(len(m.cfg.ShareExclusions), v.cursor, rows)
		for i := start; i < end; i++ {
			rule := m.cfg.ShareExclusions[i]
			line := rule
			if width >= 64 {
				pattern := ansi.Truncate(rule, patternWidth, "…")
				line = pattern + strings.Repeat(" ", patternWidth-ansi.StringWidth(pattern)+2) + shareExclusionDescription(rule)
			}
			lines = append(lines, selectedRow(ansi.Truncate(line, max(1, width-2), "…"), i == v.cursor))
		}
		if len(m.cfg.ShareExclusions) == 0 {
			lines = append(lines, muted("No configurable exclusions. Press a to add a rule."))
		}
		lines = append(lines, footer...)
	}
	// Keep the list navigable even when there is only room for one rule.
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}
