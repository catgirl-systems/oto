package ipc

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerCoreRoutes() {
	s.registerStateRoutes()
	s.registerSearchRoutes()
	s.registerBrowseRoutes()
	s.registerTransferRoutes()
	s.registerShareRoutes()
}

// portStatus maps port-check failures onto HTTP statuses.
func portStatus(err error) int {
	if errors.Is(err, daemon.ErrNotStarted) || errors.Is(err, daemon.ErrListenPortUnavailable) {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}

// presenceInput is the set-presence body.
type presenceInput struct {
	Body struct {
		Presence daemon.Presence `json:"presence" enum:"offline,away,online"`
	}
}

func (s *Server) registerStateRoutes() {
	route(s, scopePublic, huma.Operation{
		OperationID: "get-health", Method: http.MethodGet, Path: "/v1/health",
		Summary: "Liveness probe", Errors: []int{503},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Status daemon.Status `json:"status"`
		}
	}, error) {
		status := s.service.Status()
		if status == daemon.StatusStopped || status == daemon.StatusError {
			return nil, errStatus(http.StatusServiceUnavailable, errors.New("daemon: "+string(status)))
		}
		return &struct {
			Body struct {
				Status daemon.Status `json:"status"`
			}
		}{Body: struct {
			Status daemon.Status `json:"status"`
		}{Status: status}}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-state", Method: http.MethodGet, Path: "/v1/state",
		Summary: "Daemon state snapshot",
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body daemon.Snapshot
	}, error) {
		return &struct {
			Body daemon.Snapshot
		}{s.service.Snapshot()}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-logs", Method: http.MethodGet, Path: "/v1/logs",
		Summary: "Recent diagnostic log lines", Errors: []int{503},
	}, func(ctx context.Context, input *struct {
		Limit int `query:"limit" default:"500" doc:"Maximum log lines to return (1-2000)"`
	}) (*struct {
		Body any
	}, error) {
		records, err := s.service.LogRecords(min(max(input.Limit, 1), 2000))
		if err != nil {
			return nil, errStatus(http.StatusServiceUnavailable, err)
		}
		return &struct {
			Body any
		}{records}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-network-interfaces", Method: http.MethodGet, Path: "/v1/network/interfaces",
		Summary: "Available network interface names", Errors: []int{500},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []string
	}, error) {
		interfaces, err := s.listInterfaces()
		if err != nil {
			return nil, errStatus(http.StatusInternalServerError, err)
		}
		names := make([]string, 0, len(interfaces))
		for _, networkInterface := range interfaces {
			if networkInterface.Name != "" {
				names = append(names, networkInterface.Name)
			}
		}
		return &struct {
			Body []string
		}{sortedCompact(names)}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "check-listening-port", Method: http.MethodPost, Path: "/v1/network/port-check",
		Summary: "Check the listening port reachability", Errors: []int{502, 503},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body daemon.ListeningPortCheck
	}, error) {
		result, err := s.service.CheckListeningPort(ctx)
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, daemon.ErrNotStarted) || errors.Is(err, daemon.ErrListenPortUnavailable) {
				status = http.StatusServiceUnavailable
			}
			return nil, errStatus(status, err)
		}
		return &struct {
			Body daemon.ListeningPortCheck
		}{result}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "set-presence", Method: http.MethodPut, Path: "/v1/presence",
		Summary: "Set chat presence", Errors: []int{400},
	}, func(ctx context.Context, input *presenceInput) (*struct{}, error) {
		if err := s.service.SetPresence(input.Body.Presence); err != nil {
			return nil, errStatus(http.StatusBadRequest, err)
		}
		return &struct{}{}, nil
	})
	route(s, scopeLocal, huma.Operation{
		OperationID: "change-password", Method: http.MethodPut, Path: "/v1/account/password",
		Summary: "Change the Soulseek account password", Errors: []int{400, 503},
	}, func(ctx context.Context, input *struct {
		Body struct {
			Password string `json:"password"`
		}
	}) (*struct {
		Body daemon.PasswordChangeResult
	}, error) {
		if strings.TrimSpace(input.Body.Password) == "" {
			return nil, errStatus(http.StatusBadRequest, errors.New("ipc: password cannot be empty"))
		}
		result, err := s.service.ChangePassword(ctx, input.Body.Password)
		if err != nil {
			return nil, errStatus(http.StatusServiceUnavailable, err)
		}
		return &struct {
			Body daemon.PasswordChangeResult
		}{result}, nil
	})
}

type searchInput struct {
	Query     string   `json:"query" required:"true"`
	Filter    string   `json:"filter" required:"false"`
	Account   string   `json:"account" required:"false"`
	Daemon    string   `json:"daemon" required:"false"`
	Session   uint64   `json:"session" required:"false"`
	Scope     string   `json:"scope,omitempty"`
	Usernames []string `json:"usernames,omitempty"`
	Rooms     []string `json:"rooms,omitempty"`
}

func (s *Server) registerSearchRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "search", Method: http.MethodPost, Path: "/v1/search",
		Summary: "Start a search", Errors: []int{400, 503},
	}, func(ctx context.Context, input *struct {
		Body searchInput
	}) (*struct {
		Body daemon.SearchPage
	}, error) {
		req := daemon.ScopedSearchRequest{CommunityIdentity: daemon.CommunityIdentity{Account: input.Body.Account, Daemon: input.Body.Daemon, Session: input.Body.Session}, Query: input.Body.Query, Filter: input.Body.Filter, Scope: input.Body.Scope, Usernames: input.Body.Usernames, Rooms: input.Body.Rooms}
		out, err := s.service.SearchScoped(ctx, req)
		if err != nil {
			if errors.Is(err, daemon.ErrNotStarted) || errors.Is(err, soulseek.ErrNotConnected) || errors.Is(err, context.DeadlineExceeded) {
				return nil, errStatus(http.StatusServiceUnavailable, err)
			}
			return nil, communityErr(err)
		}
		return &struct {
			Body daemon.SearchPage
		}{out}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-search-results", Method: http.MethodGet, Path: "/v1/searches",
		Summary: "Page search results", Errors: []int{400, 404},
	}, func(ctx context.Context, input *struct {
		ID     string `query:"id" doc:"Search ID to page"`
		Cursor int    `query:"cursor"`
		Filter string `query:"filter"`
	}) (*struct {
		Body daemon.SearchPage
	}, error) {
		page, err := s.service.SearchPage(input.ID, input.Cursor, input.Filter)
		if err != nil {
			status := http.StatusNotFound
			if errors.Is(err, daemon.ErrInvalidFilter) {
				status = http.StatusBadRequest
			}
			return nil, errStatus(status, err)
		}
		return &struct {
			Body daemon.SearchPage
		}{page}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-wishlist", Method: http.MethodGet, Path: "/v1/wishlist",
		Summary: "List wishlist items",
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []daemon.WishlistItem
	}, error) {
		return &struct {
			Body []daemon.WishlistItem
		}{s.service.Wishlist()}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "put-wishlist", Method: http.MethodPut, Path: "/v1/wishlist",
		Summary: "Add a wishlist item", Errors: []int{400, 404, 503},
	}, func(ctx context.Context, input *struct {
		Body wishlistInput
	}) (*struct {
		Body daemon.WishlistItem
	}, error) {
		item, err := s.service.PutWishlist(input.Body.Query, input.Body.Filter)
		if err != nil {
			return nil, wishlistErr(err)
		}
		return &struct {
			Body daemon.WishlistItem
		}{item}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "remove-wishlist", Method: http.MethodDelete, Path: "/v1/wishlist/{id}",
		Summary: "Remove a wishlist item", Errors: []int{400, 404, 503},
	}, func(ctx context.Context, input *struct {
		ID string `path:"id"`
	}) (*struct{}, error) {
		if err := s.service.RemoveWishlist(input.ID); err != nil {
			return nil, wishlistErr(err)
		}
		return &struct{}{}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "run-wishlist", Method: http.MethodPost, Path: "/v1/wishlist/{id}/run",
		Summary: "Run a wishlist search now", Errors: []int{400, 404, 503},
	}, func(ctx context.Context, input *struct {
		ID string `path:"id"`
	}) (*struct {
		Body daemon.SearchPage
	}, error) {
		page, err := s.service.RunWishlist(ctx, input.ID)
		if err != nil {
			return nil, wishlistErr(err)
		}
		return &struct {
			Body daemon.SearchPage
		}{page}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "open-wishlist", Method: http.MethodPost, Path: "/v1/wishlist/{id}/open",
		Summary: "Open wishlist results", Errors: []int{400, 404, 503},
	}, func(ctx context.Context, input *struct {
		ID string `path:"id"`
	}) (*struct {
		Body daemon.SearchPage
	}, error) {
		page, err := s.service.OpenWishlist(input.ID)
		if err != nil {
			return nil, wishlistErr(err)
		}
		return &struct {
			Body daemon.SearchPage
		}{page}, nil
	})
}

// wishlistInput is the add-wishlist body.
type wishlistInput struct {
	Query  string `json:"query"`
	Filter string `json:"filter"`
}

func (s *Server) registerBrowseRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "open-browse", Method: http.MethodGet, Path: "/v1/browse",
		Summary: "Open a peer share browse", Errors: []int{400},
	}, func(ctx context.Context, input *struct {
		User   string `query:"user"`
		Folder string `query:"folder"`
		Query  string `query:"query"`
	}) (*struct {
		Body daemon.BrowsePage
	}, error) {
		out, err := s.service.OpenBrowse(ctx, input.User, input.Folder, input.Query)
		return wrapBody(out), badRequestErr(err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-browse-page", Method: http.MethodGet, Path: "/v1/browse/page",
		Summary: "Page an open browse", Errors: []int{400},
	}, func(ctx context.Context, input *struct {
		User     string `query:"user"`
		Revision uint64 `query:"revision"`
		Folder   string `query:"folder"`
		Query    string `query:"query"`
		Cursor   int    `query:"cursor"`
	}) (*struct {
		Body daemon.BrowsePage
	}, error) {
		out, err := s.service.BrowsePage(ctx, daemon.BrowsePageRequest{Username: input.User, Revision: input.Revision, Folder: input.Folder, Query: input.Query, Cursor: input.Cursor})
		return wrapBody(out), badRequestErr(err)
	})
	for _, variant := range []struct {
		id      string
		path    string
		summary string
		as      bool
	}{{"queue-browse", "/v1/browse/download", "Queue files from a browse", false}, {"queue-browse-as", "/v1/browse/download-as", "Queue files from a browse with a destination", true}} {
		requireDestination := variant.as
		route(s, scopeAuthed, huma.Operation{
			OperationID: variant.id, Method: http.MethodPost, Path: variant.path,
			Summary: variant.summary, Errors: []int{400},
		}, func(ctx context.Context, input *struct {
			Body daemon.BrowseDownloadRequest
		}) (*struct {
			Body daemon.BrowseDownloadResult
		}, error) {
			if requireDestination && input.Body.Destination == "" {
				return nil, errStatus(http.StatusBadRequest, errors.New("download destination is required"))
			}
			out, err := s.service.QueueBrowse(ctx, input.Body)
			return wrapBody(out), badRequestErr(err)
		})
	}
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-browse-progress", Method: http.MethodGet, Path: "/v1/browse/progress",
		Summary: "Browse download progress",
	}, func(ctx context.Context, input *struct {
		User string `query:"user"`
	}) (*struct {
		Body *daemon.BrowseProgress
	}, error) {
		return &struct {
			Body *daemon.BrowseProgress
		}{s.service.BrowseProgress(input.User)}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "get-saved-browses", Method: http.MethodGet, Path: "/v1/browse/saved",
		Summary: "Saved share browses", Errors: []int{400},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []daemon.SavedBrowse
	}, error) {
		out, err := s.service.SavedBrowses()
		return wrapBody(out), badRequestErr(err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "save-browse", Method: http.MethodPost, Path: "/v1/browse/save",
		Summary: "Save a share browse snapshot", Errors: []int{400},
	}, func(ctx context.Context, input *struct {
		User string `query:"user"`
		Body saveBrowseInput
	}) (*struct {
		Body daemon.SavedBrowse
	}, error) {
		out, err := s.service.SaveBrowse(input.User, input.Body.Revision)
		return wrapBody(out), badRequestErr(err)
	})
}

type folderDownloadInput struct {
	Username    string                `json:"username" required:"false"`
	DownloadDir string                `json:"download_dir,omitempty"`
	Folder      string                `json:"folder" required:"false"`
	Destination string                `json:"destination,omitempty"`
	Subfolders  []string              `json:"subfolders,omitempty"`
	Files       []daemon.DownloadItem `json:"files,omitempty"`
	Recursive   bool                  `json:"recursive" required:"false"`
}

func (f folderDownloadInput) request() daemon.FolderDownloadRequest {
	return daemon.FolderDownloadRequest{Username: f.Username, DownloadDir: f.DownloadDir, Folder: f.Folder, Destination: f.Destination, Subfolders: f.Subfolders, Files: f.Files, Recursive: f.Recursive}
}

// saveBrowseInput is the save-browse body.
type saveBrowseInput struct {
	Revision uint64 `json:"revision"`
}

func (s *Server) registerTransferRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "queue-downloads", Method: http.MethodPost, Path: "/v1/downloads",
		Summary: "Queue downloads", Errors: []int{400},
	}, func(ctx context.Context, input *struct {
		Body []daemon.DownloadRequest
	}) (*struct {
		Body []daemon.Download
	}, error) {
		out, err := s.service.QueueDownloads(input.Body)
		return wrapBody(out), badRequestErr(err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "force-downloads", Method: http.MethodPost, Path: "/v1/downloads/force",
		Summary: "Queue downloads bypassing filters", Errors: []int{400},
	}, func(ctx context.Context, input *struct {
		Body forceDownloadsInput
	}) (*struct {
		Body daemon.UploadActionResult
	}, error) {
		result, err := s.service.ForceDownloads(input.Body.IDs)
		return wrapBody(result), badRequestErr(err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "queue-folder-downloads", Method: http.MethodPost, Path: "/v1/folder-downloads",
		Summary: "Queue a folder download", Errors: []int{400},
	}, func(ctx context.Context, input *struct {
		Body folderDownloadInput
	}) (*queuedOutput, error) {
		out, err := s.service.QueueFolder(ctx, input.Body.request())
		if err != nil {
			return nil, errStatus(http.StatusBadRequest, err)
		}
		return &queuedOutput{Body: queuedBody{Queued: len(out)}}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "queue-folder-downloads-as", Method: http.MethodPost, Path: "/v1/folder-downloads/as",
		Summary: "Queue a folder download with a destination", Errors: []int{400},
	}, func(ctx context.Context, input *struct {
		Body folderDownloadInput
	}) (*queuedOutput, error) {
		if input.Body.Destination == "" {
			return nil, errStatus(http.StatusBadRequest, errors.New("download destination is required"))
		}
		out, err := s.service.QueueFolder(ctx, input.Body.request())
		if err != nil {
			return nil, errStatus(http.StatusBadRequest, err)
		}
		return &queuedOutput{Body: queuedBody{Queued: len(out)}}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-transfers", Method: http.MethodGet, Path: "/v1/transfers",
		Summary: "List transfers",
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []daemon.Transfer
	}, error) {
		return wrapBody(s.service.Transfers()), nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "transfer-action", Method: http.MethodPost, Path: "/v1/transfers/{id}",
		Summary: "Pause, resume or cancel a transfer", Errors: []int{400},
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id"`
		Body transferActionInput
	}) (*okOutput, error) {
		if err := s.service.TransferAction(input.ID, input.Body.Action); err != nil {
			return nil, errStatus(http.StatusBadRequest, err)
		}
		out := &okOutput{}
		out.Body.OK = true
		return out, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "upload-action", Method: http.MethodPost, Path: "/v1/uploads/actions",
		Summary: "Act on a waiting upload", Errors: []int{400, 503},
	}, func(ctx context.Context, input *struct {
		Body daemon.UploadActionRequest
	}) (*struct {
		Body daemon.UploadActionResult
	}, error) {
		result, err := s.service.UploadAction(input.Body)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, daemon.ErrClosed) || errors.Is(err, daemon.ErrUploadUnavailable) {
				status = http.StatusServiceUnavailable
			}
			return nil, errStatus(status, err)
		}
		return wrapBody(result), nil
	})
}

func (s *Server) registerShareRoutes() {
	route(s, scopeAuthed, huma.Operation{
		OperationID: "list-shares", Method: http.MethodGet, Path: "/v1/shares",
		Summary: "List shares",
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []config.Share
	}, error) {
		return wrapBody(s.service.Shares()), nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "add-share", Method: http.MethodPost, Path: "/v1/shares",
		Summary: "Add a share", Errors: []int{400},
	}, func(ctx context.Context, input *struct {
		Body config.Share
	}) (*struct {
		Body []config.Share
	}, error) {
		if err := s.service.AddShare(input.Body); err != nil {
			return nil, errStatus(http.StatusBadRequest, err)
		}
		return wrapBody(s.service.Shares()), nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "browse-local-shares", Method: http.MethodGet, Path: "/v1/shares/browse",
		Summary: "Browse the local share index", Errors: []int{400},
	}, func(ctx context.Context, input *struct {
		Path string `query:"path"`
	}) (*struct {
		Body []soulseek.ShareEntry
	}, error) {
		out, err := s.service.BrowseLocal(input.Path)
		return wrapBody(out), badRequestErr(err)
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "rescan-shares", Method: http.MethodPost, Path: "/v1/shares/rescan",
		Summary: "Rescan shares", Errors: []int{400, 409},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body []config.Share
	}, error) {
		if err := s.service.Rescan(); err != nil {
			return nil, scanErr(err)
		}
		return wrapBody(s.service.Shares()), nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "cancel-share-scan", Method: http.MethodPost, Path: "/v1/shares/rescan/cancel",
		Summary: "Cancel the running share scan", Errors: []int{400, 409},
		DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, input *struct {
		Body scanCancelRequest
	}) (*struct {
		Status int
		Body   scanCancelRequest
	}, error) {
		if err := s.service.CancelShareScan(input.Body.ID); err != nil {
			return nil, scanErr(err)
		}
		return &struct {
			Status int
			Body   scanCancelRequest
		}{http.StatusAccepted, input.Body}, nil
	})
	route(s, scopeAuthed, huma.Operation{
		OperationID: "remove-share", Method: http.MethodDelete, Path: "/v1/shares/{name}",
		Summary: "Remove a share", Errors: []int{404},
	}, func(ctx context.Context, input *struct {
		Name string `path:"name"`
	}) (*struct {
		Body []config.Share
	}, error) {
		if err := s.service.RemoveShare(input.Name); err != nil {
			return nil, errStatus(http.StatusNotFound, err)
		}
		return wrapBody(s.service.Shares()), nil
	})
	// PUT/PATCH /v1/config is TUI-only (local scope) and owned by
	// config.Config's custom UnmarshalJSON semantics, so it keeps the original
	// raw handler instead of a huma operation.
	s.socketMux.HandleFunc("PUT /v1/config", s.updateConfigRaw)
	s.socketMux.HandleFunc("PATCH /v1/config", s.updateConfigRaw)
}

func (s *Server) updateConfigRaw(w http.ResponseWriter, r *http.Request) {
	var c config.Config
	if err := decode(w, r, &c); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.service.UpdateConfig(c); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, s.service.Config())
}

// helpers

func wrapBody[T any](value T) *struct {
	Body T
} {
	return &struct {
		Body T
	}{value}
}

func badRequestErr(err error) huma.StatusError {
	if err == nil {
		return nil
	}
	return huma.NewError(http.StatusBadRequest, err.Error())
}

func wishlistErr(err error) huma.StatusError {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, daemon.ErrWishlistNotFound), errors.Is(err, daemon.ErrWishlistNoResults):
		status = http.StatusNotFound
	case errors.Is(err, daemon.ErrNotStarted):
		status = http.StatusServiceUnavailable
	}
	return huma.NewError(status, err.Error())
}

func scanErr(err error) huma.StatusError {
	status := http.StatusBadRequest
	if errors.Is(err, daemon.ErrScanConflict) || errors.Is(err, daemon.ErrScanBusy) || errors.Is(err, daemon.ErrScanCancelled) {
		status = http.StatusConflict
	}
	return huma.NewError(status, err.Error())
}

// forceDownloadsInput is the force-downloads body.
type forceDownloadsInput struct {
	IDs []string `json:"ids"`
}

// queuedBody is the folder-download queue result.
type queuedBody struct {
	Queued int `json:"queued"`
}

// queuedOutput wraps queuedBody.
type queuedOutput struct {
	Body queuedBody
}

// transferActionInput is the transfer-action body.
type transferActionInput struct {
	Action string `json:"action" enum:"pause,cancel,retry,resume,clear"`
}

func sortedCompact(names []string) []string {
	slices.Sort(names)
	return slices.Compact(names)
}
