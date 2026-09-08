package daemon

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/testutil"
)

type communityIdleContext struct {
	context.Context
	once  sync.Once
	ready chan struct{}
}

func (c *communityIdleContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.ready) })
	return c.Context.Done()
}

func TestCommunityPrivateShutdownDrain(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(map[bool]string{false: "incoming", true: "session-worker"}[worker], func(t *testing.T) {
			s, manager, active, _ := uploadService(t)
			s.cfg.Uploads.WaitForActiveUploadsOnQuit = true
			left, right := net.Pipe()
			_ = right.SetDeadline(time.Now().Add(3 * time.Second))
			identity := s.community.identity
			client := soulseek.NewClientOnConn(soulseek.ClientConfig{Uploads: manager, SocialUpdate: func(ctx context.Context, message soulseek.SocialMessage) error {
				return s.communityUpdate(ctx, identity, message)
			}}, left)
			s.client, s.community.online = client, true
			if _, err := s.SendCommunityPrivate(context.Background(), CommunitySendRequest{CommunityIdentity: identity, Username: "Bob", Text: "wait until next session", RequestID: "before-drain"}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			run, drained := make(chan error, 1), make(chan struct{})
			go func() { run <- client.Run(ctx) }()
			go func() { s.WaitForUploads(ctx); close(drained) }()
			var workerDone chan struct{}
			t.Cleanup(func() {
				cancel()
				_ = right.Close()
				_ = client.Close()
				<-run
				<-drained
				if workerDone != nil {
					<-workerDone
				}
			})
			deadline := time.Now().Add(time.Second)
			for {
				if draining, count := client.UploadDrainStatus(); draining && count == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("upload drain not entered")
				}
				time.Sleep(time.Millisecond)
			}
			if worker {
				idle := &communityIdleContext{Context: ctx, ready: make(chan struct{})}
				workerDone = make(chan struct{})
				go func() { s.consumeClientEvents(idle, client); close(workerDone) }()
				select {
				case <-idle.ready: // The initial refresh completed without closing the draining client.
				case <-workerDone:
					t.Fatal("community refresh aborted upload drain")
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			for _, fixture := range testutil.SocialFixtures(t) {
				if fixture.Name != "pm-online" {
					continue
				}
				if err := soulseek.WriteFrame(right, fixture.Code, fixture.Payload(t)); err != nil {
					t.Fatal(err)
				}
				break
			}
			code, _, err := soulseek.ReadFrame(right)
			if err != nil || code != soulseek.ServerPrivateAck {
				t.Fatalf("incoming PM stopped drain or sent queued work: %d %v", code, err)
			}
			if draining, count := client.UploadDrainStatus(); !draining || count != 1 {
				t.Fatal("active upload was cancelled")
			}
			select {
			case <-drained:
				t.Fatal("drain returned before upload completed")
			default:
			}
			var queued, received int
			if err := s.stateDB.SQL().QueryRow("SELECT count(*) FROM community_messages WHERE state = 'queued'").Scan(&queued); err != nil || queued != 1 {
				t.Fatal("outbox sent during drain", queued, err)
			}
			if err := s.stateDB.SQL().QueryRow("SELECT count(*) FROM community_messages WHERE state = 'received'").Scan(&received); err != nil || received != 1 {
				t.Fatal("PM acknowledged without durable storage", received, err)
			}
			manager.Done(active)
			select {
			case <-drained:
			case <-ctx.Done():
				t.Fatal("completed upload did not release drain")
			}
		})
	}
}
