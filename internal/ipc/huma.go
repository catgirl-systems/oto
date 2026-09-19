package ipc

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// huma powers the IPC's typed operations and OpenAPI generation. The legacy
// {"error": "..."} wire format is preserved by replacing huma.NewError, so
// the TUI client, e2e script, and every existing test keep working unchanged.

func init() {
	huma.NewError = func(status int, msg string, _ ...error) huma.StatusError {
		return &apiError{status: status, Message: msg}
	}
}

// apiError keeps oto's historical error body on huma's error path. Message is
// exported so the generated ApiError schema documents the {"error": string}
// wire format; the status stays unexported and out of the schema.
type apiError struct {
	status  int
	Message string `json:"error"`
}

func (e *apiError) Error() string  { return e.Message }
func (e *apiError) GetStatus() int { return e.status }

// errStatus maps an error onto a huma status error with the legacy body.
func errStatus(status int, err error) huma.StatusError {
	return huma.NewError(status, err.Error())
}

// newAPIs builds the Unix-socket and TCP huma APIs over their muxes.
func (s *Server) newAPIs() {
	config := huma.DefaultConfig("oto API", "1")
	// Keep response bodies exactly as the handlers produce them: no $schema
	// injection, no links. The default transformer is attached via a create
	// hook, so clearing Transformers alone is not enough.
	config.CreateHooks = nil
	config.Transformers = nil
	config.Info.Description = "Soulseek daemon API. Pair an app via POST /v1/auth/requests, then send the delivered app token as an Authorization: Bearer header. Plain JSON over HTTP; put a reverse proxy in front for TLS."
	s.socketAPI = humago.New(s.socketMux, config)
	s.socketAPI.UseMiddleware(s.remoteAddr)

	tcpConfig := huma.DefaultConfig("oto API", "1")
	tcpConfig.CreateHooks = nil
	tcpConfig.Transformers = nil
	tcpConfig.Info.Description = config.Info.Description
	tcpConfig.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearerAuth": {Type: "http", Scheme: "bearer"},
	}
	s.tcpAPI = humago.New(s.tcpMux, tcpConfig)
	s.tcpAPI.UseMiddleware(s.remoteAddr)
}

// remoteAddrKey carries the connection's remote address to handlers, which
// only receive a context.Context.
type remoteAddrKey struct{}

func (s *Server) remoteAddr(ctx huma.Context, next func(huma.Context)) {
	next(huma.WithContext(ctx, context.WithValue(ctx.Context(), remoteAddrKey{}, ctx.RemoteAddr())))
}

// route registers an operation on the Unix socket (all scopes) and the TCP API
// (public + authed scopes; authed routes require a Bearer app token there).
func route[I any, O any](s *Server, scope routeScope, op huma.Operation, fn func(context.Context, *I) (*O, error)) {
	huma.Register(s.socketAPI, op, fn)
	if scope == scopeLocal {
		return
	}
	if scope == scopeAuthed {
		op.Security = []map[string][]string{{"bearerAuth": {}}}
		op.Middlewares = append(op.Middlewares, s.requireAuth)
	}
	huma.Register(s.tcpAPI, op, fn)
}

// requireAuth is the huma middleware enforcing the Bearer app token on the
// TCP API.
func (s *Server) requireAuth(ctx huma.Context, next func(huma.Context)) {
	token, ok := strings.CutPrefix(ctx.Header("Authorization"), "Bearer ")
	if !ok || !s.service.AuthenticateAPIToken(strings.TrimSpace(token)) {
		ctx.SetHeader("WWW-Authenticate", `Bearer realm="oto"`)
		huma.WriteErr(s.tcpAPI, ctx, http.StatusUnauthorized, "ipc: missing or invalid bearer token")
		return
	}
	next(ctx)
}

// remoteHost strips the port from RemoteAddr. Proxies can hide or spoof the
// real client, so the value is display-only.
func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
