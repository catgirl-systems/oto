package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/storage"
)

// Capabilities are additive and advertise only wired backend functionality.
func communityCapabilities() []string {
	return []string{"users", "watches", "private-chat", "public-rooms", "private-rooms", "buddies", "interests", "self-profile", "discovery", "profiles"}
}

type CommunitySummary struct {
	CommunityIdentity
	Revision          uint64               `json:"revision"`
	Connected         bool                 `json:"connected"`
	Unread            int64                `json:"unread"`
	Mentions          int64                `json:"mentions"`
	Capabilities      []string             `json:"capabilities"`
	BuddyNotification DownloadNotification `json:"buddy_notification"`
}

func (s *Service) CommunitySummary(ctx context.Context) (CommunitySummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.shuttingDown {
		return CommunitySummary{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return CommunitySummary{}, err
	}
	if err := s.loadCommunityLocked(ctx); err != nil {
		return CommunitySummary{}, err
	}
	s.desiredUserWatchesLocked(time.Now())
	out := CommunitySummary{CommunityIdentity: s.community.identity, Connected: s.community.online, Revision: s.community.revision, Capabilities: communityCapabilities()}
	out.BuddyNotification = s.community.buddyNotification
	err := s.stateDB.ReadSnapshot(ctx, func(tx *storage.ReadTx) error {
		account, err := tx.Queries().GetCommunityAccount(ctx, out.Account)
		if err != nil {
			return err
		}
		totals, err := tx.Queries().CommunityUnreadTotals(ctx, out.Account)
		if err != nil {
			return err
		}
		out.Revision += uint64(account.Revision)
		out.Unread, out.Mentions = totals.Unread, totals.Mentions
		return nil
	})
	return out, err
}

type CommunityUsersRequest struct {
	CommunityIdentity
	Username string `json:"username"` // Optional exact lookup, distinct from the presentation filter.
	Cursor   string `json:"cursor"`
	Limit    int    `json:"limit"`
	Query    string `json:"query"`
}

type CommunityUsersPage struct {
	CommunityIdentity
	Users      []CommunityUser `json:"users"`
	NextCursor string          `json:"next_cursor"`
	Revision   uint64          `json:"revision"`
}

// CommunityUsers returns exact identities; folding is only a presentation filter.
// These live records never consult the legacy lowercased browse archive keys.
func (s *Service) CommunityUsers(ctx context.Context, req CommunityUsersRequest) (CommunityUsersPage, error) {
	if req.Limit < 0 || len(req.Cursor) > 1024 || len(req.Query) > 1024 || len(req.Username) > 1024 || req.Username != "" && (req.Cursor != "" || req.Query != "") {
		return CommunityUsersPage{}, errors.New("community: invalid user page")
	}
	limit := req.Limit
	if limit == 0 {
		limit = 200
	}
	limit = min(limit, 200)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.shuttingDown {
		return CommunityUsersPage{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return CommunityUsersPage{}, err
	}
	if req.CommunityIdentity != s.community.identity || req.Account != accountKey(s.cfg) {
		return CommunityUsersPage{}, ErrCommunitySession
	}
	account, err := s.stateDB.Queries().GetCommunityAccount(ctx, req.Account)
	if err != nil {
		return CommunityUsersPage{}, err
	}
	out := CommunityUsersPage{CommunityIdentity: s.community.identity, Revision: s.community.revision + uint64(account.Revision), Users: []CommunityUser{}}
	if req.Username != "" {
		if user, ok := s.community.users[req.Username]; ok {
			out.Users = append(out.Users, user)
		}
		return out, nil
	}
	var names []string
	query := strings.ToLower(req.Query)
	for username := range s.community.users {
		if username > req.Cursor && strings.Contains(strings.ToLower(username), query) {
			names = append(names, username)
		}
	}
	slices.Sort(names)
	for _, username := range names[:min(len(names), limit)] {
		out.Users = append(out.Users, s.community.users[username])
	}
	if len(names) > limit {
		out.NextCursor = names[limit-1]
	}
	return out, nil
}

type CommunityWatchRequest struct {
	CommunityIdentity
	Frontend string   `json:"frontend"`
	Users    []string `json:"users"`
}
