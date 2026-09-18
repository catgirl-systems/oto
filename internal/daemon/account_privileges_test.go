package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func privilegeTestService(t *testing.T) (*Service, net.Conn, string) {
	t.Helper()
	cfg := config.Default()
	cfg.Soulseek.Username = "local"
	cfg.Soulseek.Password = "local-only"
	cfg.DownloadDir = t.TempDir()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	_ = right.SetDeadline(time.Now().Add(5 * time.Second))
	id := s.community.identity
	s.client = soulseek.NewClientOnConn(soulseek.ClientConfig{SocialUpdate: func(ctx context.Context, m soulseek.SocialMessage) error { return s.communityUpdate(ctx, id, m) }}, left)
	s.community.online = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	client := s.client
	go func() { defer close(done); _ = client.Run(ctx) }()
	t.Cleanup(func() { cancel(); _ = right.Close(); _ = s.Close(); <-done })
	return s, right, path
}
func privilegeFixture(t *testing.T, name string) testutil.WireFixture {
	t.Helper()
	return testutil.SocialFixture(t, name)
}
func loadTestPrivileges(t *testing.T, s *Service, peer net.Conn) AccountPrivileges {
	t.Helper()
	done := make(chan AccountPrivileges, 1)
	go func() {
		out, err := s.AccountPrivileges(context.Background(), AccountPrivilegesRequest{CommunityIdentity: s.community.identity})
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	code, _, err := soulseek.ReadFrame(peer)
	failIf(t, err != nil || code != 92, code, err)
	f := privilegeFixture(t, "privilege-balance")
	must(t, soulseek.WriteFrame(peer, f.Code, f.Payload(t)))
	out := <-done
	failIf(t, !out.Fresh || !out.Known || out.Seconds < 259259 || out.Seconds > 259260, out)
	return out
}
func TestAccountPrivilegesGiftPreviewJournalAndRestart(t *testing.T) {
	s, peer, path := privilegeTestService(t)
	ctx := context.Background()
	balance := loadTestPrivileges(t, s, peer)
	req := AccountPrivilegeGiftRequest{CommunityIdentity: balance.CommunityIdentity, RequestID: "gift-one", Username: "Alice", Days: 1}
	preview, err := s.GiftAccountPrivileges(ctx, req)
	if err != nil || preview.State != "preview" || preview.Balance.Revision != balance.Revision {
		t.Fatal(preview, err)
	}
	if _, err := s.stateDB.Queries().GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("preview persisted a gift", err)
	}
	for _, bad := range []AccountPrivilegeGiftRequest{{CommunityIdentity: req.CommunityIdentity, RequestID: "zero", Username: "Alice"}, {CommunityIdentity: req.CommunityIdentity, RequestID: "huge", Username: "Alice", Days: ^uint32(0)}, {CommunityIdentity: req.CommunityIdentity, RequestID: "self", Username: "local", Days: 1}} {
		if _, err := s.GiftAccountPrivileges(ctx, bad); err == nil {
			t.Fatal("invalid gift accepted", bad)
		}
	}
	req.Confirm = true
	req.Revision = preview.Balance.Revision
	done := make(chan AccountPrivilegeGiftResult, 1)
	go func() {
		out, err := s.GiftAccountPrivileges(ctx, req)
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	// net.Pipe blocks the writer until read; the unknown receipt must already exist.
	for deadline := time.Now().Add(time.Second); ; {
		row, err := s.stateDB.Queries().GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID})
		if err == nil {
			failIf(t, !bytes.Contains([]byte(row.Result), []byte(`"state":"unknown"`)), row.Result)
			break
		}
		failIf(t, time.Now().After(deadline), "no pre-write receipt", err)
		time.Sleep(time.Millisecond)
	}
	pending, err := s.AccountPrivileges(ctx, AccountPrivilegesRequest{CommunityIdentity: req.CommunityIdentity, Refresh: true})
	failIf(t, err != nil || !pending.Pending || pending.Fresh, "query crossed gift writer", pending, err)
	code, payload, err := soulseek.ReadFrame(peer)
	fixture := privilegeFixture(t, "give-privileges-request")
	failIf(t, err != nil || code != fixture.Code || !bytes.Equal(payload, fixture.Payload(t)), code, payload, err)
	out := <-done
	failIf(t, out.State != "unknown", out)
	duplicate, err := s.GiftAccountPrivileges(ctx, req)
	failIf(t, err != nil || !duplicate.Duplicate || duplicate.State != "unknown", duplicate, err)
	req.Days = 2
	if _, err := s.GiftAccountPrivileges(ctx, req); err == nil {
		t.Fatal("request ID reused for different amount")
	}
	req.Days = 1
	cfg := s.cfg
	must(t, s.Close())
	next, err := New(cfg, path)
	must(t, err)
	defer next.Close()
	req.CommunityIdentity = next.community.identity
	duplicate, err = next.GiftAccountPrivileges(ctx, req)
	failIf(t, err != nil || !duplicate.Duplicate || duplicate.State != "unknown", "restart lost ambiguity journal", duplicate, err)
}

func TestAccountPrivilegeQueryCancellationAndLateReply(t *testing.T) {
	s, peer, _ := privilegeTestService(t)
	identity := s.community.identity
	done := make(chan AccountPrivileges, 1)
	go func() {
		out, err := s.AccountPrivileges(context.Background(), AccountPrivilegesRequest{CommunityIdentity: identity})
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	if code, _, err := soulseek.ReadFrame(peer); err != nil || code != 92 {
		t.Fatal(code, err)
	}
	s.mu.Lock()
	s.community.privileges.queryCancel()
	s.mu.Unlock()
	out := <-done
	failIf(t, out.Fresh || out.Error == "", out)
	f := privilegeFixture(t, "privilege-balance")
	must(t, soulseek.WriteFrame(peer, f.Code, f.Payload(t)))
	out, err := s.AccountPrivileges(context.Background(), AccountPrivilegesRequest{CommunityIdentity: identity, Refresh: true})
	failIf(t, err != nil || out.Fresh || out.Known || out.Pending, "late response acquired authority", out, err)
	s.mu.Lock()
	s.retireCommunityLocked()
	s.community.identity.Session++
	s.mu.Unlock()
	if _, err := s.AccountPrivileges(context.Background(), AccountPrivilegesRequest{CommunityIdentity: identity}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal(err)
	}
}

func TestAccountPrivilegeCoalescedQuerySurvivesCallerCancellation(t *testing.T) {
	s, peer, _ := privilegeTestService(t)
	id := s.community.identity
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := s.AccountPrivileges(ctx, AccountPrivilegesRequest{CommunityIdentity: id})
		first <- err
	}()
	if code, _, err := soulseek.ReadFrame(peer); err != nil || code != 92 {
		t.Fatal(code, err)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	second := make(chan AccountPrivileges, 1)
	go func() {
		out, err := s.AccountPrivileges(context.Background(), AccountPrivilegesRequest{CommunityIdentity: id})
		if err != nil {
			t.Error(err)
		}
		second <- out
	}()
	f := privilegeFixture(t, "privilege-balance-empty")
	must(t, soulseek.WriteFrame(peer, f.Code, f.Payload(t)))
	out := <-second
	failIf(t, !out.Fresh || !out.Known || out.Seconds != 0, out)
	if _, err := s.GiftAccountPrivileges(context.Background(), AccountPrivilegeGiftRequest{CommunityIdentity: id, RequestID: "empty", Username: "Alice", Days: 1}); err == nil {
		t.Fatal("empty balance allowed gifting")
	}
}

func TestAccountPrivilegePartialGiftWriteRemainsUnknown(t *testing.T) {
	s, peer, _ := privilegeTestService(t)
	balance := loadTestPrivileges(t, s, peer)
	req := AccountPrivilegeGiftRequest{CommunityIdentity: balance.CommunityIdentity, RequestID: "partial", Username: "Alice", Days: 1, Revision: balance.Revision, Confirm: true}
	done := make(chan AccountPrivilegeGiftResult, 1)
	go func() {
		out, err := s.GiftAccountPrivileges(context.Background(), req)
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	var prefix [1]byte
	if _, err := peer.Read(prefix[:]); err != nil {
		t.Fatal(err)
	}
	_ = peer.Close()
	out := <-done
	if out.State != "unknown" || out.Balance.Fresh {
		t.Fatal(out)
	}
	again, err := s.GiftAccountPrivileges(context.Background(), req)
	if err != nil || !again.Duplicate || again.State != "unknown" {
		t.Fatal(again, err)
	}
}
