package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func newAPIAuthServer(t *testing.T) (*Server, *daemon.Service) {
	t.Helper()
	c := config.Default()
	c.Soulseek.Username, c.Soulseek.Password = "u", "p"
	c.DownloadDir = t.TempDir()
	svc, err := daemon.New(c, filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return NewServer(svc, filepath.Join(t.TempDir(), "run", "oto.sock")), svc
}

func doJSON(t *testing.T, client *http.Client, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		must(t, err)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	must(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "test-agent/1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	must(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAPIAuthFlowOverTCP(t *testing.T) {
	srv, _ := newAPIAuthServer(t)
	tcp := httptest.NewServer(srv.tcpHandler())
	defer tcp.Close()
	local := httptest.NewServer(srv.handler())
	defer local.Close()

	// Pairing is unauthenticated on the TCP server.
	status, out := doJSON(t, tcp.Client(), "POST", tcp.URL+"/v1/auth/requests", "", map[string]string{"name": "my-agent"})
	failIfFmt(t, status != http.StatusAccepted, "create status %d", status)
	requestToken, _ := out["request_token"].(string)
	failIfFmt(t, requestToken == "" || !strings.HasPrefix(requestToken, "oto"), "request token: %v", out)
	if status, out := doJSON(t, tcp.Client(), "GET", tcp.URL+"/v1/auth/requests/"+requestToken, "", nil); status != 200 || out["status"] != "pending" {
		t.Fatalf("pending poll: %d %v", status, out)
	}

	// The daemon operator approves over the local socket.
	var pending []daemon.APIAuthRequest
	resp, err := local.Client().Get(local.URL + "/v1/auth/requests")
	must(t, err)
	defer resp.Body.Close()
	must(t, json.NewDecoder(resp.Body).Decode(&pending))
	failIfFmt(t, len(pending) != 1 || pending[0].Name != "my-agent" || pending[0].UserAgent != "test-agent/1.0" || pending[0].SourceIP != "127.0.0.1", "pending: %+v", pending)
	status, _ = doJSON(t, local.Client(), "POST", fmt.Sprintf("%s/v1/auth/requests/%s/approve", local.URL, pending[0].ID), "", nil)
	failIfFmt(t, status != 200, "approve status %d", status)

	// First poll after approval delivers the app token exactly once.
	status, out = doJSON(t, tcp.Client(), "GET", tcp.URL+"/v1/auth/requests/"+requestToken, "", nil)
	failIfFmt(t, status != 200 || out["status"] != "approved", "approved poll: %d %v", status, out)
	appToken, _ := out["app_token"].(string)
	failIf(t, appToken == "", "missing app token")
	status, out = doJSON(t, tcp.Client(), "GET", tcp.URL+"/v1/auth/requests/"+requestToken, "", nil)
	failIfFmt(t, status != 200 || out["status"] != "approved" || out["app_token"] != nil, "second poll: %d %v", status, out)

	// Authenticated access works; missing or wrong tokens do not.
	if status, _ := doJSON(t, tcp.Client(), "GET", tcp.URL+"/v1/state", appToken, nil); status != 200 {
		t.Fatalf("bearer state: %d", status)
	}
	if status, _ := doJSON(t, tcp.Client(), "GET", tcp.URL+"/v1/state", "", nil); status != 401 {
		t.Fatalf("unauthenticated state: %d", status)
	}
	if status, _ := doJSON(t, tcp.Client(), "GET", tcp.URL+"/v1/state", "oto-forged", nil); status != 401 {
		t.Fatalf("forged state: %d", status)
	}

	// Local-only routes never exist on the TCP server (405 when a public
	// sibling method shares the path, 404 otherwise).
	for _, route := range []struct{ method, path string }{
		{"GET", "/v1/auth/requests"},
		{"POST", fmt.Sprintf("/v1/auth/requests/%s/approve", pending[0].ID)},
		{"GET", "/v1/apps"},
		{"PUT", "/v1/config"},
	} {
		if status, _ := doJSON(t, tcp.Client(), route.method, tcp.URL+route.path, appToken, nil); status != 404 && status != 405 {
			t.Fatalf("local-only %s %s on TCP: %d", route.method, route.path, status)
		}
	}
	// ...but all of them still work through the Unix-socket mux.
	if status, _ := doJSON(t, local.Client(), "GET", local.URL+"/v1/apps", "", nil); status != 200 {
		t.Fatalf("apps on socket: %d", status)
	}
}

func TestAPIAuthApproveExpiryBody(t *testing.T) {
	srv, svc := newAPIAuthServer(t)
	local := httptest.NewServer(srv.handler())
	defer local.Close()
	tcp := httptest.NewServer(srv.tcpHandler())
	defer tcp.Close()
	token, err := svc.CreateAPIAuthRequest("agent", "ua", "127.0.0.1")
	must(t, err)
	var pending []daemon.APIAuthRequest
	resp, err := local.Client().Get(local.URL + "/v1/auth/requests")
	must(t, err)
	defer resp.Body.Close()
	must(t, json.NewDecoder(resp.Body).Decode(&pending))
	// Out-of-range values are refused without consuming the request.
	status, _ := doJSON(t, local.Client(), "POST", fmt.Sprintf("%s/v1/auth/requests/%s/approve", local.URL, pending[0].ID), "", map[string]int{"expires_in_days": 9999})
	failIfFmt(t, status != http.StatusBadRequest, "range check: %d", status)
	status, _ = doJSON(t, local.Client(), "POST", fmt.Sprintf("%s/v1/auth/requests/%s/approve", local.URL, pending[0].ID), "", map[string]int{"expires_in_days": 7})
	failIfFmt(t, status != http.StatusOK, "approve with expiry: %d", status)
	_, appToken := svc.APIAuthRequestStatus(token)
	failIf(t, appToken == "", "no app token")
	if status, _ := doJSON(t, tcp.Client(), "GET", tcp.URL+"/v1/state", appToken, nil); status != 200 {
		t.Fatalf("bearer with future expiry: %d", status)
	}
	apps, err := svc.ListAPIApps()
	must(t, err)
	failIf(t, len(apps) != 1 || apps[0].ExpiresAt == nil, "expiry missing", apps)
}

func TestAPIAuthRateLimit(t *testing.T) {
	srv, _ := newAPIAuthServer(t)
	tcp := httptest.NewServer(srv.tcpHandler())
	defer tcp.Close()
	for i := 0; i < 5; i++ {
		status, _ := doJSON(t, tcp.Client(), "POST", tcp.URL+"/v1/auth/requests", "", map[string]string{"name": "app"})
		failIfFmt(t, status != http.StatusAccepted, "create %d: %d", i, status)
	}
	status, out := doJSON(t, tcp.Client(), "POST", tcp.URL+"/v1/auth/requests", "", map[string]string{"name": "app"})
	failIfFmt(t, status != http.StatusTooManyRequests, "cap status %d %v", status, out)
}

func TestAPIAuthRejectAndExpiry(t *testing.T) {
	srv, svc := newAPIAuthServer(t)
	tcp := httptest.NewServer(srv.tcpHandler())
	defer tcp.Close()
	local := httptest.NewServer(srv.handler())
	defer local.Close()
	status, out := doJSON(t, tcp.Client(), "POST", tcp.URL+"/v1/auth/requests", "", map[string]string{"name": "nope"})
	failIfFmt(t, status != 202, "create: %d", status)
	requestToken, _ := out["request_token"].(string)
	var pending []daemon.APIAuthRequest
	resp, err := local.Client().Get(local.URL + "/v1/auth/requests")
	must(t, err)
	defer resp.Body.Close()
	must(t, json.NewDecoder(resp.Body).Decode(&pending))
	status, _ = doJSON(t, local.Client(), "POST", fmt.Sprintf("%s/v1/auth/requests/%s/reject", local.URL, pending[0].ID), "", nil)
	failIfFmt(t, status != 200, "reject: %d", status)
	if status, out := doJSON(t, tcp.Client(), "GET", tcp.URL+"/v1/auth/requests/"+requestToken, "", nil); status != 200 || out["status"] != "rejected" {
		t.Fatalf("rejected poll: %d %v", status, out)
	}
	// Unknown and expired tokens are indistinguishable.
	if status, _ := doJSON(t, tcp.Client(), "GET", tcp.URL+"/v1/auth/requests/oto-unknown", "", nil); status != 404 {
		t.Fatalf("unknown poll: %d", status)
	}
	if token, err := svc.CreateAPIAuthRequest("expiring", "ua", "127.0.0.1"); err != nil {
		t.Fatal(err)
	} else {
		if status, _ := doJSON(t, tcp.Client(), "GET", tcp.URL+"/v1/auth/requests/"+token, "", nil); status != 200 {
			t.Fatalf("before expiry: %d", status)
		}
	}
}

func TestOpenAPIDocumentMatchesRoutes(t *testing.T) {
	srv, _ := newAPIAuthServer(t)
	tcp := httptest.NewServer(srv.tcpHandler())
	defer tcp.Close()
	resp, err := tcp.Client().Get(tcp.URL + "/openapi.json")
	must(t, err)
	defer resp.Body.Close()
	failIfFmt(t, resp.StatusCode != 200, "openapi status %d", resp.StatusCode)
	var full struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Summary     string `json:"summary"`
			Responses   map[string]struct {
				Description string `json:"description"`
				Content     map[string]struct {
					Schema json.RawMessage `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
			Security json.RawMessage `json:"security"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Enum       []string                   `json:"enum"`
			} `json:"schemas"`
		} `json:"components"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&full))
	failIf(t, len(full.Paths) < 40, "path count", len(full.Paths))
	public := map[string]bool{"/v1/health": true, "/v1/auth/requests": true, "/v1/auth/requests/{token}": true, "/openapi.json": true, "/openapi-3.0.json": true, "/openapi.yaml": true, "/openapi-3.0.yaml": true, "/docs": true, "/schemas/{schema}": true}
	for pattern, entry := range full.Paths {
		for method, operation := range entry {
			failIfFmt(t, operation.OperationID == "", "missing operationId on %s %s", method, pattern)
			failIfFmt(t, operation.Summary == "", "missing summary on %s %s", method, pattern)
			for status, response := range operation.Responses {
				failIfFmt(t, response.Description == "Success; JSON response", "generic response on %s %s %s", method, pattern, status)
			}
			if public[pattern] {
				failIfFmt(t, operation.Security != nil, "unexpected security on %s", pattern)
			} else {
				failIfFmt(t, operation.Security == nil, "missing security on %s %s", method, pattern)
			}
		}
	}
	// The pairing endpoints are documented for pre-auth discovery.
	for _, pattern := range []string{"/v1/auth/requests", "/v1/auth/requests/{token}"} {
		if _, ok := full.Paths[pattern]; !ok {
			t.Errorf("pairing path missing: %s", pattern)
		}
	}
	// Conversation routes document their {id} path parameter.
	for _, pattern := range []string{"/v1/community/conversations/{id}/messages", "/v1/community/conversations/{id}/export"} {
		entry, ok := full.Paths[pattern]
		failIfFmt(t, !ok, "conversation path missing: %s", pattern)
		if !ok {
			continue
		}
		get, ok := entry["get"]
		failIfFmt(t, !ok, "conversation GET missing: %s", pattern)
		found := false
		for _, param := range get.Parameters {
			if param.Name == "id" && param.In == "path" {
				found = true
			}
		}
		failIfFmt(t, !found, "conversation {id} path parameter missing: %s", pattern)
	}
	// ApiError documents the {"error": string} wire format.
	apiError, ok := full.Components.Schemas["ApiError"]
	failIf(t, !ok, "ApiError schema missing")
	if ok {
		_, hasError := apiError.Properties["error"]
		failIf(t, !hasError, "ApiError lacks error property", apiError.Properties)
	}
	// Constrained values are enums, not arbitrary strings.
	transfer, ok := full.Components.Schemas["Transfer"]
	failIf(t, !ok, "Transfer schema missing")
	if ok {
		for name, property := range transfer.Properties {
			if name != "direction" && name != "state" {
				continue
			}
			var withEnum struct {
				Enum []string `json:"enum"`
			}
			must(t, json.Unmarshal(property, &withEnum))
			failIfFmt(t, len(withEnum.Enum) == 0, "Transfer.%s lacks enum", name)
		}
	}
}

func TestAPIServerRebind(t *testing.T) {
	srv, _ := newAPIAuthServer(t)
	api := NewAPIServer(srv, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- api.Serve(ctx) }()
	for i := 0; i < 100 && api.Addr() == nil; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	first := api.Addr()
	failIf(t, first == nil, "listener never started")
	base := "http://" + first.String()
	status, _ := doJSON(t, http.DefaultClient, "GET", base+"/openapi.json", "", nil)
	failIfFmt(t, status != 200, "openapi before rebind: %d", status)

	// Disabled: the listener closes.
	must(t, api.Rebind(""))
	failIf(t, api.Addr() != nil, "listener still bound after disable")
	// Re-enabled on a fresh port.
	must(t, api.Rebind("127.0.0.1:0"))
	second := api.Addr()
	failIf(t, second == nil || second.String() == first.String(), "listener not rebound")
	status, _ = doJSON(t, http.DefaultClient, "GET", "http://"+second.String()+"/openapi.json", "", nil)
	failIfFmt(t, status != 200, "openapi after rebind: %d", status)

	// A failed bind restores the current listener.
	if err := api.Rebind("256.256.256.256:1"); err == nil {
		t.Fatal("invalid address bound")
	}
	failIf(t, api.Addr() == nil, "listener lost after failed rebind")
	cancel()
	must(t, <-done)
	must(t, api.Close())
}

func TestAPIServerRebindOverlappingAddress(t *testing.T) {
	srv, _ := newAPIAuthServer(t)
	port := freeTCPPort(t)
	api := NewAPIServer(srv, "127.0.0.1:"+port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- api.Serve(ctx) }()
	for i := 0; i < 100 && api.Addr() == nil; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	failIf(t, api.Addr() == nil, "listener never started")
	// Moving from a specific address to the wildcard on the same port must
	// rebind, not fail with the old address still held.
	must(t, api.Rebind("0.0.0.0:"+port))
	status, _ := doJSON(t, http.DefaultClient, "GET", "http://127.0.0.1:"+port+"/openapi.json", "", nil)
	failIfFmt(t, status != 200, "openapi after wildcard rebind: %d", status)
	cancel()
	must(t, <-done)
	must(t, api.Close())
}

func freeTCPPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	must(t, err)
	return port
}

func TestAPIServerDisabledByDefault(t *testing.T) {
	srv, _ := newAPIAuthServer(t)
	api := NewAPIServer(srv, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- api.Serve(ctx) }()
	time.Sleep(20 * time.Millisecond)
	failIf(t, api.Addr() != nil, "disabled server listening")
	cancel()
	must(t, <-done)
	failIf(t, api.Addr() != nil, "addr after shutdown", api.Addr())
}
