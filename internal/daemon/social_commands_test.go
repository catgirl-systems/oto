package daemon

import (
	"context"
	"testing"

	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestSocialCommandsReuseDurableSending(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	id := s.community.identity
	req := CommandRequest{CommunityIdentity: id, Name: "message", Args: []string{"Alice Smith", "hello 世界"}, RequestID: "command-message"}
	out, err := s.RunCommand(ctx, req)
	if err != nil || out.Send == nil || out.Send.State != "queued" {
		t.Fatal(out, err)
	}
	again, err := s.RunCommand(ctx, req)
	if err != nil || again.Send == nil || !again.Send.Duplicate || again.Send.MessageID != out.Send.MessageID {
		t.Fatal(again, err)
	}
	req = CommandRequest{CommunityIdentity: id, Name: "me", Username: "Alice Smith", Args: []string{"waves", "hello"}, RequestID: "command-action"}
	out, err = s.RunCommand(ctx, req)
	if err != nil || out.Send == nil {
		t.Fatal(out, err)
	}
	message, err := s.stateDB.Queries().GetCommunityMessage(ctx, db.GetCommunityMessageParams{Account: id.Account, ID: out.Send.MessageID})
	if err != nil || message.Body != "/me waves hello" {
		t.Fatal(message, err)
	}
	req.Room = "lounge"
	if _, err := s.RunCommand(ctx, req); err == nil {
		t.Fatal("ambiguous action target accepted")
	}
	for _, req := range []CommandRequest{
		{Name: "message", Args: []string{"Alice"}}, {Name: "me", Args: []string{"waves"}},
		{Name: "search", Args: []string{"rooms", "lounge"}}, {Name: "say", Args: []string{"lounge", "hello"}, RequestID: "room-offline"},
		{Name: "not-a-command", Args: []string{"hello"}},
	} {
		req.CommunityIdentity = id
		if _, err := s.RunCommand(ctx, req); err == nil {
			t.Fatal("invalid command accepted", req.Name)
		}
	}
}
