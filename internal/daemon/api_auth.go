package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/storage"
	storageDB "github.com/catgirl-systems/oto/internal/storage/db"
)

// API pairing: an unauthenticated client creates an auth request, the user
// approves it in the TUI, and the client polls for its one-time app token.
// Tokens are opaque 256-bit values; only an HMAC-SHA512 digest keyed by a
// per-install secret (kept beside the state database, not inside it) is stored.

const (
	APIAuthRequestTTL     = 10 * time.Minute
	apiMaxPendingRequests = 16
	apiMaxPendingPerIP    = 5
	apiNameMaxRunes       = 64
	// ponytail: last_used_at is stamped at most once per minute per token; a
	// per-request stamp would turn 1 Hz status polling into a write storm.
	apiTouchInterval = time.Minute
)

var (
	ErrTooManyAPIAuthRequests = errors.New("daemon: too many pending API auth requests")
	ErrAPIAuthRequestNotFound = errors.New("daemon: API auth request not found")
	ErrAPIAppNotFound         = errors.New("daemon: API app not found")
)

// APIAuthRequest is a pending pairing request shown in the TUI.
type APIAuthRequest struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UserAgent string    `json:"user_agent"`
	SourceIP  string    `json:"source_ip"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// APIApp is an approved, token-holding app.
type APIApp struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	UserAgent  string     `json:"user_agent"`
	SourceIP   string     `json:"source_ip"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

type apiAuthState struct {
	mu        sync.Mutex
	key       []byte
	pending   map[string]*apiAuth // keyed by request token hash
	delivered map[string]bool     // request IDs whose app token was handed out
	lastTouch map[string]time.Time
	rebind    func(addr string) error
	nextID    uint64
}

type apiAuth struct {
	id         string
	name       string
	userAgent  string
	sourceIP   string
	createdAt  time.Time
	expiresAt  time.Time
	status     string // pending, approved, rejected
	appToken   string // plaintext until first poll after approval
	appTokenID string
}

func newAPIAuthState() apiAuthState {
	return apiAuthState{pending: make(map[string]*apiAuth), delivered: make(map[string]bool), lastTouch: make(map[string]time.Time)}
}

// initAPIAuth loads or creates the per-install HMAC key. Called lazily so an
// unused API never touches the filesystem.
func (s *Service) initAPIAuth() error {
	s.apiAuth.mu.Lock()
	defer s.apiAuth.mu.Unlock()
	if s.apiAuth.key != nil {
		return nil
	}
	key, err := loadOrCreateAPIKey(s.journalPath + ".api-key")
	if err != nil {
		return err
	}
	s.apiAuth.key = key
	return nil
}

func loadOrCreateAPIKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("daemon: API key path unavailable")
	}
	if key, err := os.ReadFile(path); err == nil && len(key) == 32 {
		return key, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, err
	}
	return key, nil
}

// apiHashLocked digests a token under the install key; initAPIAuth ran first.
func (s *Service) apiHashLocked(token string) string {
	mac := hmac.New(sha512.New, s.apiAuth.key)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// newAPITokenLocked mints an opaque token and its stored digest.
func (s *Service) newAPITokenLocked() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	token = "oto" + base64.RawURLEncoding.EncodeToString(raw)
	return token, s.apiHashLocked(token), nil
}

func (s *Service) sweepAPIAuthLocked(now time.Time) {
	for hash, request := range s.apiAuth.pending {
		if now.After(request.expiresAt) {
			delete(s.apiAuth.pending, hash)
			delete(s.apiAuth.delivered, request.id)
		}
	}
}

// CreateAPIAuthRequest starts a pairing flow and returns the poll token.
func (s *Service) CreateAPIAuthRequest(name, userAgent, sourceIP string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > apiNameMaxRunes {
		return "", errors.New("daemon: invalid API app name")
	}
	if err := s.initAPIAuth(); err != nil {
		return "", err
	}
	s.apiAuth.mu.Lock()
	defer s.apiAuth.mu.Unlock()
	now := time.Now()
	s.sweepAPIAuthLocked(now)
	pending, perIP := 0, 0
	for _, request := range s.apiAuth.pending {
		if request.status == "pending" {
			pending++
			if request.sourceIP == sourceIP {
				perIP++
			}
		}
	}
	if pending >= apiMaxPendingRequests || perIP >= apiMaxPendingPerIP {
		return "", ErrTooManyAPIAuthRequests
	}
	token, hash, err := s.newAPITokenLocked()
	if err != nil {
		return "", err
	}
	s.apiAuth.nextID++
	request := &apiAuth{id: fmt.Sprintf("a-%d-%d", now.Unix(), s.apiAuth.nextID), name: name, userAgent: userAgent, sourceIP: sourceIP, createdAt: now, expiresAt: now.Add(APIAuthRequestTTL), status: "pending"}
	s.apiAuth.pending[hash] = request
	s.event(slog.LevelInfo, "api_auth_request_created", nil, slog.String("name", name), slog.String("source_ip", sourceIP))
	return token, nil
}

// APIAuthRequestStatus polls a pairing request by its token.
func (s *Service) APIAuthRequestStatus(token string) (status, appToken string) {
	if err := s.initAPIAuth(); err != nil {
		return "unknown", ""
	}
	s.apiAuth.mu.Lock()
	defer s.apiAuth.mu.Unlock()
	request, ok := s.apiAuth.pending[s.apiHashLocked(token)]
	if !ok || time.Now().After(request.expiresAt) {
		return "unknown", ""
	}
	if request.status == "approved" && !s.apiAuth.delivered[request.id] {
		s.apiAuth.delivered[request.id] = true
		return "approved", request.appToken
	}
	return request.status, ""
}

// ListAPIAuthRequests returns pending pairing requests for the TUI.
func (s *Service) ListAPIAuthRequests() []APIAuthRequest {
	s.apiAuth.mu.Lock()
	defer s.apiAuth.mu.Unlock()
	now := time.Now()
	s.sweepAPIAuthLocked(now)
	out := make([]APIAuthRequest, 0, len(s.apiAuth.pending))
	for _, request := range s.apiAuth.pending {
		if request.status != "pending" {
			continue
		}
		out = append(out, APIAuthRequest{ID: request.id, Name: request.name, UserAgent: request.userAgent, SourceIP: request.sourceIP, CreatedAt: request.createdAt, ExpiresAt: request.expiresAt})
	}
	return out
}

// ApproveAPIAuthRequest mints and persists the app token for a pending
// request. A non-positive ttl means the token never expires.
func (s *Service) ApproveAPIAuthRequest(id string, ttl time.Duration) error {
	if err := s.initAPIAuth(); err != nil {
		return err
	}
	s.apiAuth.mu.Lock()
	defer s.apiAuth.mu.Unlock()
	request := s.findPendingLocked(id)
	if request == nil || request.status != "pending" {
		return ErrAPIAuthRequestNotFound
	}
	token, hash, err := s.newAPITokenLocked()
	if err != nil {
		return err
	}
	createdAt := time.Now()
	s.apiAuth.nextID++
	appID := fmt.Sprintf("app-%d-%d", createdAt.Unix(), s.apiAuth.nextID)
	var expiresAt *int64
	if ttl > 0 {
		at := createdAt.Add(ttl).UnixNano()
		expiresAt = &at
	}
	if err := s.stateDB.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return s.stateDB.Queries().WithTx(tx).InsertAPIToken(context.Background(), storageDB.InsertAPITokenParams{
			ID: appID, Name: request.name, TokenHash: hash, UserAgent: request.userAgent, SourceIp: request.sourceIP, CreatedAt: createdAt.UnixNano(), ExpiresAt: expiresAt,
		})
	}); err != nil {
		return err
	}
	request.status, request.appToken, request.appTokenID = "approved", token, appID
	s.event(slog.LevelInfo, "api_auth_request_approved", nil, slog.String("name", request.name))
	return nil
}

// RejectAPIAuthRequest refuses a pending request; the poller sees "rejected".
func (s *Service) RejectAPIAuthRequest(id string) error {
	s.apiAuth.mu.Lock()
	defer s.apiAuth.mu.Unlock()
	request := s.findPendingLocked(id)
	if request == nil || request.status != "pending" {
		return ErrAPIAuthRequestNotFound
	}
	request.status = "rejected"
	s.event(slog.LevelInfo, "api_auth_request_rejected", nil, slog.String("name", request.name))
	return nil
}

func (s *Service) findPendingLocked(id string) *apiAuth {
	for _, request := range s.apiAuth.pending {
		if request.id == id {
			return request
		}
	}
	return nil
}

// ListAPIApps returns approved apps; token digests never leave the daemon.
func (s *Service) ListAPIApps() ([]APIApp, error) {
	if s.stateDB == nil {
		return nil, errors.New("daemon: state database is not open")
	}
	var apps []APIApp
	err := s.stateDB.ReadSnapshot(context.Background(), func(snapshot *storage.ReadTx) error {
		rows, err := snapshot.Queries().ListAPITokens(context.Background())
		if err != nil {
			return err
		}
		apps = make([]APIApp, 0, len(rows))
		for _, row := range rows {
			app := APIApp{ID: row.ID, Name: row.Name, UserAgent: row.UserAgent, SourceIP: row.SourceIp, CreatedAt: unixTime(row.CreatedAt)}
			if row.LastUsedAt != nil {
				used := unixTime(*row.LastUsedAt)
				app.LastUsedAt = &used
			}
			if row.ExpiresAt != nil {
				expires := unixTime(*row.ExpiresAt)
				app.ExpiresAt = &expires
			}
			apps = append(apps, app)
		}
		return nil
	})
	return apps, err
}

// RevokeAPIApp deletes an app token, immediately invalidating it.
func (s *Service) RevokeAPIApp(id string) error {
	if s.stateDB == nil {
		return errors.New("daemon: state database is not open")
	}
	return s.stateDB.WriteTx(context.Background(), func(tx *sql.Tx) error {
		result, err := tx.Exec("DELETE FROM api_tokens WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return ErrAPIAppNotFound
		}
		return nil
	})
}

// AuthenticateAPIToken reports whether the token matches an approved,
// unexpired app. Expired rows are removed on detection.
func (s *Service) AuthenticateAPIToken(token string) bool {
	if err := s.initAPIAuth(); err != nil {
		return false
	}
	s.apiAuth.mu.Lock()
	hash := s.apiHashLocked(token)
	s.apiAuth.mu.Unlock()
	if s.stateDB == nil {
		return false
	}
	var row storageDB.ApiToken
	found := s.stateDB.ReadSnapshot(context.Background(), func(snapshot *storage.ReadTx) error {
		var err error
		row, err = snapshot.Queries().GetAPITokenByHash(context.Background(), hash)
		return err
	}) == nil
	if !found {
		return false
	}
	if row.ExpiresAt != nil && time.Now().UnixNano() >= *row.ExpiresAt {
		_ = s.stateDB.WriteTx(context.Background(), func(tx *sql.Tx) error {
			return s.stateDB.Queries().WithTx(tx).DeleteAPIToken(context.Background(), row.ID)
		})
		return false
	}
	s.apiAuth.mu.Lock()
	last, touched := s.apiAuth.lastTouch[row.ID]
	stamp := !touched || time.Since(last) >= apiTouchInterval
	if stamp {
		s.apiAuth.lastTouch[row.ID] = time.Now()
	}
	s.apiAuth.mu.Unlock()
	if stamp {
		used := time.Now().UnixNano()
		_ = s.stateDB.WriteTx(context.Background(), func(tx *sql.Tx) error {
			return s.stateDB.Queries().WithTx(tx).TouchAPIToken(context.Background(), storageDB.TouchAPITokenParams{LastUsedAt: &used, ID: row.ID})
		})
	}
	return true
}

// SetAPIListener installs the rebind hook used by UpdateConfig.
func (s *Service) SetAPIListener(rebind func(addr string) error) {
	s.apiAuth.mu.Lock()
	s.apiAuth.rebind = rebind
	s.apiAuth.mu.Unlock()
}

// RebindAPIListener applies a new API listen address ("" stops the listener).
func (s *Service) RebindAPIListener(addr string) error {
	s.apiAuth.mu.Lock()
	rebind := s.apiAuth.rebind
	s.apiAuth.mu.Unlock()
	if rebind == nil {
		if addr == "" {
			return nil
		}
		return errors.New("daemon: API listener is not running")
	}
	return rebind(addr)
}

// PendingAPIAuthSummary feeds the TUI notice from the state poll.
func (s *Service) PendingAPIAuthSummary() (count int, name string) {
	s.apiAuth.mu.Lock()
	defer s.apiAuth.mu.Unlock()
	now := time.Now()
	s.sweepAPIAuthLocked(now)
	for _, request := range s.apiAuth.pending {
		if request.status != "pending" {
			continue
		}
		if count == 0 {
			name = request.name
		}
		count++
	}
	return count, name
}
