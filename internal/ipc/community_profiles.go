package ipc

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func (s *Server) communityProfile(w http.ResponseWriter, r *http.Request) {
	var req daemon.CommunityProfileRequest
	if r.Method == http.MethodPost {
		if err := decode(w, r, &req); err != nil {
			communityError(w, err)
			return
		}
	} else {
		q := r.URL.Query()
		id, err := communityRoomIdentityQuery(q)
		if err != nil {
			communityError(w, err)
			return
		}
		req = daemon.CommunityProfileRequest{CommunityIdentity: id, Username: q.Get("username"), Frontend: q.Get("frontend")}
	}
	var out daemon.CommunityProfile
	var err error
	if r.Method == http.MethodPost {
		out, err = s.service.StartCommunityProfile(r.Context(), req)
	} else {
		out, err = s.service.CommunityProfile(r.Context(), req)
	}
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) communityProfilePicture(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, err := communityRoomIdentityQuery(q)
	if err != nil {
		communityError(w, err)
		return
	}
	revision, err := strconv.ParseUint(q.Get("revision"), 10, 64)
	if err != nil {
		communityError(w, err)
		return
	}
	out, err := s.service.CommunityProfilePicture(r.Context(), daemon.CommunityProfilePictureRequest{CommunityIdentity: id, Username: q.Get("username"), Revision: revision})
	if err != nil {
		communityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (c *Client) CommunityProfile(ctx context.Context, req daemon.CommunityProfileRequest) (daemon.CommunityProfile, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("username", req.Username)
	q.Set("frontend", req.Frontend)
	var out daemon.CommunityProfile
	err := c.Do(ctx, http.MethodGet, "/v1/community/profile?"+q.Encode(), nil, &out)
	return out, err
}
func (c *Client) StartCommunityProfile(ctx context.Context, req daemon.CommunityProfileRequest) (daemon.CommunityProfile, error) {
	var out daemon.CommunityProfile
	err := c.Do(ctx, http.MethodPost, "/v1/community/profile", req, &out)
	return out, err
}
func (c *Client) CommunityProfilePicture(ctx context.Context, req daemon.CommunityProfilePictureRequest) (daemon.CommunityProfilePicture, error) {
	q := communityRoomValues(req.CommunityIdentity)
	q.Set("username", req.Username)
	q.Set("revision", strconv.FormatUint(req.Revision, 10))
	var out daemon.CommunityProfilePicture
	// Only the explicit binary resource permits a larger response (base64 + metadata).
	err := c.do(ctx, http.MethodGet, "/v1/community/profile/picture?"+q.Encode(), nil, &out, int64(soulseek.MaxProfilePictureBytes*4/3+65536))
	return out, err
}
