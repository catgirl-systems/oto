package daemon

import (
	"context"
	"testing"
)

func TestBroadcastCommandConfirmationIsExplicitAndCanonical(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	id := s.community.identity
	if _, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: id, Username: "Alice"}); err != nil {
		t.Fatal(err)
	}
	preview, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "broadcast", RequestID: "command-preview", Args: []string{"buddies", "hello", "Alice"}})
	if err != nil || preview.Broadcast == nil || preview.Broadcast.State != "preview" {
		t.Fatal(preview, err)
	}
	req := CommandRequest{CommunityIdentity: id, Name: "broadcast-stop", RequestID: "wrong", Args: []string{preview.Broadcast.RequestID, preview.Broadcast.Token}, Confirm: true}
	if _, err := s.RunCommand(ctx, req); err == nil {
		t.Fatal("mismatched confirmation ID")
	}
	req.RequestID = preview.Broadcast.RequestID
	req.Confirm = false
	if out, err := s.RunCommand(ctx, req); err != nil || out.Broadcast.State != "preview" {
		t.Fatal("implicit stop", out, err)
	}
	if _, err := s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: id, Name: "stop-broadcast", Expansion: "broadcast-stop $*"}); err != nil {
		t.Fatal(err)
	}
	req.Name = "stop-broadcast"
	req.Confirm = true
	if _, err := s.RunCommand(ctx, req); err == nil {
		t.Fatal("mutable alias authorized confirmation")
	}
	req.Name = "broadcast-stop"
	if out, err := s.RunCommand(ctx, req); err != nil || out.Broadcast.State != "stopped" {
		t.Fatal(out, err)
	}
	req.Name = "broadcast-send"
	if out, err := s.RunCommand(ctx, req); err != nil || out.Broadcast.State != "stopped" {
		t.Fatal("stopped preview sent", out, err)
	}
	req.Name = "broadcast-show"
	req.Args = []string{preview.Broadcast.RequestID, "invalid"}
	req.Confirm = false
	if _, err := s.RunCommand(ctx, req); err == nil {
		t.Fatal("invalid cursor")
	}
}
