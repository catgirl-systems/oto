package daemon

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

const MaxCommunityBuddyNoteBytes = 4096

type CommunityBuddy struct {
	CommunityUser
	Note         string `json:"note"`
	NotifyOnline bool   `json:"notify_online"`
	Priority     bool   `json:"priority"`
	Trusted      bool   `json:"trusted"`
	// Revision covers editable buddy metadata, not unrelated chat/presence updates.
	// Together with CommunityIdentity it also fences deletion/recreation and restart.
	Revision uint64 `json:"revision"`
}
type CommunityBuddiesRequest struct {
	CommunityIdentity
	Username string `json:"username"`
	Query    string `json:"query"`
	Sort     string `json:"sort"`
	Cursor   string `json:"cursor"`
	Limit    int    `json:"limit"`
}
type CommunityBuddiesPage struct {
	CommunityIdentity
	Buddies    []CommunityBuddy `json:"buddies"`
	Revision   uint64           `json:"revision"`
	NextCursor string           `json:"next_cursor"`
	Total      int              `json:"total"`
}
type CommunityBuddyRequest struct {
	CommunityIdentity
	Username     string `json:"username"`
	Note         string `json:"note"`
	NotifyOnline bool   `json:"notify_online"`
	Priority     bool   `json:"priority"`
	Trusted      bool   `json:"trusted"`
	// Nil creates only. Updates/removal require the loaded buddy's revision.
	Revision *uint64 `json:"revision"`
	Remove   bool    `json:"remove"`
	Confirm  bool    `json:"confirm"`
}
type CommunityBuddyResult struct {
	CommunityIdentity
	Buddy   CommunityBuddy `json:"buddy"`
	Removed bool           `json:"removed"`
}

func communityBuddyFromRow(row db.CommunityBuddy, revision uint64) CommunityBuddy {
	buddy := CommunityBuddy{CommunityUser: CommunityUser{Username: row.Username}, Note: row.Note, NotifyOnline: row.NotifyOnline != 0, Priority: row.Priority != 0, Trusted: row.Trusted != 0, Revision: revision}
	if row.LastSeen != nil {
		buddy.LastSeen = time.UnixMilli(*row.LastSeen).UTC()
	}
	return buddy
}
func (s *Service) communityBuddyLocked(username string) CommunityBuddy {
	buddy := s.community.buddies[username]
	if user, ok := s.community.users[username]; ok {
		buddy.CommunityUser = user
	}
	return buddy
}
func (s *Service) SetCommunityBuddy(ctx context.Context, req CommunityBuddyRequest) (CommunityBuddyResult, error) {
	out := CommunityBuddyResult{CommunityIdentity: req.CommunityIdentity}
	if err := soulseek.ValidateUsername(req.Username); err != nil {
		return out, err
	}
	req.Note = strings.ReplaceAll(req.Note, "\r\n", "\n")
	if len(req.Note) > MaxCommunityBuddyNoteBytes || !utf8.ValidString(req.Note) || communityDisplayText(req.Note) != req.Note {
		return out, errors.New("community: buddy note must be valid text, without terminal controls, at most 4096 bytes")
	}
	if req.Remove && !req.Confirm {
		return out, errors.New("community: confirm removing this exact buddy; history is retained")
	}
	s.mu.Lock()
	var changedClient *soulseek.Client
	defer func() {
		s.mu.Unlock()
		if changedClient != nil {
			changedClient.RevalidateSharePolicy()
		}
	}()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return out, err
	}
	old, exists := s.community.buddies[req.Username]
	if req.Remove && !exists {
		out.Removed = true
		return out, nil
	}
	if !req.Remove && exists && old.Note == req.Note && old.NotifyOnline == req.NotifyOnline && old.Priority == req.Priority && old.Trusted == req.Trusted {
		out.Buddy = s.communityBuddyLocked(req.Username)
		return out, nil // Idempotent reconciliation of a lost response.
	}
	if exists && (req.Revision == nil || *req.Revision != old.Revision) || !exists && req.Revision != nil {
		return out, fmt.Errorf("community: buddy changed; reload saved note/flags: %w", ErrCommunityMessageState)
	}
	var row db.CommunityBuddy
	var revision int64
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		if req.Remove {
			if _, err := q.DeleteCommunityBuddy(ctx, db.DeleteCommunityBuddyParams{Account: req.Account, Username: req.Username}); err != nil {
				return err
			}
		} else {
			if err := q.PutCommunityBuddy(ctx, db.PutCommunityBuddyParams{Account: req.Account, Username: req.Username, Note: req.Note, NotifyOnline: boolInt(req.NotifyOnline), Priority: boolInt(req.Priority), Trusted: boolInt(req.Trusted)}); err != nil {
				return err
			}
			if seen := s.community.users[req.Username].LastSeen; !seen.IsZero() {
				if err := q.SetCommunityBuddyLastSeen(ctx, db.SetCommunityBuddyLastSeenParams{Account: req.Account, Username: req.Username, SeenAt: seen.UnixMilli()}); err != nil {
					return err
				}
			}
			var err error
			row, err = q.GetCommunityBuddy(ctx, db.GetCommunityBuddyParams{Account: req.Account, Username: req.Username})
			if err != nil {
				return err
			}
		}
		var err error
		revision, err = q.BumpCommunityRevision(ctx, req.Account)
		return err
	})
	if err != nil {
		return out, err
	}
	if req.Remove {
		delete(s.community.buddies, req.Username)
		out.Removed = true
	} else {
		if s.community.buddies == nil {
			s.community.buddies = map[string]CommunityBuddy{}
		}
		s.community.buddies[req.Username] = communityBuddyFromRow(row, uint64(revision))
	}
	changedClient = s.client
	if !exists || req.Remove || old.Priority != req.Priority {
		s.applyUploadUserPoliciesLocked()
	}
	names := make([]string, 0, len(s.community.buddies))
	for name := range s.community.buddies {
		names = append(names, name)
	}
	slices.Sort(names)
	s.setUserWatchesLocked("buddies", names, time.Time{})
	if !req.Remove {
		user := s.community.users[req.Username]
		if seen := s.community.buddies[req.Username].LastSeen; seen.After(user.LastSeen) {
			user.LastSeen = seen
			s.community.users[req.Username] = user
		}
		out.Buddy = s.communityBuddyLocked(req.Username)
	}
	return out, nil
}

// A keyset cursor retains its original sort key even if presence subsequently
// moves that buddy. Exact username breaks ties; sorting never folds identities.
type communityBuddyCursor struct{ Sort, Query, Key, Username string }

func (s *Service) CommunityBuddies(ctx context.Context, req CommunityBuddiesRequest) (CommunityBuddiesPage, error) {
	out := CommunityBuddiesPage{CommunityIdentity: req.CommunityIdentity, Buddies: []CommunityBuddy{}}
	limit, err := communityPageLimit(req.Limit)
	if err != nil || len(req.Cursor) > 65536 || len(req.Query) > 1024 || !utf8.ValidString(req.Query) || communityDisplayText(req.Query) != req.Query || strings.ContainsAny(req.Query, "\n\t") || req.Username != "" && (req.Cursor != "" || req.Query != "") {
		return out, errors.New("community: invalid buddy page")
	}
	if req.Sort == "" {
		req.Sort = "username"
	}
	if !slices.Contains([]string{"username", "status", "country", "last_seen", "note", "priority", "trusted", "notify"}, req.Sort) {
		return out, errors.New("community: invalid buddy sort")
	}
	if req.Username != "" {
		if err := soulseek.ValidateUsername(req.Username); err != nil {
			return out, err
		}
	}
	query := strings.ToLower(req.Query)
	var cursor communityBuddyCursor
	if req.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Sort != req.Sort || cursor.Query != query || len(cursor.Key) > MaxCommunityBuddyNoteBytes*2 || soulseek.ValidateUsername(cursor.Username) != nil {
			return out, errors.New("community: invalid buddy cursor; restart paging")
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return out, err
	}
	account, err := s.stateDB.Queries().GetCommunityAccount(ctx, req.Account)
	if err != nil {
		return out, err
	}
	out.Revision = s.community.revision + uint64(account.Revision)
	if req.Username != "" {
		if _, ok := s.community.buddies[req.Username]; ok {
			out.Buddies = append(out.Buddies, s.communityBuddyLocked(req.Username))
			out.Total = 1
		}
		return out, nil
	}
	keys := make(map[string]string)
	compare := func(key, name, otherKey, otherName string) int {
		c := strings.Compare(key, otherKey)
		if req.Sort == "last_seen" {
			c = -c
		}
		if c == 0 {
			c = strings.Compare(name, otherName)
		}
		return c
	}
	var names []string
	for name, buddy := range s.community.buddies {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if !strings.Contains(strings.ToLower(name), query) && !strings.Contains(strings.ToLower(buddy.Note), query) {
			continue
		}
		out.Total++
		key := s.communityBuddySortKeyLocked(name, req.Sort)
		if req.Cursor != "" && compare(key, name, cursor.Key, cursor.Username) <= 0 {
			continue
		}
		keys[name] = key
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int { return compare(keys[a], a, keys[b], b) })
	budget := 0
	for _, name := range names[:min(int(limit), len(names))] {
		row := s.communityBuddyLocked(name)
		encoded, _ := json.Marshal(row)
		if budget+len(encoded)+1 > communityPageBytes {
			if len(out.Buddies) == 0 {
				return out, errors.New("community: buddy exceeds page budget")
			}
			break
		}
		budget += len(encoded) + 1
		out.Buddies = append(out.Buddies, row)
	}
	if len(names) > len(out.Buddies) {
		last := names[len(out.Buddies)-1]
		data, _ := json.Marshal(communityBuddyCursor{Sort: req.Sort, Query: query, Key: keys[last], Username: last})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return out, nil
}
func (s *Service) communityBuddySortKeyLocked(name, order string) string {
	buddy := s.communityBuddyLocked(name)
	switch order {
	case "status":
		if !s.community.online || !buddy.StatusFresh {
			return "3"
		}
		if buddy.Status == soulseek.UserStatusOnline {
			return "0"
		}
		if buddy.Status == soulseek.UserStatusAway {
			return "1"
		}
		return "2"
	case "country":
		return cmp.Or(buddy.Country, "~~")
	case "last_seen":
		seen := int64(0)
		if !buddy.LastSeen.IsZero() {
			seen = max(0, buddy.LastSeen.UnixMilli())
		}
		return fmt.Sprintf("%020d", seen)
	case "note":
		return strings.ToLower(buddy.Note)
	case "priority":
		if buddy.Priority {
			return "0"
		}
		return "1"
	case "trusted":
		if buddy.Trusted {
			return "0"
		}
		return "1"
	case "notify":
		if buddy.NotifyOnline {
			return "0"
		}
		return "1"
	default:
		return name
	}
}
