package daemon

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAPIAuthApproveFlow(t *testing.T) {
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer s.Close()

	token, err := s.CreateAPIAuthRequest("my-script", "curl/8.0", "127.0.0.1")
	failIfFmt(t, err != nil || token == "" || !strings.HasPrefix(token, "oto"), "create: %q %v", token, err)
	if status, appToken := s.APIAuthRequestStatus(token); status != "pending" || appToken != "" {
		t.Fatalf("status before approval: %q %q", status, appToken)
	}
	pending := s.ListAPIAuthRequests()
	failIfFmt(t, len(pending) != 1 || pending[0].Name != "my-script" || pending[0].UserAgent != "curl/8.0" || pending[0].SourceIP != "127.0.0.1", "pending: %+v", pending)
	if count, name := s.PendingAPIAuthSummary(); count != 1 || name != "my-script" {
		t.Fatalf("summary: %d %q", count, name)
	}

	must(t, s.ApproveAPIAuthRequest(pending[0].ID, 0)) // 0 = never expires
	status, appToken := s.APIAuthRequestStatus(token)
	failIfFmt(t, status != "approved" || appToken == "" || !strings.HasPrefix(appToken, "oto"), "approved poll: %q %q", status, appToken)
	if status, again := s.APIAuthRequestStatus(token); status != "approved" || again != "" {
		t.Fatalf("app token delivered twice: %q %q", status, again)
	}
	if !s.AuthenticateAPIToken(appToken) {
		t.Fatal("app token rejected")
	}
	if s.AuthenticateAPIToken("oto-forged") {
		t.Fatal("forged token accepted")
	}
	apps, err := s.ListAPIApps()
	failIfFmt(t, err != nil || len(apps) != 1 || apps[0].Name != "my-script" || apps[0].LastUsedAt == nil, "apps: %+v %v", apps, err)

	// Request and app tokens are distinct: the poll token never authenticates.
	if s.AuthenticateAPIToken(token) {
		t.Fatal("request token authenticates")
	}
}

func TestAPIAuthRejectAndUnknown(t *testing.T) {
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer s.Close()
	token, err := s.CreateAPIAuthRequest("bad", "ua", "10.0.0.1")
	must(t, err)
	pending := s.ListAPIAuthRequests()
	must(t, s.RejectAPIAuthRequest(pending[0].ID))
	if status, appToken := s.APIAuthRequestStatus(token); status != "rejected" || appToken != "" {
		t.Fatalf("rejected: %q %q", status, appToken)
	}
	if status, _ := s.APIAuthRequestStatus("oto-unknown"); status != "unknown" {
		t.Fatalf("unknown: %q", status)
	}
	if err := s.ApproveAPIAuthRequest(pending[0].ID, 0); err == nil {
		t.Fatal("approved a rejected request")
	}
}

func TestAPIAuthCaps(t *testing.T) {
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer s.Close()
	for i := 0; i < apiMaxPendingPerIP; i++ {
		_, err := s.CreateAPIAuthRequest("app", "ua", "10.0.0.9")
		must(t, err)
	}
	if _, err := s.CreateAPIAuthRequest("one-too-many", "ua", "10.0.0.9"); err != ErrTooManyAPIAuthRequests {
		t.Fatalf("per-IP cap: %v", err)
	}
	// A different source IP still pairs.
	if _, err := s.CreateAPIAuthRequest("other-ip", "ua", "10.0.0.10"); err != nil {
		t.Fatalf("other IP: %v", err)
	}
	if _, err := s.CreateAPIAuthRequest("", "ua", "10.0.0.11"); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := s.CreateAPIAuthRequest(strings.Repeat("x", apiNameMaxRunes+1), "ua", "10.0.0.11"); err == nil {
		t.Fatal("long name accepted")
	}
}

func TestAPIAuthExpiry(t *testing.T) {
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer s.Close()
	token, err := s.CreateAPIAuthRequest("expiring", "ua", "10.0.0.2")
	must(t, err)
	s.apiAuth.mu.Lock()
	for _, request := range s.apiAuth.pending {
		request.expiresAt = time.Now().Add(-time.Second)
	}
	s.apiAuth.mu.Unlock()
	if status, _ := s.APIAuthRequestStatus(token); status != "unknown" {
		t.Fatalf("expired status: %q", status)
	}
	if len(s.ListAPIAuthRequests()) != 0 {
		t.Fatal("expired request still listed")
	}
}

func TestAPIRevokeAndHashAtRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := New(testConfig(t), path)
	must(t, err)
	defer s.Close()
	token, err := s.CreateAPIAuthRequest("revoked", "ua", "10.0.0.3")
	must(t, err)
	pending := s.ListAPIAuthRequests()
	must(t, s.ApproveAPIAuthRequest(pending[0].ID, 24*time.Hour))
	_, appToken := s.APIAuthRequestStatus(token)
	if !s.AuthenticateAPIToken(appToken) {
		t.Fatal("app token rejected before revoke")
	}
	apps, err := s.ListAPIApps()
	must(t, err)
	must(t, s.RevokeAPIApp(apps[0].ID))
	if s.AuthenticateAPIToken(appToken) {
		t.Fatal("revoked token accepted")
	}
	if err := s.RevokeAPIApp(apps[0].ID); err == nil {
		t.Fatal("double revoke succeeded")
	}

	// The plaintext never lands in the database, and the HMAC key lives beside
	// it: a database copy alone cannot verify token guesses.
	raw, err := os.ReadFile(path)
	must(t, err)
	failIf(t, strings.Contains(string(raw), appToken), "plaintext token stored in database")
	keyInfo, err := os.Stat(path + ".api-key")
	failIfFmt(t, err != nil || keyInfo.Mode().Perm() != 0600, "api key file: %v %v", keyInfo, err)
}

func TestAPIAuthRequestTTLimitsPendingSweep(t *testing.T) {
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer s.Close()
	s.apiAuth.mu.Lock()
	s.apiAuth.nextID = 1
	s.apiAuth.mu.Unlock()
	for i := 0; i < apiMaxPendingRequests; i++ {
		if _, err := s.CreateAPIAuthRequest("app", "ua", "10.0.0."+string(rune('A'+i))); err != nil {
			t.Fatalf("fill: %v", err)
		}
	}
	if _, err := s.CreateAPIAuthRequest("overflow", "ua", "10.9.9.9"); err != ErrTooManyAPIAuthRequests {
		t.Fatalf("total cap: %v", err)
	}
	// Expiring all pending frees the budget again.
	s.apiAuth.mu.Lock()
	for _, request := range s.apiAuth.pending {
		request.expiresAt = time.Now().Add(-time.Second)
	}
	s.apiAuth.mu.Unlock()
	if _, err := s.CreateAPIAuthRequest("after-sweep", "ua", "10.9.9.9"); err != nil {
		t.Fatalf("after sweep: %v", err)
	}
}

func TestAPIAuthTokenExpiry(t *testing.T) {
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer s.Close()
	token, err := s.CreateAPIAuthRequest("short-lived", "ua", "10.0.0.4")
	must(t, err)
	pending := s.ListAPIAuthRequests()
	must(t, s.ApproveAPIAuthRequest(pending[0].ID, time.Hour))
	_, appToken := s.APIAuthRequestStatus(token)
	if !s.AuthenticateAPIToken(appToken) {
		t.Fatal("unexpired token rejected")
	}
	apps, err := s.ListAPIApps()
	must(t, err)
	failIf(t, len(apps) != 1 || apps[0].ExpiresAt == nil, "expiry not surfaced", apps)
	// Force the row past its expiry; auth must fail and drop the row.
	must(t, s.stateDB.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE api_tokens SET expires_at = ?", time.Now().Add(-time.Second).UnixNano())
		return err
	}))
	if s.AuthenticateAPIToken(appToken) {
		t.Fatal("expired token accepted")
	}
	apps, err = s.ListAPIApps()
	must(t, err)
	failIf(t, len(apps) != 0, "expired token not removed", apps)
	// Never-expiring tokens keep working.
	token2, err := s.CreateAPIAuthRequest("forever", "ua", "10.0.0.5")
	must(t, err)
	pending = s.ListAPIAuthRequests()
	must(t, s.ApproveAPIAuthRequest(pending[0].ID, 0))
	_, appToken2 := s.APIAuthRequestStatus(token2)
	if !s.AuthenticateAPIToken(appToken2) {
		t.Fatal("never-expiring token rejected")
	}
	apps, err = s.ListAPIApps()
	must(t, err)
	failIf(t, len(apps) != 1 || apps[0].ExpiresAt != nil, "unexpected expiry", apps)
}

func TestAPIRebindHook(t *testing.T) {
	s, err := New(testConfig(t), filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer s.Close()
	calls := make([]string, 0, 2)
	s.SetAPIListener(func(addr string) error {
		calls = append(calls, addr)
		return nil
	})
	cfg := testConfig(t)
	cfg.API.ListenAddr = "127.0.0.1:59999"
	must(t, s.UpdateConfig(cfg))
	failIf(t, len(calls) != 1 || calls[0] != "127.0.0.1:59999", "rebind hook not called", calls)
	// Unchanged address does not rebind.
	must(t, s.UpdateConfig(cfg))
	failIf(t, len(calls) != 1, "unchanged address rebound", calls)
	// Disabling stops the listener.
	cfg.API.ListenAddr = ""
	must(t, s.UpdateConfig(cfg))
	failIf(t, len(calls) != 2 || calls[1] != "", "stop not called", calls)
}
