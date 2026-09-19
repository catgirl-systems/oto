package ipc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/danielgtaylor/huma/v2"
)

// The TCP API server: the same operations as the Unix socket, minus
// local-only routes, with Bearer-token auth on everything not public.

type authRequestInput struct {
	Name string `json:"name" maxLength:"64" doc:"Display name for the connecting app"`
}

type authRequestOutput struct {
	Status int
	Body   authTokenBody
}

// authTokenBody is the pairing-request response.
type authTokenBody struct {
	RequestToken string `json:"request_token" doc:"Opaque token to poll the pairing request"`
	ExpiresIn    int    `json:"expires_in" doc:"Seconds until the request expires"`
}

type apiAuthStatusBody struct {
	Status   string `json:"status" enum:"pending,approved,rejected" doc:"Pairing status"`
	AppToken string `json:"app_token,omitempty" doc:"App token, delivered exactly once on the first poll after approval"`
}

type okOutput struct {
	Body okBody
}

// okBody is the generic success response.
type okBody struct {
	OK bool `json:"ok"`
}

func (s *Server) registerAPIAuthRoutes() {
	route(s, scopePublic, huma.Operation{
		OperationID: "create-auth-request", Method: http.MethodPost, Path: "/v1/auth/requests",
		Summary:       "Request app pairing; returns an opaque poll token",
		Description:   "Creates a pairing request a human must approve in the oto TUI (Settings → API). Poll GET /v1/auth/requests/{token} until it is approved or rejected.",
		DefaultStatus: http.StatusAccepted,
		Errors:        []int{400, 429},
	}, func(ctx context.Context, input *struct {
		Body      authRequestInput
		UserAgent string `header:"User-Agent" doc:"Identifies the connecting app"`
	}) (*authRequestOutput, error) {
		token, err := s.service.CreateAPIAuthRequest(input.Body.Name, input.UserAgent, remoteHost(ctx.Value(remoteAddrKey{}).(string)))
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, daemon.ErrTooManyAPIAuthRequests) {
				status = http.StatusTooManyRequests
			}
			return nil, errStatus(status, err)
		}
		out := &authRequestOutput{Status: http.StatusAccepted}
		out.Body.RequestToken = token
		out.Body.ExpiresIn = int(daemon.APIAuthRequestTTL.Seconds())
		return out, nil
	})
	route(s, scopePublic, huma.Operation{
		OperationID: "get-auth-request", Method: http.MethodGet, Path: "/v1/auth/requests/{token}",
		Summary: "Poll a pairing request; delivers the app token once when approved",
		Errors:  []int{404},
	}, func(ctx context.Context, input *struct {
		Token string `path:"token"`
	}) (*struct {
		Body apiAuthStatusBody
	}, error) {
		status, appToken := s.service.APIAuthRequestStatus(input.Token)
		if status == "unknown" {
			return nil, errStatus(http.StatusNotFound, errors.New("ipc: unknown or expired auth request"))
		}
		return &struct {
			Body apiAuthStatusBody
		}{apiAuthStatusBody{Status: status, AppToken: appToken}}, nil
	})
	route(s, scopeLocal, huma.Operation{
		OperationID: "list-auth-requests", Method: http.MethodGet, Path: "/v1/auth/requests",
		Summary: "List pending pairing requests",
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []daemon.APIAuthRequest
	}, error) {
		return &struct {
			Body []daemon.APIAuthRequest
		}{s.service.ListAPIAuthRequests()}, nil
	})
	route(s, scopeLocal, huma.Operation{
		OperationID: "approve-auth-request", Method: http.MethodPost, Path: "/v1/auth/requests/{id}/approve",
		Summary:     "Approve a pairing request",
		Description: "Approves the pairing request and mints an app token; the poller receives it on its next poll.",
		Errors:      []int{400, 404},
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id"`
		Body *struct {
			ExpiresInDays int `json:"expires_in_days" doc:"Days until the app token expires; 0 or omitted means it never expires"`
		}
	}) (*okOutput, error) {
		if input.Body != nil && (input.Body.ExpiresInDays < 0 || input.Body.ExpiresInDays > 3650) {
			return nil, errStatus(http.StatusBadRequest, errors.New("ipc: expires_in_days must be 0 (never) through 3650"))
		}
		ttl := time.Duration(0)
		if input.Body != nil && input.Body.ExpiresInDays > 0 {
			ttl = time.Duration(input.Body.ExpiresInDays) * 24 * time.Hour
		}
		if err := s.service.ApproveAPIAuthRequest(input.ID, ttl); err != nil {
			return nil, errStatus(http.StatusNotFound, err)
		}
		return &okOutput{}, nil
	})
	route(s, scopeLocal, huma.Operation{
		OperationID: "reject-auth-request", Method: http.MethodPost, Path: "/v1/auth/requests/{id}/reject",
		Summary: "Reject a pairing request",
		Errors:  []int{404},
	}, func(ctx context.Context, input *struct {
		ID string `path:"id"`
	}) (*okOutput, error) {
		if err := s.service.RejectAPIAuthRequest(input.ID); err != nil {
			return nil, errStatus(http.StatusNotFound, err)
		}
		return &okOutput{}, nil
	})
	route(s, scopeLocal, huma.Operation{
		OperationID: "list-apps", Method: http.MethodGet, Path: "/v1/apps",
		Summary: "List connected apps",
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []daemon.APIApp
	}, error) {
		apps, err := s.service.ListAPIApps()
		if err != nil {
			return nil, errStatus(http.StatusInternalServerError, err)
		}
		return &struct {
			Body []daemon.APIApp
		}{apps}, nil
	})
	route(s, scopeLocal, huma.Operation{
		OperationID: "revoke-app", Method: http.MethodDelete, Path: "/v1/apps/{id}",
		Summary: "Revoke a connected app",
		Errors:  []int{404},
	}, func(ctx context.Context, input *struct {
		ID string `path:"id"`
	}) (*okOutput, error) {
		if err := s.service.RevokeAPIApp(input.ID); err != nil {
			return nil, errStatus(http.StatusNotFound, err)
		}
		return &okOutput{}, nil
	})
}

// tcpAuthMux wraps the TCP mux with the Bearer-token check for routes
// registered outside huma (the API index).
func (s *Server) tcpAuthMux(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !s.service.AuthenticateAPIToken(strings.TrimSpace(token)) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="oto"`)
			writeErr(w, http.StatusUnauthorized, errors.New("ipc: missing or invalid bearer token"))
			return
		}
		handler.ServeHTTP(w, r)
	})
}

// APIServer serves the TCP HTTP API. Constructed even when disabled so a later
// settings change can enable it through Rebind.
type APIServer struct {
	server   *Server
	addr     string
	mu       sync.Mutex
	ctx      context.Context
	listener net.Listener
	http     *http.Server
}

func NewAPIServer(server *Server, addr string) *APIServer {
	return &APIServer{server: server, addr: addr}
}

// Serve blocks until ctx is done, keeping the listener bound to the current
// configuration; Rebind swaps listeners underneath it.
func (a *APIServer) Serve(ctx context.Context) error {
	a.mu.Lock()
	a.ctx = ctx
	if a.addr != "" {
		if err := a.startLocked(); err != nil {
			a.mu.Unlock()
			return err
		}
	}
	a.mu.Unlock()
	<-ctx.Done()
	a.mu.Lock()
	a.stopLocked()
	a.mu.Unlock()
	return nil
}

func (a *APIServer) startLocked() error {
	listener, err := net.Listen("tcp", a.addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: a.server.tcpHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	a.listener, a.http = listener, server
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (a *APIServer) stopLocked() {
	if a.http != nil {
		_ = a.http.Shutdown(context.Background())
		a.http, a.listener = nil, nil
	}
}

// Rebind applies a new listen address. An empty address stops listening. The
// old listener is always released first: overlapping changes such as
// 127.0.0.1:P to 0.0.0.0:P cannot bind while the old address is held. A failed
// bind restores the previous listener when possible.
func (a *APIServer) Rebind(addr string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if addr == a.addr {
		return nil
	}
	previous := a.addr
	a.stopLocked()
	a.addr = addr
	if addr == "" {
		return nil
	}
	if err := a.startLocked(); err != nil {
		a.addr = previous
		if previous != "" {
			if restoreErr := a.startLocked(); restoreErr != nil {
				return errors.Join(err, restoreErr)
			}
		}
		return err
	}
	return nil
}

func (a *APIServer) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.listener != nil {
		_ = a.listener.Close()
	}
	a.stopLocked()
	return nil
}

// Addr reports the listening address (nil when the API is disabled).
func (a *APIServer) Addr() net.Addr {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.listener == nil {
		return nil
	}
	return a.listener.Addr()
}
