package daemon

import (
	"context"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestBroadcastPreparedTextIsValidatedButNotTransformedAgain(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	id := s.community.identity
	s.mu.Lock()
	var err error
	s.community.text, err = compileCommunityTextTools(config.CommunityTextTools{Substitutions: []config.CommunitySubstitution{{From: "hello", To: "hello!"}}})
	s.mu.Unlock()
	must(t, err)
	req := CommunitySendRequest{CommunityIdentity: id, RequestID: "prepared-broadcast", Username: "Alice", Text: "hello!"}
	out, err := s.sendCommunityPrivate(ctx, req, true)
	if err != nil {
		t.Fatal(out, err)
	}
	if _, err := s.SendCommunityPrivate(ctx, req); err == nil {
		t.Fatal("prepared and ordinary submissions share a fingerprint")
	}
	message, err := s.stateDB.Queries().GetCommunityMessage(ctx, db.GetCommunityMessageParams{Account: id.Account, ID: out.MessageID})
	failIf(t, err != nil || message.Body != "hello!", message, err)
	req.RequestID = "prepared-invalid"
	req.Text = "\x1b[31munsafe"
	if _, err := s.sendCommunityPrivate(ctx, req, true); err == nil {
		t.Fatal("prepared text bypassed validation")
	}
	req.Text = communityCTCPVersionRequest
	if _, err := s.SendCommunityPrivate(ctx, req); err == nil {
		t.Fatal("public caller bypassed CTCP validation")
	}
}
