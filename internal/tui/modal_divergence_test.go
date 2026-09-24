package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestModalPasteGatedByConfirmDialog(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openAwaySettings())
	e := m.awayEditor
	failIf(t, e == nil, "away editor missing")
	e.form.values[e.form.field] = ""
	m.confirm = true
	m.Update(tea.PasteMsg{Content: "pasted"})
	failIf(t, e.form.values[e.form.field] == "pasted", "paste reached modal under confirm dialog")
	m.confirm = false
	m.Update(tea.PasteMsg{Content: "60"})
	failIf(t, e.form.values[e.form.field] != "60", "paste ignored without confirm dialog")
}

func TestAPIEditorPasteFeedsApproveDays(t *testing.T) {
	m, _ := privateChatModel(t)
	m.client = nil // openAPIEditor requires a client; build the editor directly.
	m.apiEditor = &apiEditor{mode: "requests", approve: "req-1", approveDays: "3", approveCursor: 1}
	m.Update(tea.PasteMsg{Content: "0"})
	failIf(t, m.apiEditor.approveDays != "30", "paste missed approve prompt", m.apiEditor.approveDays)
	m.apiEditor.approve = ""
	m.Update(tea.PasteMsg{Content: "9"})
	failIf(t, m.apiEditor.approveDays != "30", "paste outside approve prompt")
}

func TestDraftCountWarnsOnBusyAwayEditor(t *testing.T) {
	m, _ := privateChatModel(t)
	failIf(t, m.chatDraftCount() != 0, "baseline drafts", m.chatDraftCount())
	m.awayEditor = &awayEditor{busy: true}
	failIf(t, m.chatDraftCount() != 1, "busy away editor not counted")
	m.awayEditor = &awayEditor{}
	failIf(t, m.chatDraftCount() != 0, "idle clean away editor counted")
}
