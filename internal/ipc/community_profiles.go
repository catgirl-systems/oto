package ipc

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCommunityProfileRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-user-profile", Method: http.MethodGet, Path: "/v1/community/profile",
		Summary: "Fetch a user profile", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account  string `query:"account" doc:"Community account"`
		Daemon   string `query:"daemon" doc:"Daemon identity"`
		Session  string `query:"session" doc:"Session number"`
		Username string `query:"username"`
		Frontend string `query:"frontend"`
	}) (*struct {
		Body daemon.CommunityProfile
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityProfile(ctx, daemon.CommunityProfileRequest{CommunityIdentity: identity, Username: input.Username, Frontend: input.Frontend})
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "refresh-user-profile", Method: http.MethodPost, Path: "/v1/community/profile",
		Summary: "Refresh a user profile", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Body daemon.CommunityProfileRequest
	}) (*struct {
		Body daemon.CommunityProfile
	}, error) {
		out, err := s.service.StartCommunityProfile(ctx, input.Body)
		return communityBody(out, err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-profile-picture", Method: http.MethodGet, Path: "/v1/community/profile/picture",
		Summary: "Fetch a profile picture", Errors: communityErrors,
	}, func(ctx context.Context, input *struct {
		Account  string `query:"account" doc:"Community account"`
		Daemon   string `query:"daemon" doc:"Daemon identity"`
		Session  string `query:"session" doc:"Session number"`
		Username string `query:"username"`
		Revision uint64 `query:"revision"`
	}) (*struct {
		Body daemon.CommunityProfilePicture
	}, error) {
		identity, err := identityParams{Account: input.Account, Daemon: input.Daemon, Session: input.Session}.identity()
		if err != nil {
			return nil, communityErr(err)
		}
		out, err := s.service.CommunityProfilePicture(ctx, daemon.CommunityProfilePictureRequest{CommunityIdentity: identity, Username: input.Username, Revision: input.Revision})
		return communityBody(out, err)
	})
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
