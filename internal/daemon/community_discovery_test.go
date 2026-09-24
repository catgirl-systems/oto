package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCommunityDiscoveryCorrelationLateRepliesAndPaging(t *testing.T) {
	s := downloadService(t)
	_, peer, id := communityTestConnection(t, s)
	ctx := context.Background()
	req := CommunityDiscoveryRequest{CommunityIdentity: id, Frontend: "first", Kind: "global", Limit: 1}
	start := func(req CommunityDiscoveryRequest, fixture string) CommunityDiscoveryPage {
		t.Helper()
		out, err := s.StartCommunityDiscovery(ctx, req)
		failIf(t, err != nil || out.State != "pending", out, err)
		f := roomFixture(t, fixture)
		code, p, err := soulseek.ReadFrame(peer)
		failIf(t, err != nil || code != f.Code || !bytes.Equal(p, f.Payload(t)), fixture, code, p, err)
		return out
	}
	apply := func(fixture string) {
		t.Helper()
		f := roomFixture(t, fixture)
		m, err := soulseek.DecodeDiscoveryResponse(f.Code, f.Payload(t))
		must(t, err)
		must(t, s.communityUpdate(ctx, id, m))
	}
	global := start(req, "discovery-global-request")
	duplicate, err := s.StartCommunityDiscovery(ctx, req)
	failIf(t, err != nil || duplicate.Generation != global.Generation, "duplicate query", duplicate, err)
	personalReq := req
	personalReq.Kind = "personal"
	start(personalReq, "discovery-personal-request") // Also detects accidental global retransmission.
	apply("discovery-personal")
	page, err := s.CommunityDiscovery(ctx, personalReq)
	failIf(t, err != nil || page.State != "ready" || page.Total != 2 || len(page.Rows) != 1 || page.Rows[0].Item != "techno" || page.Rows[0].Score != 42 || page.NextCursor == "", page, err)
	oldCursor := page.NextCursor
	personalReq.Cursor = oldCursor
	next, err := s.CommunityDiscovery(ctx, personalReq)
	failIf(t, err != nil || len(next.Rows) != 1 || next.Rows[0].Score != -7, next, err)
	s.mu.Lock()
	s.community.discovery.queries[communityDiscoveryKey{Kind: "global"}].deadline = time.Now().Add(-time.Second)
	s.mu.Unlock()
	pending, err := s.CommunityDiscovery(ctx, req)
	failIf(t, err != nil || pending.State != "unknown", pending, err)
	req.Refresh = true
	pending, err = s.StartCommunityDiscovery(ctx, req)
	failIf(t, err != nil || pending.State != "unknown" || pending.Generation != global.Generation, "reissued uncertain tokenless query", pending, err)
	itemReq := req
	itemReq.Kind, itemReq.Target = "item", "  TECHno  "
	start(itemReq, "discovery-item-request")
	must(t, s.communityUpdate(ctx, id, soulseek.DiscoveryResponse{Kind: "item", Target: "TECHNO"}))
	item, err := s.CommunityDiscovery(ctx, itemReq)
	failIf(t, err != nil || item.State != "pending", "normalized reply identity", item, err)
	apply("discovery-item")
	apply("discovery-global")
	globalReady, err := s.CommunityDiscovery(ctx, req)
	failIf(t, err != nil || globalReady.State != "ready" || globalReady.Generation != global.Generation, "late original reply", globalReady, err)
	personalReq.Cursor = ""
	personalReq.Refresh = true
	start(personalReq, "discovery-personal-request")
	personalReq.Cursor = oldCursor
	if _, err := s.CommunityDiscovery(ctx, personalReq); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("accepted stale cursor", err)
	}
	apply("discovery-personal")
	// Fresh requests to a different exact username must not consume Alice's reply.
	userReq := req
	userReq.Kind, userReq.Target = "user-interests", "Alice"
	start(userReq, "discovery-user-interests-request")
	apply("discovery-user-interests")
	interests, err := s.CommunityDiscovery(ctx, userReq)
	failIf(t, err != nil || interests.State != "ready" || interests.Total != 2, interests, err)
	userReq.Target = "alice"
	empty, err := s.CommunityDiscovery(ctx, userReq)
	failIf(t, err != nil || empty.State != "idle", "case-folded username", empty, err)
	s.mu.Lock()
	s.retireCommunityLocked()
	s.mu.Unlock()
	stale, err := s.CommunityDiscovery(ctx, req)
	failIf(t, err != nil || stale.State != "offline" || stale.Total == 0, "lost stale results", stale, err)
	if err := s.communityUpdate(ctx, id, soulseek.DiscoveryResponse{Kind: "global"}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("retired session applied response", err)
	}
	// An oversized first row must error instead of returning an empty page with
	// a non-advancing cursor (client livelock until results expire).
	s.mu.Lock()
	s.community.discovery.queries[communityDiscoveryKey{Kind: "global"}].rows[0].Item = strings.Repeat("<", 96<<10)
	s.mu.Unlock()
	if _, err := s.CommunityDiscovery(ctx, req); !errors.Is(err, errPageItemTooLarge) {
		t.Fatal("oversized discovery row", err)
	}
}

func TestCommunityDiscoveryCacheBoundsAndWatchOwnership(t *testing.T) {
	s := downloadService(t)
	_, _, id := communityTestConnection(t, s)
	ctx := context.Background()
	key := communityDiscoveryKey{Kind: "similar"}
	response := soulseek.DiscoveryResponse{Kind: key.Kind}
	for i := 0; i < 300; i++ {
		response.Users = append(response.Users, soulseek.SimilarUser{Username: fmt.Sprintf("user-%03d", i), Rating: uint32(300 - i)})
	}
	s.mu.Lock()
	s.community.discovery.queries[key] = &communityDiscoveryQuery{generation: 1, awaiting: true, state: "pending"}
	s.applyDiscoveryLocked(response)
	s.mu.Unlock()
	req := CommunityDiscoveryRequest{CommunityIdentity: id, Kind: "similar", Frontend: "first", Limit: 200}
	page, err := s.CommunityDiscovery(ctx, req)
	failIf(t, err != nil || len(page.Rows) != 200 || page.NextCursor == "", page, err)
	must(t, s.WatchCommunityUsers(id, "profile", []string{"user-000"}))
	req.Frontend, req.Cursor = "second", page.NextCursor
	next, err := s.CommunityDiscovery(ctx, req)
	failIf(t, err != nil || len(next.Rows) != 100, next, err)
	s.mu.Lock()
	watched := len(s.desiredUserWatchesLocked(time.Now()))
	s.mu.Unlock()
	failIf(t, watched != 200, "watch limit", watched)
	// Releasing first's discovery page cannot release the independent profile.
	if _, err := s.CommunityDiscovery(ctx, CommunityDiscoveryRequest{CommunityIdentity: id, Kind: "global", Frontend: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommunityDiscovery(ctx, req); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	watched = len(s.desiredUserWatchesLocked(time.Now()))
	_, profile := s.community.users["user-000"]
	s.mu.Unlock()
	failIf(t, watched != 101 || !profile, "shared ownership", watched, profile)
	s.mu.Lock()
	s.desiredUserWatchesLocked(time.Now().Add(2 * time.Minute))
	remaining := len(s.community.users)
	s.mu.Unlock()
	failIf(t, remaining != 0, "unexpired detached frontend", remaining)
	// Completed resources are reclaimable; unanswered queries are not.
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := 0; i < 40; i++ {
			k := communityDiscoveryKey{Kind: "item", Target: fmt.Sprint(i)}
			failIf(t, len(s.community.discovery.queries) >= 32 && !s.evictDiscoveryLocked(k), "could not evict completed query")
			s.community.discovery.queries[k] = &communityDiscoveryQuery{awaiting: true, state: "pending"}
			s.applyDiscoveryLocked(soulseek.DiscoveryResponse{Kind: k.Kind, Target: k.Target, Recommendations: []soulseek.ScoredInterest{{Item: "\x1b[2Jbad"}}})
		}
		failIf(t, len(s.community.discovery.queries) != 32, "cache count")
		rows := make([]soulseek.ScoredInterest, 3500)
		for i := range rows {
			rows[i].Item = strings.Repeat("x", 1024)
		}
		for i := 0; i < 6; i++ {
			k := communityDiscoveryKey{Kind: "item", Target: fmt.Sprintf("large-%d", i)}
			if len(s.community.discovery.queries) >= 32 {
				s.evictDiscoveryLocked(k)
			}
			s.community.discovery.queries[k] = &communityDiscoveryQuery{awaiting: true, state: "pending"}
			s.applyDiscoveryLocked(soulseek.DiscoveryResponse{Kind: k.Kind, Target: k.Target, Recommendations: rows})
		}
		failIf(t, s.community.discovery.queryBytes > maxCommunityDiscoveryBytes, "cache byte limit")
		var measured int
		for _, q := range s.community.discovery.queries {
			measured += q.bytes
			for _, row := range q.rows {
				failIf(t, strings.ContainsRune(row.Item, '\x1b'), "terminal control in discovery")
			}
		}
		failIf(t, measured != s.community.discovery.queryBytes, "cache accounting", measured, s.community.discovery.queryBytes)
	}()
}

func TestCommunityDiscoveryPendingLimitAndShutdown(t *testing.T) {
	s := downloadService(t)
	_, _, id := communityTestConnection(t, s)
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.StartCommunityDiscovery(cancelled, CommunityDiscoveryRequest{CommunityIdentity: id, Kind: "global", Frontend: "one"}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled request", err)
	}
	s.mu.Lock()
	for i := 0; i < 32; i++ {
		s.community.discovery.queries[communityDiscoveryKey{"item", fmt.Sprint(i)}] = &communityDiscoveryQuery{state: "unknown", awaiting: true}
	}
	s.mu.Unlock()
	req := CommunityDiscoveryRequest{CommunityIdentity: id, Kind: "global", Frontend: "one"}
	if _, err := s.StartCommunityDiscovery(ctx, req); err == nil {
		t.Fatal("evicted pending tokenless query")
	}
	s.mu.Lock()
	s.community.discovery.queries = map[communityDiscoveryKey]*communityDiscoveryQuery{}
	s.mu.Unlock()
	if _, err := s.StartCommunityDiscovery(ctx, req); err != nil {
		t.Fatal(err)
	} // Peer deliberately never reads.
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown blocked on discovery write")
	}
}

func TestCommunityDiscoveryReviewRegressions(t *testing.T) {
	s := downloadService(t)
	client, _, _ := communityTestConnection(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := &communityDiscoveryQuery{state: "pending"}
	attempted, err := client.SendDiscovery(ctx, soulseek.DiscoveryRequest{Kind: "global"}, func() error { q.awaiting = true; cancel(); return nil })
	failIf(t, !errors.Is(err, context.Canceled) || attempted, "reservation cancellation", attempted, err)
	q.writeFailed(attempted)
	failIf(t, q.awaiting || q.state != "failed", "definitely unsent request retained tokenless reservation", q)
	q.writeFailed(true)
	failIf(t, !q.awaiting || q.state != "unknown", "ambiguous request became retriable", q)
	response := soulseek.DiscoveryResponse{Kind: "similar", Users: make([]soulseek.SimilarUser, soulseek.MaxDiscoveryEntries)}
	for i := range response.Users {
		response.Users[i].Username = "Alice"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := communityDiscoveryKey{Kind: "similar"}
	s.community.discovery.queries[key] = &communityDiscoveryQuery{awaiting: true, state: "pending"}
	s.applyDiscoveryLocked(response)
	cached := s.community.discovery.queries[key]
	failIf(t, len(cached.rows) != 1 || cap(cached.rows) > 2, "dedup retained incoming backing capacity", len(cached.rows), cap(cached.rows))
	failIf(t, cached.bytes != 512+len("Alice"), "dedup accounting", cached.bytes)
}
