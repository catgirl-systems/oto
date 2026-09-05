package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestUploadActionRoutes(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "test", "test"
	svc, err := daemon.New(cfg, filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	handler := NewServer(svc, "").handler()
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"action":"clear","all":true}`, 200},
		{`{"action":"clear","states":["completed","cancelled","failed"]}`, 200},
		{`{"action":"cancel","usernames":["missing"]}`, 200},
		{`{"action":"cancel","ids":["upload:missing"]}`, 200},
		{`{"action":"retry","ids":["upload:missing"]}`, 503},
		{`{"action":"clear"}`, 400},
		{`{"action":"clear","all":true,"ids":["upload:missing"]}`, 400},
		{`{"action":"clear","states":["running"]}`, 400},
		{`{"action":"clear","ids":["d-1","upload:missing"]}`, 400},
		{`{"action":"cancel","all":true}`, 400},
		{`{"action":"unknown","all":true}`, 400},
		{`{`, 400},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/uploads/actions", strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/uploads/actions", nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/transfers/upload:missing", strings.NewReader(`{"action":"clear"}`)))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	// Exercise the actual Unix-socket client codec, not just handler validation.
	socket := filepath.Join(t.TempDir(), "ipc.sock")
	server := NewServer(svc, socket)
	if _, err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.http = &http.Server{Handler: server.handler()}
	go server.http.Serve(server.listener)
	client := NewClient(socket)
	result, err := client.UploadAction(ctx, daemon.UploadActionRequest{Action: "clear", All: true})
	if err != nil || result.Changed != 0 {
		t.Fatalf("client round trip %+v %v", result, err)
	}
	result, err = client.UploadAction(ctx, daemon.UploadActionRequest{Action: "cancel", IDs: []string{"upload:missing"}})
	if err != nil || len(result.Errors) != 1 {
		t.Fatalf("batch item error %+v %v", result, err)
	}
	// The result's counters and errors are stable JSON fields.
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), `"changed":0`) {
		t.Fatal(string(data))
	}
}
