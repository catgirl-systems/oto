package daemon

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestBroadcastPacingStopAndConfirmationDeduplication(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.mu.Lock()
	s.ctx = ctx
	s.community.online = true
	for _, name := range []string{"Alice", "Bob", "Charlie"} {
		s.community.buddies[name] = CommunityBuddy{}
		s.community.users[name] = CommunityUser{StatusFresh: true, Status: soulseek.UserStatusOnline}
	}
	wake := s.community.wake
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				if s.syncCommunityOutbox(ctx, client, id) != nil {
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	preview, err := s.PreviewCommunityBroadcast(ctx, CommunityBroadcastRequest{CommunityIdentity: id, RequestID: "paced", Audience: "buddies", Text: "hello"})
	must(t, err)
	action := CommunityBroadcastAction{CommunityIdentity: id, RequestID: preview.RequestID, Token: preview.Token, Action: "send"}
	if out, err := s.ActCommunityBroadcast(ctx, action); err != nil || out.State != "preview" {
		t.Fatal(out, err)
	}
	action.Confirm = true
	if _, err := s.ActCommunityBroadcast(ctx, action); err != nil {
		t.Fatal(err)
	}
	var previous time.Time
	for _, expected := range []string{"Alice", "Bob"} {
		code, payload, err := soulseek.ReadFrame(peer)
		must(t, err)
		now := time.Now()
		failIf(t, !previous.IsZero() && now.Sub(previous) < time.Second, "broadcast was not paced")
		previous = now
		d := soulseek.NewDecoder(payload)
		name, _ := d.String()
		text, _ := d.String()
		failIf(t, code != soulseek.ServerPrivateMessage || name != expected || text != "hello", code, name, text)
	}
	action.Action = "stop"
	if _, err := s.ActCommunityBroadcast(ctx, action); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	var result CommunityBroadcastPage
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		result, err = s.CommunityBroadcast(ctx, id, preview.RequestID, 0)
		must(t, err)
		if result.Recipients[1].State == "sent" {
			break
		}
	}
	failIf(t, result.State != "stopped" || result.Recipients[0].State != "sent" || result.Recipients[1].State != "sent" || result.Recipients[2].State != "not-submitted", result)
	action.Action = "send"
	out, err := s.ActCommunityBroadcast(ctx, action)
	failIf(t, err != nil || out.State != "stopped", "blind rebroadcast", out, err)
}
func TestBroadcastInterruptedWriteIsUnknownAndNeverRebroadcast(t *testing.T) {
	s := downloadService(t)
	client, peer, id := communityTestConnection(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	s.ctx = ctx
	s.community.online = true
	s.community.buddies["Alice"] = CommunityBuddy{}
	s.community.users["Alice"] = CommunityUser{StatusFresh: true, Status: soulseek.UserStatusOnline}
	wake := s.community.wake
	s.mu.Unlock()
	preview, err := s.PreviewCommunityBroadcast(ctx, CommunityBroadcastRequest{CommunityIdentity: id, RequestID: "partial", Audience: "buddies", Text: "hello"})
	must(t, err)
	action := CommunityBroadcastAction{CommunityIdentity: id, RequestID: preview.RequestID, Token: preview.Token, Action: "send", Confirm: true}
	done := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			done <- ctx.Err()
		case <-wake:
			done <- s.syncCommunityOutbox(ctx, client, id)
		}
	}()
	if _, err := s.ActCommunityBroadcast(ctx, action); err != nil {
		t.Fatal(err)
	}
	var partial [3]byte
	if _, err := io.ReadFull(peer, partial[:]); err != nil {
		t.Fatal(err)
	}
	_ = peer.Close()
	if err := <-done; err == nil {
		t.Fatal("partial write accepted")
	}
	cancel()
	s.wg.Wait()
	out, err := s.CommunityBroadcast(context.Background(), id, preview.RequestID, 0)
	failIf(t, err != nil || out.Recipients[0].State != "unknown", out, err)
	again, err := s.ActCommunityBroadcast(context.Background(), action)
	failIf(t, err != nil || again.State == "running", "unknown automatically retried", again, err)
}
