package ipc

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestScanCancellationInvalidAndStaleIDs(t *testing.T) {
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "test", "password"
	s, err := daemon.New(cfg, filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	handler := NewServer(s, "").handler()
	for _, tc := range []struct {
		body   string
		status int
	}{
		{"", 400}, {"{}", 400}, {`{"id":0}`, 400}, {`{"id":-1}`, 400},
		{`{"id":"1"}`, 400}, {`{"id":1} {}`, 400}, {`{"id":18446744073709551616}`, 400}, {`{"id":1}`, 409},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/v1/shares/rescan/cancel", strings.NewReader(tc.body)))
		if response.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, response.Code, response.Body.String())
		}
	}
}
