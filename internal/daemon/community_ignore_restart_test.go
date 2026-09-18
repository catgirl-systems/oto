package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityIgnoreHeldWorkerSurvivesRestart(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "discard"}[blocked], func(t *testing.T) {
			fixtures := testutil.SocialFixtureMap(t)
			var connections atomic.Int32
			ack := make(chan struct{}, 1)
			server := testutil.ListenScript(t, func(_ context.Context, conn net.Conn) error {
				generation := connections.Add(1)
				write := func(name string) error { f := fixtures[name]; return soulseek.WriteFrame(conn, f.Code, f.Payload(t)) }
				for {
					code, _, err := soulseek.ReadFrame(conn)
					if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
						return nil
					}
					if err != nil {
						return err
					}
					switch code {
					case soulseek.ServerLogin:
						if err := write("login-ok"); err != nil {
							return err
						}
						if generation == 1 {
							if err := write("pm-online"); err != nil {
								return err
							}
						}
					case soulseek.ServerPrivateAck:
						select {
						case ack <- struct{}{}:
						default:
						}
					case soulseek.ServerGetPeerAddress:
						if generation > 1 {
							if err := write("peer-address"); err != nil {
								return err
							}
						}
					}
				}
			})
			cfg := testConfig(t)
			cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = server.Listener.Addr().String(), closedAddress(t)
			path := filepath.Join(t.TempDir(), "state.sqlite3")
			s, err := New(cfg, path)
			must(t, err)
			t.Cleanup(func() { _ = s.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			summary, err := s.CommunitySummary(ctx)
			must(t, err)
			value := "192.0.2.0/24"
			if blocked {
				value = "127.0.0.1"
			}
			if _, err := s.SetCommunityRule(ctx, CommunityRuleRequest{CommunityIdentity: summary.CommunityIdentity, Revision: 0, Confirm: true, Rule: CommunityRule{Action: "ignore", Kind: "ip", Value: value}}); err != nil {
				t.Fatal(err)
			}
			must(t, s.Start(ctx))
			select {
			case <-ack:
			case <-ctx.Done():
				t.Fatal("held message not acknowledged", ctx.Err())
			}
			summary, err = s.CommunitySummary(ctx)
			failIf(t, err != nil || summary.Unread != 0, "held unread notification", summary, err)
			held, err := s.stateDB.Queries().ListHeldCommunityMessages(ctx, db.ListHeldCommunityMessagesParams{Account: summary.Account, Sender: "Alice", PageSize: 200})
			failIf(t, err != nil || len(held) != 1, "ACK preceded durable holding", held, err)
			must(t, s.Close())
			s, err = New(cfg, path)
			must(t, err)
			must(t, s.Start(ctx))
			for {
				held, err = s.stateDB.Queries().ListHeldCommunityMessages(ctx, db.ListHeldCommunityMessagesParams{Account: summary.Account, Sender: "Alice", PageSize: 200})
				must(t, err)
				if len(held) == 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("restarted held-message worker stalled", ctx.Err())
				case <-time.After(time.Millisecond):
				}
			}
			summary, err = s.CommunitySummary(ctx)
			must(t, err)
			want := 0
			if !blocked {
				want = 1
			}
			page, err := s.CommunityConversations(ctx, CommunityConversationsRequest{CommunityIdentity: summary.CommunityIdentity})
			failIf(t, err != nil || len(page.Conversations) != want || int(summary.Unread) != want, "restart visibility", page, summary, err)
		})
	}
}
