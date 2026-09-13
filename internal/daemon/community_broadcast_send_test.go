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
	if err != nil {
		t.Fatal(err)
	}
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
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if !previous.IsZero() && now.Sub(previous) < time.Second {
			t.Fatal("broadcast was not paced")
		}
		previous = now
		d := soulseek.NewDecoder(payload)
		name, _ := d.String()
		text, _ := d.String()
		if code != soulseek.ServerPrivateMessage || name != expected || text != "hello" {
			t.Fatal(code, name, text)
		}
	}
	action.Action = "stop"
	if _, err := s.ActCommunityBroadcast(ctx, action); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	var result CommunityBroadcastPage
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		result, err = s.CommunityBroadcast(ctx, id, preview.RequestID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if result.Recipients[1].State == "sent" {
			break
		}
	}
	if result.State != "stopped" || result.Recipients[0].State != "sent" || result.Recipients[1].State != "sent" || result.Recipients[2].State != "not-submitted" {
		t.Fatal(result)
	}
	action.Action = "send"
	out, err := s.ActCommunityBroadcast(ctx, action)
	if err != nil || out.State != "stopped" {
		t.Fatal("blind rebroadcast", out, err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil || out.Recipients[0].State != "unknown" {
		t.Fatal(out, err)
	}
	again, err := s.ActCommunityBroadcast(context.Background(), action)
	if err != nil || again.State == "running" {
		t.Fatal("unknown automatically retried", again, err)
	}
}
