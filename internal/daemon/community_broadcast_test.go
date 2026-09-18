package daemon

import (
	"context"
	"fmt"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestBroadcastPreviewCapturesPagedAudienceWithoutSending(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	id := s.community.identity
	s.mu.Lock()
	s.community.online = true
	for i := range 240 {
		name := fmt.Sprintf("buddy-%03d", i)
		s.community.buddies[name] = CommunityBuddy{}
		s.community.users[name] = CommunityUser{Username: name, StatusFresh: true, Status: soulseek.UserStatusOnline}
	}
	s.community.buddies["Offline"] = CommunityBuddy{}
	s.community.buddies["Stale"] = CommunityBuddy{}
	s.community.users["Stale"] = CommunityUser{Status: soulseek.UserStatusOnline}
	var err error
	s.community.text, err = compileCommunityTextTools(config.CommunityTextTools{Substitutions: []config.CommunitySubstitution{{From: "hello", To: "greetings"}}})
	s.mu.Unlock()
	must(t, err)
	req := CommunityBroadcastRequest{CommunityIdentity: id, RequestID: "broadcast-preview", Audience: "buddies", Text: "hello", Offline: []string{"Offline", "Offline"}}
	out, err := s.PreviewCommunityBroadcast(ctx, req)
	failIf(t, err != nil || out.Total != 241 || len(out.Recipients) != 200 || out.NextCursor != 200 || out.Text != "greetings" || out.Recipients[0].Username != "Offline" || out.Token == "", out, err)
	second, err := s.CommunityBroadcast(ctx, id, req.RequestID, out.NextCursor)
	failIf(t, err != nil || len(second.Recipients) != 41 || second.NextCursor != 0, second, err)
	s.mu.Lock()
	delete(s.community.buddies, "buddy-239")
	s.mu.Unlock()
	again, err := s.PreviewCommunityBroadcast(ctx, req)
	failIf(t, err != nil || again.Total != 241 || again.Token != out.Token, "snapshot changed", again, err)
	req.Text = "different"
	if _, err := s.PreviewCommunityBroadcast(ctx, req); err == nil {
		t.Fatal("request ID reused")
	}
	conversations, err := s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: id})
	failIf(t, err != nil || len(conversations.Conversations) != 0, "preview enqueued messages", conversations, err)
	s.mu.Lock()
	s.community.identity.Session++
	current := s.community.identity
	s.mu.Unlock()
	if _, err := s.CommunityBroadcast(ctx, id, req.RequestID, 0); err == nil {
		t.Fatal("stale caller")
	}
	recovered, err := s.CommunityBroadcast(ctx, current, req.RequestID, 0)
	failIf(t, err != nil || recovered.State != "stale-preview" || recovered.Total != 241, recovered, err)
}
func TestBroadcastUploadAudienceIsActiveExactAndDeduplicated(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	id := s.community.identity
	s.mu.Lock()
	s.community.online = true
	s.uploadEpoch = 10
	for _, v := range []struct {
		id, user, state string
		epoch           uint64
	}{{"a", "Alice", "running", 10}, {"b", "Alice", "running", 10}, {"c", "alice", "running", 10}, {"d", "Waiting", "queued", 10}, {"e", "Old", "running", 9}} {
		s.transfers[v.id] = Transfer{ID: v.id, Username: v.user, Direction: "upload", State: v.state}
		s.uploadOwners[v.id] = uploadOwner{session: v.epoch}
	}
	s.mu.Unlock()
	req := CommunityBroadcastRequest{CommunityIdentity: id, RequestID: "upload-audience", Audience: "uploaders", Text: "hello"}
	out, err := s.PreviewCommunityBroadcast(ctx, req)
	failIf(t, err != nil || out.Total != 2 || out.Recipients[0].Username != "Alice" || out.Recipients[1].Username != "alice", out, err)
	req.RequestID = "bad-audience"
	req.Offline = []string{"Alice"}
	if _, err := s.PreviewCommunityBroadcast(ctx, req); err == nil {
		t.Fatal("offline uploaders accepted")
	}
	req.Audience = "buddies"
	if _, err := s.PreviewCommunityBroadcast(ctx, req); err == nil {
		t.Fatal("non-buddy added")
	}
	req.Offline = nil
	if _, err := s.PreviewCommunityBroadcast(ctx, req); err == nil {
		t.Fatal("empty audience accepted")
	}
}
