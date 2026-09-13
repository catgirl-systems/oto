package tui

import (
	"reflect"
	"testing"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestCompletionQuotingCyclingAndFencing(t *testing.T) {
	m := model{}
	id := daemon.CommunityIdentity{Account: "a", Daemon: "d", Session: 1}
	m.community.summary.CommunityIdentity = id
	m.community.chats.conversation = daemon.CommunityConversation{Kind: "private", Target: "Alice"}
	text := `/msg "Al" hi`
	cursor := 8
	kind, prefix, _, start, end, ok := m.completionContext(text, cursor)
	if !ok || kind != "user" || prefix != "Al" {
		t.Fatal(kind, prefix, start, end)
	}
	m.setChatDraft(text, cursor)
	c := &m.community.chats
	c.completion = chatCompletion{identity: id, key: m.chatKey(), text: text, cursor: cursor, start: start, end: end, kind: kind, candidates: []string{"Alice Smith", "Alfred"}}
	m.applyCompletionCandidate("Alice Smith")
	if got := c.drafts[m.chatKey()].text; got != `/msg "Alice Smith" hi` {
		t.Fatal(got)
	}
	m.completeChat(false)
	if got := c.drafts[m.chatKey()].text; got != "/msg Alfred hi" {
		t.Fatal(got)
	}
	m.completeChat(true)
	if got := c.drafts[m.chatKey()].text; got != `/msg "Alice Smith" hi` {
		t.Fatal(got)
	}
	for _, name := range []string{`A'B`, `A\B`, `A"B`, "世界 名"} {
		_, args, err := daemon.ParseCommand("msg " + quoteCompletion(name))
		if err != nil || !reflect.DeepEqual(args, []string{name}) {
			t.Fatal(name, args, err)
		}
	}
	m.setChatDraft("/he", 3)
	c.completion = chatCompletion{identity: id, key: m.chatKey(), text: "/he", cursor: 3, start: 0, end: 3, kind: "command"}
	msg := chatCompletionMsg{request: c.completionRequest, navigation: c.navigation, identity: id, key: m.chatKey(), text: "/he", cursor: 3, kind: "command", candidates: []string{"help"}}
	m.setChatDraft("/hi", 3)
	m.applyChatCompletion(msg)
	if c.drafts[m.chatKey()].text != "/hi" {
		t.Fatal("late completion overwrote edit")
	}
	m.setChatDraft("/he", 3)
	c.completion = chatCompletion{identity: id, key: m.chatKey(), text: "/he", cursor: 3, start: 0, end: 3, kind: "command"}
	msg.request = c.completionRequest
	m.applyChatCompletion(msg)
	d := c.drafts[m.chatKey()]
	if d.text != "/help" || d.cursor != utf8.RuneCountInString(d.text) {
		t.Fatal(d)
	}
}
