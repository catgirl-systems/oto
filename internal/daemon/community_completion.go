package daemon

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

type CommunityCompletionRequest struct {
	CommunityIdentity
	Kind   string `json:"kind"` // command, user, room
	Prefix string `json:"prefix"`
	Room   string `json:"room,omitempty"`
}
type CommunityCompletion struct {
	CommunityIdentity
	Candidates []string `json:"candidates"`
	Truncated  bool     `json:"truncated"`
}

func (s *Service) CompleteCommunity(ctx context.Context, req CommunityCompletionRequest) (CommunityCompletion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := CommunityCompletion{CommunityIdentity: req.CommunityIdentity, Candidates: []string{}}
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return out, err
	}
	if len(req.Prefix) > 1024 || !utf8.ValidString(req.Prefix) || strings.IndexFunc(req.Prefix, unicode.IsControl) >= 0 {
		return out, errors.New("invalid completion prefix")
	}
	if req.Room != "" {
		if err := soulseek.ValidateRoomName(req.Room); err != nil {
			return out, err
		}
	}
	prefix := strings.ToLower(req.Prefix)
	candidates := map[string]bool{}
	add := func(name string) {
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			candidates[name] = true
		}
	}
	switch req.Kind {
	case "command":
		for _, spec := range CommandSpecs() {
			add(spec.Name)
		}
		q := s.stateDB.Queries()
		// Alias names are lowercase ASCII. Exact lookup plus one sorted page covers
		// the matching prefix without loading the entire account's alias collection.
		if row, err := q.GetCommunityAlias(ctx, db.GetCommunityAliasParams{Account: req.Account, Name: prefix}); err == nil {
			add(row.Name)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		rows, err := q.ListCommunityAliases(ctx, db.ListCommunityAliasesParams{Account: req.Account, AfterName: prefix, PageSize: 200})
		if err != nil {
			return out, err
		}
		for _, row := range rows {
			add(row.Name)
		}
		if len(rows) == 200 && strings.HasPrefix(rows[len(rows)-1].Name, prefix) {
			out.Truncated = true
		}
	case "user":
		add(s.cfg.Soulseek.Username)
		for name := range s.community.buddies {
			add(name)
		}
		for _, lease := range s.community.watches {
			if !lease.expiresAt.IsZero() && !time.Now().Before(lease.expiresAt) {
				continue
			}
			for _, name := range lease.users {
				add(name)
			}
		}
		if room := s.community.rooms[req.Room]; room != nil && room.joined {
			for name := range room.members {
				add(name)
			}
		}
	case "room":
		for name, room := range s.community.rooms {
			if room.joined {
				add(name)
			}
		}
	default:
		return out, errors.New("unsupported completion kind")
	}
	for name := range candidates {
		if name != "" {
			out.Candidates = append(out.Candidates, name)
		}
	}
	sort.Strings(out.Candidates)
	if len(out.Candidates) > 200 {
		out.Candidates = out.Candidates[:200]
		out.Truncated = true
	}
	page, more, err := takePage(slices.Values(out.Candidates), 200, 64<<10)
	if err != nil {
		return out, err
	}
	out.Candidates = page
	if more {
		out.Truncated = true
	}
	return out, ctx.Err()
}
