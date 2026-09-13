package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCTCPReplyConsentReplayAndRate(t *testing.T) {
	s := downloadService(t)
	_, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	req := soulseek.PrivateMessage{ID: 1, Username: "Alice", Text: communityCTCPVersionRequest, New: true}
	if err := s.receiveCommunityPrivate(ctx, id, req); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if s.community.ctcp != nil {
		t.Fatal("default-on CTCP")
	}
	s.community.text, _ = compileCommunityTextTools(CommunityTextTools{CTCPVersion: true})
	s.mu.Unlock()
	req.ID++
	if err := s.receiveCommunityPrivate(ctx, id, req); err != nil {
		t.Fatal(err)
	}
	command, payload, err := soulseek.ReadFrame(peer)
	if err != nil {
		t.Fatal(err)
	}
	d := soulseek.NewDecoder(payload)
	username, err := d.String()
	if err != nil {
		t.Fatal(err)
	}
	body, err := d.String()
	if err != nil || d.Done() != nil || command != soulseek.ServerPrivateMessage || username != "Alice" || !strings.HasPrefix(body, "VERSION: oto") {
		t.Fatal(command, username, body, err)
	}
	s.wg.Wait()
	s.mu.Lock()
	s.community.ctcp.last = time.Time{}
	clear(s.community.ctcp.users)
	s.mu.Unlock()
	if err := s.receiveCommunityPrivate(ctx, id, req); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.community.ctcp.users) != 0 {
		t.Fatal("replay reserved response")
	}
	for _, m := range []soulseek.PrivateMessage{{Username: "Alice", Text: communityCTCPVersionRequest, New: false}, {Username: "server", Text: communityCTCPVersionRequest, New: true}, {Username: "Alice", Text: "VERSION: other", New: true}, {Username: "Alice", Text: "\x01VERSION other\x01", New: true}} {
		if s.allowCTCPReplyLocked(id, m) {
			t.Fatal("response loop or offline reply")
		}
	}
	id.Session++
	if s.allowCTCPReplyLocked(id, req) {
		t.Fatal("stale reply")
	}
}
func TestCTCPRateBudget(t *testing.T) {
	c := communityCTCPState{}
	now := time.Now()
	if !c.reserve("Alice", now) || c.reserve("Bob", now.Add(time.Minute)) {
		t.Fatal("busy limit")
	}
	c.busy = false
	if c.reserve("Bob", now.Add(time.Second)) || c.reserve("Alice", now.Add(11*time.Second)) {
		t.Fatal("rate limit")
	}
	if !c.reserve("Bob", now.Add(11*time.Second)) {
		t.Fatal("independent user")
	}
	c.busy = false
	if !c.reserve("Alice", now.Add(time.Minute)) {
		t.Fatal("expiration")
	}
}
func TestCTCPExplicitQueryIsDurableAndSanitized(t *testing.T) {
	s := downloadService(t)
	id := s.community.identity
	ctx := context.Background()
	req := CommandRequest{CommunityIdentity: id, Name: "ctcp", Args: []string{"Alice"}, RequestID: "version-query"}
	result, err := s.RunCommand(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := s.RunCommand(ctx, req)
	if err != nil || !repeat.Send.Duplicate || repeat.Send.MessageID != result.Send.MessageID {
		t.Fatal(repeat, err)
	}
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: id, ConversationID: result.Send.ConversationID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Text != "[CTCP] VERSION request" {
		t.Fatal(page)
	}
	if _, err := s.SendCommunityPrivate(ctx, CommunitySendRequest{CommunityIdentity: id, Username: "Alice", Text: communityCTCPVersionRequest, RequestID: "raw"}); err == nil {
		t.Fatal("ordinary text bypassed validation")
	}
}
