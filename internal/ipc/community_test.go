package ipc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/storage"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func communityIPC(t *testing.T, cfg config.Config, path string) (*Client, *daemon.Service) {
	t.Helper()
	svc, err := daemon.New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(svc, filepath.Join(t.TempDir(), "community.sock"))
	ln, err := srv.Listen()
	if err != nil {
		_ = svc.Close()
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: srv.handler()}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(ln) }()
	t.Cleanup(func() { _ = httpServer.Close(); <-done; _ = srv.Close(); _ = svc.Close() })
	return NewClient(srv.path), svc
}

func TestCommunityResourcesContracts(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.DownloadDir = t.TempDir()
	client, _ := communityIPC(t, cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	ctx := context.Background()
	before, err := client.CommunitySummary(ctx)
	if err != nil || before.Account == "" || before.Daemon == "" || before.Connected || before.Unread != 0 || !slices.Contains(before.Capabilities, "watches") {
		t.Fatalf("summary %+v %v", before, err)
	}
	identity := before.CommunityIdentity
	users := []string{"Alice", "alice", "猫/?#"}
	for n := range 205 {
		users = append(users, fmt.Sprintf("user%03d", n))
	}
	for n, page := range [][]string{users[:200], users[200:]} {
		if err := client.WatchCommunityUsers(ctx, daemon.CommunityWatchRequest{CommunityIdentity: identity, Frontend: fmt.Sprint(n), Users: page}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := client.CommunitySummary(ctx)
	if err != nil || after.Revision <= before.Revision {
		t.Fatalf("watch revision %+v %v", after, err)
	}
	req := daemon.CommunityUsersRequest{CommunityIdentity: identity, Limit: 999}
	first, err := client.CommunityUsers(ctx, req)
	if err != nil || len(first.Users) != 200 || first.NextCursor != first.Users[199].Username || first.Revision != after.Revision || first.CommunityIdentity != identity {
		t.Fatalf("first page: %d %q %v", len(first.Users), first.NextCursor, err)
	}
	req.Cursor = first.NextCursor
	second, err := client.CommunityUsers(ctx, req)
	if err != nil || len(second.Users) != 8 || second.NextCursor != "" {
		t.Fatalf("second page: %d %v", len(second.Users), err)
	}
	var got []string
	for _, user := range append(first.Users, second.Users...) {
		got = append(got, user.Username)
		if user.StatusFresh || user.StatsFresh || user.AddressFresh {
			t.Fatal("offline watch invented presence")
		}
	}
	slices.Sort(users)
	if !reflect.DeepEqual(got, users) {
		t.Fatalf("identity/paging mismatch: %v", got)
	}
	filtered, err := client.CommunityUsers(ctx, daemon.CommunityUsersRequest{CommunityIdentity: identity, Query: "ALICE"})
	if err != nil || len(filtered.Users) != 2 || filtered.Users[0].Username != "Alice" || filtered.Users[1].Username != "alice" {
		t.Fatalf("presentation filter: %+v %v", filtered, err)
	}
	for _, name := range []string{"Alice", "alice", users[len(users)-1]} {
		exact, err := client.CommunityUsers(ctx, daemon.CommunityUsersRequest{CommunityIdentity: identity, Username: name})
		if err != nil || len(exact.Users) != 1 || exact.Users[0].Username != name || exact.NextCursor != "" {
			t.Fatalf("exact lookup %q: %+v %v", name, exact, err)
		}
	}
	if _, err := client.CommunityUsers(ctx, daemon.CommunityUsersRequest{CommunityIdentity: identity, Username: "Alice", Query: "alice"}); err == nil {
		t.Fatal("accepted contradictory exact/filter request")
	}
	for _, changed := range []daemon.CommunityIdentity{{Account: identity.Account, Session: identity.Session}, {Account: identity.Account, Daemon: identity.Daemon, Session: identity.Session + 1}, {Account: "other", Daemon: identity.Daemon, Session: identity.Session}} {
		payload, _ := json.Marshal(daemon.CommunityWatchRequest{CommunityIdentity: changed, Frontend: "one", Users: []string{"Alice"}})
		response, err := client.http.Do(mustRequest(http.MethodPut, "http://oto.local/v1/community/watches", bytes.NewReader(payload)))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("stale identity status %d", response.StatusCode)
		}
	}
	for _, path := range []string{"/v1/community/users", "/v1/community/users?session=no", "/v1/community/users?session=0&limit=-1"} {
		response, err := client.http.Do(mustRequest(http.MethodGet, "http://oto.local"+path, nil))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid query %s: %d", path, response.StatusCode)
		}
	}
	if err := client.WatchCommunityUsers(ctx, daemon.CommunityWatchRequest{CommunityIdentity: identity, Frontend: "too-many", Users: make([]string, 201)}); err == nil {
		t.Fatal("accepted unbounded watch list")
	}
	oversized := daemon.CommunityWatchRequest{CommunityIdentity: identity, Frontend: "oversized", Users: []string{strings.Repeat("x", int(MaxBodySize)+1)}}
	if err := client.WatchCommunityUsers(ctx, oversized); err == nil || !strings.Contains(err.Error(), "request too large") {
		t.Fatalf("client body budget not enforced: %v", err)
	}
	payload, _ := json.Marshal(oversized)
	response, err := client.http.Do(mustRequest(http.MethodPut, "http://oto.local/v1/community/watches", bytes.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	var rejection struct {
		Error string `json:"error"`
	}
	err = json.NewDecoder(response.Body).Decode(&rejection)
	_ = response.Body.Close()
	if err != nil || response.StatusCode == http.StatusOK || !strings.Contains(rejection.Error, "request body too large") {
		t.Fatalf("server body budget: %+v %v", rejection, err)
	}
	snapshot, err := client.Status(ctx)
	if err != nil || !reflect.DeepEqual(snapshot.CommunityCapabilities, before.Capabilities) {
		t.Fatalf("state capabilities: %v", err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || bytes.Contains(encoded, []byte("Alice")) {
		t.Fatalf("full snapshot leaked social records: %v", err)
	}
}

func TestCommunitySummaryUnreadAndRestartFence(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.DownloadDir = t.TempDir()
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	client, svc := communityIPC(t, cfg, path)
	ctx := context.Background()
	initial, err := client.CommunitySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	err = store.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		conversation, err := q.EnsureCommunityConversation(ctx, db.EnsureCommunityConversationParams{Account: initial.Account, Kind: "private", Target: "Alice"})
		if err != nil {
			return err
		}
		for _, item := range []struct {
			direction, state string
			mention          int64
		}{{"incoming", "received", 1}, {"incoming", "held", 1}, {"outgoing", "sent", 0}} {
			_, err := q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: initial.Account, ConversationID: conversation.ID, Sender: "Alice", Direction: item.direction, Body: "private-body-not-in-summary", CreatedAt: 1, State: item.state, Mention: item.mention})
			if err != nil {
				return err
			}
		}
		_, err = q.BumpCommunityRevision(ctx, initial.Account)
		return err
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	summary, err := client.CommunitySummary(ctx)
	if err != nil || summary.Unread != 1 || summary.Mentions != 1 || summary.Revision <= initial.Revision {
		t.Fatalf("unread: %+v %v", summary, err)
	}
	encoded, _ := json.Marshal(summary)
	if bytes.Contains(encoded, []byte("private-body")) || bytes.Contains(encoded, []byte("Alice")) {
		t.Fatal("summary exposed content")
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, _ := communityIPC(t, cfg, path)
	fresh, err := restarted.CommunitySummary(ctx)
	if err != nil || fresh.Daemon == initial.Daemon || fresh.Account != initial.Account || fresh.Unread != 1 {
		t.Fatalf("restart identity: %+v %v", fresh, err)
	}
	if err := restarted.WatchCommunityUsers(ctx, daemon.CommunityWatchRequest{CommunityIdentity: initial.CommunityIdentity, Frontend: "old", Users: []string{"Bob"}}); err == nil {
		t.Fatal("previous daemon request accepted")
	}
}
