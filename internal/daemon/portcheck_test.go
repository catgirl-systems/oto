package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCheckListeningPortResponses(t *testing.T) {
	client := soulseek.NewClient(soulseek.ClientConfig{})
	for _, test := range []struct {
		name, body string
		status     int
		open       bool
		wantErr    string
	}{
		{"open", "50300/TCP OPEN", http.StatusOK, true, ""},
		{"closed", "50300/tcp closed", http.StatusOK, false, ""},
		{"non-2xx", "50300/tcp open", http.StatusBadGateway, false, "HTTP 502"},
		{"unknown", "maybe", http.StatusOK, false, "unrecognized response"},
		{"bounded", strings.Repeat("x", maxPortCheckBody+1), http.StatusOK, false, "response too large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("port") != "50300" {
					t.Errorf("port query = %q", r.URL.Query().Get("port"))
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			open, err := checkListeningPort(context.Background(), client, server.URL+"?port=%d", 50300)
			if open != test.open || (test.wantErr == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("open=%v err=%v", open, err)
				if test.name == "unknown" && err != nil && strings.Contains(err.Error(), test.body) {
					t.Fatalf("error echoed response body: %v", err)
				}
			}
		})
	}
}

func TestCheckListeningPortCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := checkListeningPort(ctx, soulseek.NewClient(soulseek.ClientConfig{}), server.URL+"?port=%d", 50300)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestServiceCheckListeningPortUsesAdvertisedPort(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := soulseek.NewClientOnConn(soulseek.ClientConfig{}, clientConn)
	defer client.Close()
	defer serverConn.Close()
	if err := client.SetAdvertisedPort(61000); err != nil {
		t.Fatal(err)
	}

	service := &Service{status: StatusConnected, client: client}
	service.portCheck = func(_ context.Context, gotClient *soulseek.Client, port uint16) (bool, error) {
		if gotClient != client || port != 61000 {
			return false, fmt.Errorf("client=%p port=%d", gotClient, port)
		}
		return true, nil
	}
	result, err := service.CheckListeningPort(context.Background())
	if err != nil || result.Port != 61000 || !result.Open {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if snapshot := service.Snapshot(); snapshot.PublicPort != 61000 {
		t.Fatalf("snapshot public port = %d", snapshot.PublicPort)
	}

	service.status = StatusStopped
	if _, err := service.CheckListeningPort(context.Background()); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("offline error = %v", err)
	}
}
