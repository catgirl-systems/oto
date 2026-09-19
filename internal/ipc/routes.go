package ipc

import (
	"net/http"
)

// routeScope decides how a route behaves on the TCP API server. The Unix
// socket serves every route regardless of scope.
type routeScope uint8

const (
	// scopeAuthed requires a Bearer app token on the TCP server.
	scopeAuthed routeScope = iota
	// scopePublic is reachable without authentication on the TCP server.
	scopePublic
	// scopeLocal never leaves the Unix socket: it manages API authorization or
	// reconfigures the daemon, so a paired app must not reach it.
	scopeLocal
)

func (s *Server) registerRoutes() {
	s.registerCoreRoutes()
	s.registerStatsRoutes()
	s.registerAPIAuthRoutes()
	s.registerCommunityRoutes()
	s.registerCommunityChatRoutes()
	s.registerCommunityRoomRoutes()
	s.registerCommunityBuddyRoutes()
	s.registerCommunityInterestRoutes()
	s.registerCommunityRuleRoutes()
	s.registerCommunityAliasRoutes()
	s.registerCommunityProfileRoutes()
	s.registerCommunityDiscoveryRoutes()
	s.registerCommunityActivityRoute()
	s.registerCommunityAwayRoutes()
	s.registerCommunityBroadcastRoutes()
	s.registerCommunityTextRoutes()
	s.registerCommunityCompletionRoute()
	s.registerAccountPrivilegeRoutes()
	s.registerCommandRoute()
	s.registerReceivingRoutes()
	s.registerShareAccessRoute()
	s.registerSharedSendRoutes()
	// The API index is public on both servers.
	s.socketMux.HandleFunc("GET /{$}", s.serveAPIIndex)
	s.tcpMux.HandleFunc("GET /{$}", s.serveAPIIndex)
}

func (s *Server) serveAPIIndex(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": "oto",
		"api":  "JSON over HTTP",
		"docs": "/openapi.json",
		"auth": `POST /v1/auth/requests with {"name": "your app"}, poll the returned request token until the user approves it, then use the delivered app token as a Bearer token`,
	})
}
