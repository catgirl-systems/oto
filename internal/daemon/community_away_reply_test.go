package daemon

import (
	"context"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestAwayReplyOncePerPeriodAndDurableTranscript(t *testing.T) {
	s := downloadService(t)
	_, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	s.mu.Lock()
	s.ctx = ctx
	s.runCtx = ctx
	s.presence = PresenceAway
	s.cfg.CommunityAway = map[string]config.CommunityAway{id.Account: {AutoReply: "back later"}}
	s.mu.Unlock()
	message := soulseek.PrivateMessage{ID: 1, Username: "Alice", Text: "hello", New: true}
	receive := func() {
		t.Helper()
		must(t, s.receiveCommunityPrivate(ctx, id, message))
	}
	reply := func() {
		t.Helper()
		code, body, err := soulseek.ReadFrame(peer)
		must(t, err)
		d := soulseek.NewDecoder(body)
		user := d.String()
		text := d.String()
		failIf(t, code != soulseek.ServerPrivateMessage || d.Done() != nil || user != "Alice" || text != "[Automatic Message] back later", code, user, text, d.Done())
		s.wg.Wait()
	}
	receive()
	reply()
	message.ID++
	receive()
	s.wg.Wait()
	list, err := s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: id})
	must(t, err)
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: id, ConversationID: list.Conversations[0].ID})
	must(t, err)
	outgoing := 0
	for _, m := range page.Messages {
		if m.Direction == "outgoing" {
			outgoing++
			failIf(t, m.State != "sent", m)
		}
	}
	failIf(t, outgoing != 1, "repeat autoreply", page)
	setPresence := func(p Presence) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- s.SetPresence(p) }()
		if _, _, err := soulseek.ReadFrame(peer); err != nil {
			t.Fatal(err)
		}
		must(t, <-done)
	}
	setPresence(PresenceOnline)
	setPresence(PresenceAway)
	message.ID++
	receive()
	reply()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range []soulseek.PrivateMessage{{Username: "server", Text: "notice", New: true}, {Username: "Alice", Text: "hello", New: false}, {Username: "Alice", Text: "[Automatic Message] away", New: true}, {Username: "Alice", Text: communityCTCPVersionRequest, New: true}, {Username: "Alice", Text: "VERSION: peer", New: true}} {
		failIf(t, s.allowAwayReplyLocked(id, m), "automatic response loop")
	}
	rule, err := NormalizeCommunityRule(CommunityRule{Action: "ignore", Kind: "ip", Value: "192.0.2.0/24"})
	must(t, err)
	s.community.rules = []CommunityRule{rule}
	if s.allowAwayReplyLocked(id, soulseek.PrivateMessage{Username: "Unresolved", Text: "hello", New: true}) {
		t.Fatal("unresolved address allowed reply")
	}
}
