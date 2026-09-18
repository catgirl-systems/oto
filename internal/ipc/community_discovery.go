package ipc

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
)

func (s *Server) communityDiscovery(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityDiscoveryRequest
	if r.Method == http.MethodPost {
		if err := decode(w, r, &req); err != nil {
			communityError(w, err)
			return
		}
	} else {
		q := r.URL.Query()
		identity, err := communityRoomIdentityQuery(q)
		if err != nil {
			communityError(w, err)
			return
		}
		req = daemon.CommunityDiscoveryRequest{CommunityIdentity: identity, Kind: q.Get("kind"), Target: q.Get("target"), Frontend: q.Get("frontend"), Cursor: q.Get("cursor")}
		if q.Get("limit") != "" {
			req.Limit, err = strconv.Atoi(q.Get("limit"))
			if err != nil {
				communityError(w, err)
				return
			}
		}
	}
	var out daemon.CommunityDiscoveryPage
	var err error
	if r.Method == http.MethodPost {
		out, err = s.service.StartCommunityDiscovery(r.Context(), req)
	} else {
		out, err = s.service.CommunityDiscovery(r.Context(), req)
	}
	communityResult(w, out, err)
}
func (c *Client) CommunityDiscovery(ctx context.Context, req daemon.CommunityDiscoveryRequest) (daemon.CommunityDiscoveryPage, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("kind", req.Kind)
	q.Set("target", req.Target)
	q.Set("frontend", req.Frontend)
	q.Set("cursor", req.Cursor)
	q.Set("limit", strconv.Itoa(req.Limit))
	var out daemon.CommunityDiscoveryPage
	err := c.Do(ctx, http.MethodGet, "/v1/community/discovery?"+q.Encode(), nil, &out)
	return out, err
}
func (c *Client) StartCommunityDiscovery(ctx context.Context, req daemon.CommunityDiscoveryRequest) (daemon.CommunityDiscoveryPage, error) {
	var out daemon.CommunityDiscoveryPage
	err := c.Do(ctx, http.MethodPost, "/v1/community/discovery", req, &out)
	return out, err
}
