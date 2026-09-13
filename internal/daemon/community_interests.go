package daemon

import (
	"context"
	"database/sql"
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

type communityDiscoveryState struct {
	interests           map[string]CommunityInterest
	written             map[string]string
	description         string
	descriptionRevision uint64
	queries             map[communityDiscoveryKey]*communityDiscoveryQuery
	nextQuery           uint64
	queryBytes          int
}
type CommunityInterest struct {
	Item     string `json:"item"`
	Opinion  string `json:"opinion"`
	Revision uint64 `json:"revision"`
	State    string `json:"state"`
}
type CommunityInterestsRequest struct {
	CommunityIdentity
	Cursor string `json:"cursor"`
	Query  string `json:"query"`
	Limit  int    `json:"limit"`
}
type CommunityInterestsPage struct {
	CommunityIdentity
	Interests  []CommunityInterest `json:"interests"`
	NextCursor string              `json:"next_cursor"`
	Total      int                 `json:"total"`
	Revision   uint64              `json:"revision"`
}
type CommunityInterestRequest struct {
	CommunityIdentity
	Item     string  `json:"item"`
	Opinion  string  `json:"opinion"`
	Revision *uint64 `json:"revision"`
	Remove   bool    `json:"remove"`
	Confirm  bool    `json:"confirm"`
}
type CommunityInterestResult struct {
	CommunityIdentity
	Interest CommunityInterest `json:"interest"`
	Removed  bool              `json:"removed"`
}
type CommunitySelfProfile struct {
	CommunityIdentity
	Description string `json:"description"`
	Revision    uint64 `json:"revision"`
}

func loadCommunityDiscovery(ctx context.Context, q *db.Queries, account db.CommunityAccount, next *communityState) error {
	if err := soulseek.ValidateSelfDescription(account.Description); err != nil {
		return err
	}
	d := communityDiscoveryState{interests: map[string]CommunityInterest{}, written: map[string]string{}, description: account.Description, descriptionRevision: uint64(account.Revision)}
	d.queries = map[communityDiscoveryKey]*communityDiscoveryQuery{}
	for after := ""; ; {
		rows, err := q.ListCommunityInterests(ctx, db.ListCommunityInterestsParams{Account: account.Account, AfterItem: after, PageSize: 200})
		if err != nil {
			return err
		}
		for _, row := range rows {
			item, err := soulseek.NormalizeInterest(row.Item)
			if err != nil || item != row.Item {
				return errors.New("community: stored interest is not normalized")
			}
			opinion := "like"
			if row.Opinion < 0 {
				opinion = "dislike"
			}
			d.interests[item] = CommunityInterest{Item: item, Opinion: opinion, Revision: uint64(account.Revision)}
			after = row.Item
		}
		if len(d.interests) > soulseek.MaxDiscoveryEntries {
			return errors.New("community: too many stored interests")
		}
		if len(rows) < 200 {
			break
		}
	}
	next.discovery = d
	return nil
}
func (s *Service) communityInterestLocked(item string) CommunityInterest {
	row := s.community.discovery.interests[item]
	row.State = "offline"
	if s.community.online {
		row.State = "pending"
		if s.community.discovery.written[item] == row.Opinion {
			row.State = "sent"
		}
	}
	return row
}
func (s *Service) CommunityInterests(ctx context.Context, req CommunityInterestsRequest) (CommunityInterestsPage, error) {
	out := CommunityInterestsPage{CommunityIdentity: req.CommunityIdentity, Interests: []CommunityInterest{}}
	limit, err := communityPageLimit(req.Limit)
	if err != nil || len(req.Query) > 1024 || len(req.Cursor) > soulseek.MaxInterestBytes || !utf8.ValidString(req.Query) || communityDisplayText(req.Query) != req.Query || strings.ContainsAny(req.Query, "\n\t") {
		return out, errors.New("community: invalid interest page")
	}
	if req.Cursor != "" {
		cursor, err := soulseek.NormalizeInterest(req.Cursor)
		if err != nil || cursor != req.Cursor {
			return out, errors.New("community: invalid interest cursor")
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
	names := make([]string, 0, len(s.community.discovery.interests))
	query := strings.ToLower(req.Query)
	for item := range s.community.discovery.interests {
		if strings.Contains(item, query) {
			out.Total++
			if item > req.Cursor {
				names = append(names, item)
			}
		}
	}
	slices.Sort(names)
	budget := 0
	for _, item := range names[:min(len(names), int(limit))] {
		row := s.communityInterestLocked(item)
		encoded, _ := json.Marshal(row)
		if budget+len(encoded)+1 > communityPageBytes {
			break
		}
		budget += len(encoded) + 1
		out.Interests = append(out.Interests, row)
	}
	if len(names) > len(out.Interests) {
		out.NextCursor = names[len(out.Interests)-1]
	}
	return out, nil
}
func (s *Service) SetCommunityInterest(ctx context.Context, req CommunityInterestRequest) (CommunityInterestResult, error) {
	out := CommunityInterestResult{CommunityIdentity: req.CommunityIdentity}
	item, err := soulseek.NormalizeInterest(req.Item)
	if err != nil {
		return out, err
	}
	out.Interest.Item = item
	if !req.Remove && req.Opinion != "like" && req.Opinion != "dislike" {
		return out, errors.New("community: choose like or dislike")
	}
	if req.Remove && !req.Confirm {
		return out, errors.New("community: confirm removing this interest")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return out, err
	}
	d := &s.community.discovery
	old, exists := d.interests[item]
	if req.Remove && !exists {
		out.Removed = true
		return out, nil
	}
	if !req.Remove && exists && old.Opinion == req.Opinion {
		out.Interest = s.communityInterestLocked(item)
		return out, nil
	}
	if exists && (req.Revision == nil || *req.Revision != old.Revision) || !exists && req.Revision != nil {
		return out, fmt.Errorf("community: interest changed; reload: %w", ErrCommunityMessageState)
	}
	if !exists && len(d.interests) >= soulseek.MaxDiscoveryEntries {
		return out, errors.New("community: interest limit reached")
	}
	var revision int64
	err = s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		if req.Remove {
			if _, err := q.DeleteCommunityInterest(ctx, db.DeleteCommunityInterestParams{Account: req.Account, Item: item}); err != nil {
				return err
			}
		} else {
			opinion := int64(1)
			if req.Opinion == "dislike" {
				opinion = -1
			}
			if err := q.PutCommunityInterest(ctx, db.PutCommunityInterestParams{Account: req.Account, Item: item, Opinion: opinion}); err != nil {
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
		delete(d.interests, item)
		out.Removed = true
	} else {
		d.interests[item] = CommunityInterest{Item: item, Opinion: req.Opinion, Revision: uint64(revision)}
		out.Interest = s.communityInterestLocked(item)
	}
	select {
	case s.community.wake <- struct{}{}:
	default:
	}
	return out, nil
}
func (s *Service) CommunitySelfProfile(ctx context.Context, identity CommunityIdentity) (CommunitySelfProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, identity); err != nil {
		return CommunitySelfProfile{}, err
	}
	return CommunitySelfProfile{identity, s.community.discovery.description, s.community.discovery.descriptionRevision}, nil
}
func (s *Service) SetCommunitySelfProfile(ctx context.Context, req CommunitySelfProfile) (CommunitySelfProfile, error) {
	req.Description = strings.ReplaceAll(req.Description, "\r\n", "\n")
	if err := soulseek.ValidateSelfDescription(req.Description); err != nil {
		return CommunitySelfProfile{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunitySelfProfile{}, err
	}
	d := &s.community.discovery
	if d.description == req.Description {
		req.Revision = d.descriptionRevision
		return req, nil
	}
	if d.descriptionRevision != req.Revision {
		return CommunitySelfProfile{}, fmt.Errorf("community: description changed; reload: %w", ErrCommunityMessageState)
	}
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		row, err := q.GetCommunityAccount(ctx, req.Account)
		if err != nil {
			return err
		}
		count, err := q.EditCommunityAccount(ctx, db.EditCommunityAccountParams{Account: req.Account, Revision: row.Revision, Description: req.Description, AcceptInvitations: row.AcceptInvitations, RetentionDays: row.RetentionDays, PublicFeedLogging: row.PublicFeedLogging})
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrCommunityMessageState
		}
		req.Revision = uint64(row.Revision + 1)
		return nil
	})
	if err != nil {
		return CommunitySelfProfile{}, err
	}
	d.description, d.descriptionRevision = req.Description, req.Revision
	if s.client != nil {
		if err := s.client.SetSelfDescription(req.Description); err != nil {
			return CommunitySelfProfile{}, err
		}
	}
	return req, nil
}

// Desired preferences are replayable, unlike uncertain messages/gifts. Each
// session starts with an empty sent set; removing/changing an opinion removes
// the previous server preference before adding its replacement.
func (s *Service) syncCommunityInterests(ctx context.Context, client *soulseek.Client, identity CommunityIdentity) error {
	s.mu.RLock()
	names := make([]string, 0, len(s.community.discovery.interests)+len(s.community.discovery.written))
	for name := range s.community.discovery.interests {
		names = append(names, name)
	}
	for name := range s.community.discovery.written {
		names = append(names, name)
	}
	s.mu.RUnlock()
	slices.Sort(names) // Duplicates allow remove-then-add when changing opinion.
	attemptedCount := 0
	for _, name := range names {
		if attemptedCount >= 200 {
			s.mu.Lock()
			select {
			case s.community.wake <- struct{}{}:
			default:
			}
			s.mu.Unlock()
			return nil
		}
		s.mu.Lock()
		if !s.communityCurrentLocked(identity) || s.client != client {
			s.mu.Unlock()
			return ErrCommunitySession
		}
		if s.shuttingDown {
			s.mu.Unlock()
			return nil
		}
		d := &s.community.discovery
		wanted, expected := d.interests[name].Opinion, d.written[name]
		s.mu.Unlock()
		if expected == wanted {
			continue
		}
		req := soulseek.InterestChangeRequest{Item: name, Like: wanted == "like"}
		if expected != "" {
			req.Like, req.Remove = expected == "like", true
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		attempted, err := client.ChangeInterest(writeCtx, req, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.communityCurrentLocked(identity) || s.client != client {
				return ErrCommunitySession
			}
			if s.shuttingDown {
				return ErrClosed
			}
			d := &s.community.discovery
			if d.written[name] != expected {
				return ErrCommunityMessageState
			}
			if !req.Remove {
				wanted := d.interests[name].Opinion
				if wanted == "" || (wanted == "like") != req.Like {
					return ErrCommunityMessageState
				}
			}
			return nil
		})
		cancel()
		if errors.Is(err, ErrCommunityMessageState) {
			continue
		}
		if errors.Is(err, ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		attemptedCount++
		s.mu.Lock()
		if attempted && s.communityCurrentLocked(identity) && s.client == client {
			if req.Remove {
				delete(s.community.discovery.written, name)
			} else {
				opinion := "dislike"
				if req.Like {
					opinion = "like"
				}
				s.community.discovery.written[name] = opinion
			}
			s.community.revision++
		}
		s.mu.Unlock()
	}
	return nil
}
