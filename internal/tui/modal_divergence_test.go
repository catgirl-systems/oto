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

// A room-retry dialog can arrive while command output is open. The dialog is
// drawn on top, so it must also receive the keys.
func TestAsyncChatDialogOverCommandOutputOwnsInput(t *testing.T) {
	m, _ := privateChatModel(t)
	m.commandOutput = &commandOutput{title: "Chat commands & aliases", text: "help"}
	m.community.chats.dialog = &chatDialog{kind: "room-retry", label: "Retry joining the room?"}
	view, overlay := m.screen()
	failIf(t, !overlay || view != m.chatDialogView(), "chat dialog not drawn over command output")
	m.key(key("esc"))
	failIf(t, m.community.chats.dialog != nil, "esc reached command output instead of the drawn dialog")
	failIf(t, m.commandOutput == nil, "command output closed behind the dialog")
}

// Two community dialogs: the one drawn must be the one answering keys.
func TestStackedCommunityDialogsDrawTheInputOwner(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.rooms.private.dialog = &privateRoomDialog{kind: "wall", label: "Clear only your wall?"}
	m.community.chats.dialog = &chatDialog{kind: "room-retry", label: "Retry joining the room?"}
	view, _ := m.screen()
	failIf(t, view != m.chatDialogView(), "private-room dialog drawn while the chat dialog owns input")
	m.key(key("esc"))
	failIf(t, m.community.chats.dialog != nil || m.community.rooms.private.dialog == nil, "esc did not close only the drawn dialog")
}

// Every modal that has a view draws it when it is the only one open, and
// screen() never shows a modal other than the one receiving input.
func TestModalTableDrawsInputOwner(t *testing.T) {
	m, _ := privateChatModel(t)
	for i := range modals {
		d := &modals[i]
		if d.view == nil {
			continue
		}
		failIf(t, d.active(&m), "modal open in the base model", d.name)
	}
	m.help = true
	m.details = true
	view, overlay := m.screen()
	failIf(t, m.activeModal().name != "help" || !overlay || view != m.helpView(), "help not both drawn and focused over details")
	m.key(key("esc"))
	failIf(t, m.help || !m.details, "esc did not close help first")
	view, _ = m.screen()
	failIf(t, view != m.detailView(), "details not drawn once help closed")
}
