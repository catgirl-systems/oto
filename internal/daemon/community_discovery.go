package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

const maxCommunityDiscoveryBytes = 16 << 20

type communityDiscoveryKey struct{ Kind, Target string }
type communityDiscoveryQuery struct {
	generation              uint64
	state, failure          string
	awaiting                bool
	deadline, updated, used time.Time
	rows                    []CommunityDiscoveryRow
	bytes                   int
}
type CommunityDiscoveryRow struct {
	Kind   string         `json:"kind"`
	Item   string         `json:"item,omitempty"`
	Score  int32          `json:"score,omitempty"`
	User   *CommunityUser `json:"user,omitempty"`
	Rating uint32         `json:"rating,omitempty"`
}
type CommunityDiscoveryRequest struct {
	CommunityIdentity
	Kind     string `json:"kind"`
	Target   string `json:"target"`
	Frontend string `json:"frontend"`
	Cursor   string `json:"cursor"`
	Limit    int    `json:"limit"`
	Refresh  bool   `json:"refresh"`
}
type CommunityDiscoveryPage struct {
	CommunityIdentity
	Kind       string                  `json:"kind"`
	Target     string                  `json:"target"`
	Rows       []CommunityDiscoveryRow `json:"rows"`
	State      string                  `json:"state"`
	Error      string                  `json:"error,omitempty"`
	Generation uint64                  `json:"generation"`
	Revision   uint64                  `json:"revision"`
	UpdatedAt  time.Time               `json:"updated_at"`
	NextCursor string                  `json:"next_cursor"`
	Total      int                     `json:"total"`
}
type communityDiscoveryCursor struct {
	Kind, Target string
	Generation   uint64
	Offset       int
}

func communityDiscoveryRequest(req CommunityDiscoveryRequest) (communityDiscoveryKey, int, error) {
	key := communityDiscoveryKey{req.Kind, req.Target}
	switch req.Kind {
	case "personal", "global", "similar":
		if req.Target != "" {
			return key, 0, errors.New("community: query does not take a target")
		}
	case "item", "item-users":
		var err error
		key.Target, err = soulseek.NormalizeInterest(req.Target)
		if err != nil {
			return key, 0, err
		}
	case "user-interests":
		if err := soulseek.ValidateUsername(req.Target); err != nil {
			return key, 0, err
		}
	default:
		return key, 0, errors.New("community: invalid discovery kind")
	}
	limit, err := communityPageLimit(req.Limit)
	if err != nil {
		return key, 0, err
	}
	if len(req.Cursor) > 16384 || req.Frontend == "" || len(req.Frontend) > 128 || strings.ContainsAny(req.Frontend, "\r\n\x00") {
		return key, 0, errors.New("community: invalid discovery page/frontend")
	}
	return key, int(limit), nil
}
func (s *Service) CommunityDiscovery(ctx context.Context, req CommunityDiscoveryRequest) (CommunityDiscoveryPage, error) {
	key, limit, err := communityDiscoveryRequest(req)
	if err != nil {
		return CommunityDiscoveryPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityDiscoveryPage{}, err
	}
	return s.communityDiscoveryPageLocked(req, key, limit)
}
func (s *Service) communityDiscoveryPageLocked(req CommunityDiscoveryRequest, key communityDiscoveryKey, limit int) (CommunityDiscoveryPage, error) {
	out := CommunityDiscoveryPage{CommunityIdentity: req.CommunityIdentity, Kind: key.Kind, Target: key.Target, Rows: []CommunityDiscoveryRow{}, State: "idle", Revision: s.community.revision}
	if !s.community.online {
		out.State = "offline"
	}
	q := s.community.discovery.queries[key]
	if q == nil {
		if req.Cursor != "" {
			return out, fmt.Errorf("community: results expired; restart pages: %w", ErrCommunityMessageState)
		}
		return out, s.discoveryWatchesLocked(req.Frontend, nil, time.Now())
	}
	now := time.Now()
	q.used = now
	if q.state == "pending" && q.awaiting && now.After(q.deadline) {
		q.state, q.failure = "unknown", "response timed out; awaiting original response until reconnect"
		s.community.revision++
	}
	out.State, out.Error, out.Generation, out.UpdatedAt, out.Total = q.state, q.failure, q.generation, q.updated, len(q.rows)
	if !s.community.online {
		out.State = "offline"
	}
	offset := 0
	if req.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		var cursor communityDiscoveryCursor
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Offset < 0 {
			return out, errors.New("community: invalid discovery cursor")
		}
		if cursor.Kind != key.Kind || cursor.Target != key.Target || cursor.Generation != q.generation {
			return out, fmt.Errorf("community: results changed; restart pages: %w", ErrCommunityMessageState)
		}
		if cursor.Offset > len(q.rows) {
			return out, errors.New("community: invalid discovery cursor")
		}
		offset = cursor.Offset
	}
	end := min(offset+limit, len(q.rows))
	var users []string
	for _, row := range q.rows[offset:end] {
		if row.User != nil {
			users = append(users, row.User.Username)
		}
	}
	if err := s.discoveryWatchesLocked(req.Frontend, users, now); err != nil {
		return out, err
	}
	rows, _, err := takePage(func(yield func(CommunityDiscoveryRow) bool) {
		for _, row := range q.rows[offset:end] {
			if row.User != nil {
				user := s.community.users[row.User.Username]
				user.Username = row.User.Username
				row.User = &user
			}
			if !yield(row) {
				return
			}
		}
	}, int(limit), communityPageBytes)
	if err != nil {
		return out, err
	}
	out.Rows = rows
	end = offset + len(rows)
	if end < len(q.rows) {
		raw, _ := json.Marshal(communityDiscoveryCursor{key.Kind, key.Target, q.generation, end})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	out.Revision = s.community.revision
	return out, nil
}

// Each frontend keeps its own expiring lease; the union of discovery leases
// never exceeds 200 users. Buddies, profiles and conversations own other leases.
func (s *Service) discoveryWatchesLocked(frontend string, users []string, now time.Time) error {
	s.desiredUserWatchesLocked(now)
	owner := "discovery:" + frontend
	watched := map[string]bool{}
	frontends := 0
	for key, lease := range s.community.watches {
		if strings.HasPrefix(key, "discovery:") {
			frontends++
			if key != owner {
				for _, user := range lease.users {
					watched[user] = true
				}
			}
		}
	}
	if _, ok := s.community.watches[owner]; !ok && len(users) > 0 && frontends >= 64 {
		return errors.New("community: too many discovery frontends")
	}
	accepted := make([]string, 0, len(users))
	for _, user := range users {
		if watched[user] || len(watched) < 200 {
			watched[user] = true
			accepted = append(accepted, user)
		}
	}
	s.setUserWatchesLocked(owner, accepted, now.Add(time.Minute))
	return nil
}
func (s *Service) StartCommunityDiscovery(ctx context.Context, req CommunityDiscoveryRequest) (CommunityDiscoveryPage, error) {
	key, limit, err := communityDiscoveryRequest(req)
	if err != nil {
		return CommunityDiscoveryPage{}, err
	}
	if req.Cursor != "" {
		return CommunityDiscoveryPage{}, errors.New("community: start query without a page cursor")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityDiscoveryPage{}, err
	}
	if !s.communityCurrentLocked(req.CommunityIdentity) {
		return CommunityDiscoveryPage{}, soulseek.ErrNotConnected
	}
	d := &s.community.discovery
	old := d.queries[key]
	if old != nil && (old.awaiting || old.state == "pending" || old.state == "ready" && !req.Refresh) {
		return s.communityDiscoveryPageLocked(req, key, limit)
	}
	if old == nil && len(d.queries) >= 32 && !s.evictDiscoveryLocked(key) {
		return CommunityDiscoveryPage{}, errors.New("community: too many outstanding discovery queries; wait or reconnect")
	}
	if old != nil {
		d.queryBytes -= old.bytes
	}
	d.nextQuery++
	q := &communityDiscoveryQuery{generation: d.nextQuery, state: "pending", used: time.Now()}
	d.queries[key] = q
	s.community.revision++
	client := s.client
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		writeCtx, cancel := context.WithTimeout(s.scanCtx, 5*time.Second)
		defer cancel()
		attempted, err := client.SendDiscovery(writeCtx, soulseek.DiscoveryRequest{Kind: key.Kind, Target: key.Target}, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.communityCurrentLocked(req.CommunityIdentity) || s.client != client || s.community.discovery.queries[key] != q {
				return ErrCommunitySession
			}
			if s.shuttingDown {
				return ErrClosed
			}
			q.awaiting, q.deadline = true, time.Now().Add(15*time.Second)
			return nil
		})
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.communityCurrentLocked(req.CommunityIdentity) || s.client != client || s.community.discovery.queries[key] != q || q.state == "ready" {
			return
		}
		if err != nil {
			q.writeFailed(attempted)
			s.community.revision++
		}
	}()
	return s.communityDiscoveryPageLocked(req, key, limit)
}

// Pending/uncertain tokenless requests cannot be evicted or reissued: their late
// responses would otherwise be mistaken for a newer request on the same socket.
func (s *Service) evictDiscoveryLocked(exclude communityDiscoveryKey) bool {
	var oldest communityDiscoveryKey
	var used time.Time
	found := false
	for key, q := range s.community.discovery.queries {
		if key == exclude || q.awaiting || q.state == "pending" {
			continue
		}
		if !found || q.used.Before(used) {
			oldest, used, found = key, q.used, true
		}
	}
	if found {
		s.community.discovery.queryBytes -= s.community.discovery.queries[oldest].bytes
		delete(s.community.discovery.queries, oldest)
	}
	return found
}
func (s *Service) applyDiscoveryLocked(response soulseek.DiscoveryResponse) {
	key := communityDiscoveryKey{response.Kind, response.Target}
	q := s.community.discovery.queries[key]
	if q == nil || !q.awaiting {
		return
	}
	rows := make([]CommunityDiscoveryRow, 0, len(response.Recommendations)+len(response.Users)+len(response.Likes)+len(response.Dislikes))
	bytes := 0
	for _, r := range response.Recommendations {
		text := communityDisplayText(r.Item)
		rows = append(rows, CommunityDiscoveryRow{Kind: "recommendation", Item: text, Score: r.Score})
		bytes += len(text) + 128
	}
	seen := map[string]bool{}
	for _, u := range response.Users {
		// XXX: skip undecodable similar names; nicotine+ renders whatever it receives.
		if seen[u.Username] || soulseek.ValidateUsername(u.Username) != nil {
			continue
		}
		seen[u.Username] = true
		rows = append(rows, CommunityDiscoveryRow{Kind: "user", User: &CommunityUser{Username: u.Username}, Rating: u.Rating})
		bytes += len(u.Username) + 512
	}
	for _, group := range []struct {
		kind  string
		items []string
	}{{"like", response.Likes}, {"dislike", response.Dislikes}} {
		for _, item := range group.items {
			text := communityDisplayText(item)
			rows = append(rows, CommunityDiscoveryRow{Kind: group.kind, Item: text})
			bytes += len(text) + 128
		}
	}
	// Stable useful ordering, with exact usernames breaking equal score ties.
	slices.SortStableFunc(rows, func(a, b CommunityDiscoveryRow) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		if a.User != nil && b.User != nil {
			if a.Rating > b.Rating {
				return -1
			}
			if a.Rating < b.Rating {
				return 1
			}
			return strings.Compare(a.User.Username, b.User.Username)
		}
		if a.Score > b.Score {
			return -1
		}
		if a.Score < b.Score {
			return 1
		}
		return strings.Compare(a.Item, b.Item)
	})
	d := &s.community.discovery
	for d.queryBytes+bytes > maxCommunityDiscoveryBytes && s.evictDiscoveryLocked(key) {
	}
	q.awaiting = false
	q.updated = time.Now()
	q.used = q.updated
	if bytes > maxCommunityDiscoveryBytes-d.queryBytes {
		q.state, q.failure = "failed", "discovery cache is full; retry after other queries complete"
	} else {
		// Deduplicating users must not retain the larger incoming backing array.
		if len(rows) != cap(rows) {
			rows = slices.Clone(rows)
		}
		q.rows, q.bytes, q.state, q.failure = rows, bytes, "ready", ""
		d.queryBytes += bytes
	}
	s.community.revision++
}
func (s *Service) retireDiscoveryLocked() {
	for _, q := range s.community.discovery.queries {
		q.awaiting = false
		q.state, q.failure = "offline", "connection ended; refresh to query again"
	}
}

func (q *communityDiscoveryQuery) writeFailed(attempted bool) {
	q.awaiting = attempted
	q.state, q.failure = "failed", "query was not sent; retry explicitly"
	if attempted {
		q.state, q.failure = "unknown", "write interrupted; awaiting original response until reconnect"
	}
}
