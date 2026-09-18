package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestSharePolicyRecoveryReadsAddressBeforeAdmission(t *testing.T) {
	fixtures := testutil.SocialFixtureMap(t)
	addressSeen := make(chan struct{}, 1)
	server := testutil.ListenScript(t, func(_ context.Context, conn net.Conn) error {
		for {
			code, _, err := soulseek.ReadFrame(conn)
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if err != nil {
				return err
			}
			var fixture testutil.WireFixture
			switch code {
			case soulseek.ServerLogin:
				fixture = fixtures["login-ok"]
			case soulseek.ServerGetPeerAddress:
				fixture = fixtures["peer-address"]
				select {
				case addressSeen <- struct{}{}:
				default:
				}
			default:
				continue
			}
			if err := soulseek.WriteFrame(conn, fixture.Code, fixture.Payload(t)); err != nil {
				return err
			}
		}
	})
	cfg := testConfig(t)
	cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = server.Listener.Addr().String(), closedAddress(t)
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "song"), []byte("test"), 0600))
	cfg.Shares = []config.Share{{Name: "Music", Path: root}}
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	seed, err := New(cfg, path)
	must(t, err)
	t.Cleanup(func() { _ = seed.Close() })
	seed.uploadEpoch = 1
	must(t, seed.uploadAccepted(1, soulseek.TransferEvent{Username: "Alice", Filename: `Music\song`, Attempt: 1, Total: 4}))
	summary, err := seed.CommunitySummary(context.Background())
	must(t, err)
	_, err = seed.SetCommunityRule(context.Background(), CommunityRuleRequest{CommunityIdentity: summary.CommunityIdentity, Revision: 0, Confirm: true, Rule: CommunityRule{Action: "ban", Kind: "ip", Value: "127.0.0.1", Message: "Recovery policy denial"}})
	must(t, err)
	must(t, seed.Close())
	s, err := New(cfg, path)
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })
	must(t, s.shares.AddRoot("Music", root))
	must(t, s.shares.ScanContext(context.Background()))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	must(t, s.Start(ctx))
	for {
		s.mu.RLock()
		u := s.journal.Uploads[0]
		s.mu.RUnlock()
		if u.State == "failed" {
			failIf(t, u.Recoverable || u.Error != "Recovery policy denial", "wrong recovery outcome", u)
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("recovery did not process server address", u, ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-addressSeen:
	default:
		t.Fatal("recovery skipped address verification")
	}
}
