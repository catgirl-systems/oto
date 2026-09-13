package tui

import (
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestChatActionsAreVisuallyDistinctWithoutColor(t *testing.T) {
	m := model{}
	m.community.chats.messages = []daemon.CommunityMessage{{ID: 1, Sender: "Alice", Text: "/me waves 世界", Direction: "incoming"}}
	var text strings.Builder
	for _, line := range m.chatHistoryLines(80) {
		text.WriteString(line.text)
		text.WriteByte('\n')
	}
	if !strings.Contains(text.String(), "[action]") || !strings.Contains(text.String(), "* Alice waves 世界") {
		t.Fatal(text.String())
	}
}
