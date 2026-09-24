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
	must(t, s.receiveCommunityPrivate(ctx, id, req))
	s.mu.Lock()
	failIf(t, s.community.ctcp != nil, "default-on CTCP")
	s.community.text, _ = compileCommunityTextTools(CommunityTextTools{CTCPVersion: true})
	s.mu.Unlock()
	req.ID++
	must(t, s.receiveCommunityPrivate(ctx, id, req))
	command, payload, err := soulseek.ReadFrame(peer)
	must(t, err)
	d := soulseek.NewDecoder(payload)
	username := d.String()
	body := d.String()
	failIf(t, d.Done() != nil || command != soulseek.ServerPrivateMessage || username != "Alice" || !strings.HasPrefix(body, "VERSION: oto"), command, username, body, d.Done())
	s.wg.Wait()
	s.mu.Lock()
	s.community.ctcp.last = time.Time{}
	clear(s.community.ctcp.users)
	s.mu.Unlock()
	must(t, s.receiveCommunityPrivate(ctx, id, req))
	s.mu.Lock()
	defer s.mu.Unlock()
	failIf(t, len(s.community.ctcp.users) != 0, "replay reserved response")
	for _, m := range []soulseek.PrivateMessage{{Username: "Alice", Text: communityCTCPVersionRequest, New: false}, {Username: "server", Text: communityCTCPVersionRequest, New: true}, {Username: "Alice", Text: "VERSION: other", New: true}, {Username: "Alice", Text: "\x01VERSION other\x01", New: true}} {
		failIf(t, s.allowCTCPReplyLocked(id, m), "response loop or offline reply")
	}
	id.Session++
	failIf(t, s.allowCTCPReplyLocked(id, req), "stale reply")
}
func TestCTCPRateBudget(t *testing.T) {
	c := communityCTCPState{}
	now := time.Now()
	failIf(t, !c.reserve("Alice", now) || c.reserve("Bob", now.Add(time.Minute)), "busy limit")
	c.busy = false
	failIf(t, c.reserve("Bob", now.Add(time.Second)) || c.reserve("Alice", now.Add(11*time.Second)), "rate limit")
	failIf(t, !c.reserve("Bob", now.Add(11*time.Second)), "independent user")
	c.busy = false
	failIf(t, !c.reserve("Alice", now.Add(time.Minute)), "expiration")
}
func TestCTCPExplicitQueryIsDurableAndSanitized(t *testing.T) {
	s := downloadService(t)
	id := s.community.identity
	ctx := context.Background()
	req := CommandRequest{CommunityIdentity: id, Name: "ctcp", Args: []string{"Alice"}, RequestID: "version-query"}
	result, err := s.RunCommand(ctx, req)
	must(t, err)
	repeat, err := s.RunCommand(ctx, req)
	failIf(t, err != nil || !repeat.Send.Duplicate || repeat.Send.MessageID != result.Send.MessageID, repeat, err)
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: id, ConversationID: result.Send.ConversationID})
	must(t, err)
	failIf(t, len(page.Messages) != 1 || page.Messages[0].Text != "[CTCP] VERSION request", page)
	if _, err := s.SendCommunityPrivate(ctx, CommunitySendRequest{CommunityIdentity: id, Username: "Alice", Text: communityCTCPVersionRequest, RequestID: "raw"}); err == nil {
		t.Fatal("ordinary text bypassed validation")
	}
}
