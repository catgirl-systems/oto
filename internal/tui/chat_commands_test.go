package tui

import (
	"io"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
)

func TestChatCommandRejectionVersusLostResponse(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		m := model{}
		id := daemon.CommunityIdentity{Account: "a", Daemon: "d", Session: 1}
		m.community.summary.CommunityIdentity = id
		key := chatKey{kind: "private", target: "Alice"}
		m.community.chats.operation = 1
		m.community.chats.busy = true
		m.community.chats.drafts = map[chatKey]chatDraft{key: {text: "/unknown", requestID: "one"}}
		var err error = io.EOF
		if rejected {
			err = &ipc.HTTPError{StatusCode: 400, Message: "unknown command"}
		}
		m.applyChatCommand(chatCommandMsg{operation: 1, identity: id, key: key, requestID: "one", err: err})
		draft := m.community.chats.drafts[key]
		failIf(t, m.community.chats.busy || draft.text != "/unknown" || (draft.requestID == "") != rejected, rejected, draft)
	}
}
func TestCommandOutputBounds(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}, {10, 3}} {
		m := model{width: size[0], height: size[1], commandOutput: &commandOutput{text: "{\"name\":\"世界\"}"}}
		view := m.commandOutputView()
		failIf(t, lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height, size, view)
	}
}
