package daemon

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/country"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

var ErrCommunitySession = errors.New("community: account or session changed; refresh and try again")

// CommunityIdentity fences network operations, including replies to old TUIs.
// Session is the existing connection generation, not the configured username.
type CommunityIdentity struct {
	Account string `json:"account"`
	Session uint64 `json:"session"`
}

type CommunityUser struct {
	Username         string              `json:"username"`
	Exists           bool                `json:"exists"`
	Status           soulseek.UserStatus `json:"status"`
	Privileged       bool                `json:"privileged"`
	Country          string              `json:"country"`
	IP               string              `json:"ip"`
	Port             uint32              `json:"port"`
	Stats            soulseek.UserStats  `json:"stats"`
	StatusFresh      bool                `json:"status_fresh"`
	StatsFresh       bool                `json:"stats_fresh"`
	AddressFresh     bool                `json:"address_fresh"`
	StatusUpdatedAt  time.Time           `json:"status_updated_at"`
	StatsUpdatedAt   time.Time           `json:"stats_updated_at"`
	AddressUpdatedAt time.Time           `json:"address_updated_at"`
	LastSeen         time.Time           `json:"last_seen"`
	watchVersion     uint64              // A recreated cache needs hydration even before an unwatch was sent.
}

type userWatchLease struct {
	users     []string
	expiresAt time.Time // Zero for daemon-owned buddies, conversations and rooms.
}

// All fields are protected by Service.mu. Network writes always happen outside it.
type communityState struct {
	identity  CommunityIdentity
	online    bool
	revision  uint64
	nextWatch uint64
	users     map[string]CommunityUser
	watches   map[string]userWatchLease
	wake      chan struct{}
}

// loadCommunityLocked loads preferences, never durable authoritative presence.
// It is also used before a new connection after changing accounts.
func (s *Service) loadCommunityLocked(ctx context.Context) error {
	account := accountKey(s.cfg)
	if s.community.identity.Account == account {
		return nil
	}
	if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error { return db.New(tx).EnsureCommunityAccount(ctx, account) }); err != nil {
		return err
	}
	next := communityState{identity: CommunityIdentity{Account: account}, users: map[string]CommunityUser{}, watches: map[string]userWatchLease{}, wake: make(chan struct{}, 1)}
	err := s.stateDB.ReadSnapshot(ctx, func(tx *storage.ReadTx) error {
		q := tx.Queries()
		settings, err := q.GetCommunityAccount(ctx, account)
		if err != nil {
			return err
		}
		next.revision = uint64(settings.Revision)
		var buddies []string
		for after := ""; ; {
			rows, err := q.ListCommunityBuddies(ctx, db.ListCommunityBuddiesParams{Account: account, AfterUsername: after, PageSize: 200})
			if err != nil {
				return err
			}
			for _, row := range rows {
				buddies = append(buddies, row.Username)
				user := CommunityUser{Username: row.Username}
				if row.LastSeen != nil {
					user.LastSeen = time.UnixMilli(*row.LastSeen).UTC()
				}
				next.users[row.Username] = user
				after = row.Username
			}
			if len(rows) < 200 {
				break
			}
		}
		next.watches["buddies"] = userWatchLease{users: buddies}
		var conversations []string
		for after := int64(0); ; {
			rows, err := q.ListCommunityConversations(ctx, db.ListCommunityConversationsParams{Account: account, AfterID: after, PageSize: 200})
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.Kind == "private" && row.Closed == 0 {
					conversations = append(conversations, row.Target)
				}
				after = row.ID
			}
			if len(rows) < 200 {
				break
			}
		}
		next.watches["conversations"] = userWatchLease{users: conversations}
		return nil
	})
	if err != nil {
		return err
	}
	s.community = next
	s.desiredUserWatchesLocked(time.Now())
	return nil
}

func (s *Service) communityCurrentLocked(identity CommunityIdentity) bool {
	return !s.closed && !s.shuttingDown && s.client != nil && s.community.online && s.community.identity == identity && accountKey(s.cfg) == identity.Account
}

func (s *Service) retireCommunityLocked() {
	s.community.online = false
	for username, user := range s.community.users {
		user.StatusFresh, user.StatsFresh, user.AddressFresh = false, false, false
		s.community.users[username] = user
	}
	s.community.revision++
	// A local disconnect says nothing about when any buddy was last online.
}

func (s *Service) communityUpdate(ctx context.Context, identity CommunityIdentity, message soulseek.SocialMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.communityCurrentLocked(identity) {
		return ErrCommunitySession
	}
	var username string
	switch m := message.(type) {
	case soulseek.WatchUserResponse:
		username = m.Username
	case soulseek.UserPresence:
		username = m.Username
	case soulseek.UserStatistics:
		username = m.Username
	case soulseek.PeerAddress:
		username = m.Username
	}
	user, wanted := s.community.users[username]
	if !wanted {
		return nil
	} // Late unwatch replies and unsolicited users aren't cached.
	now := time.Now().UTC()
	switch m := message.(type) {
	case soulseek.WatchUserResponse:
		user.Exists, user.Status, user.Stats, user.Country = m.Exists, m.Status, m.Stats, m.Country
		user.StatusFresh, user.StatsFresh = true, m.Exists
		user.StatusUpdatedAt = now
		if m.Exists {
			user.StatsUpdatedAt = now
		}
	case soulseek.UserPresence:
		if user.StatusFresh && user.Status != soulseek.UserStatusOffline && m.Status == soulseek.UserStatusOffline {
			if err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
				return db.New(tx).SetCommunityBuddyLastSeen(ctx, db.SetCommunityBuddyLastSeenParams{Account: identity.Account, Username: username, SeenAt: now.UnixMilli()})
			}); err != nil {
				return err
			}
			user.LastSeen = now
		}
		user.Exists, user.Status, user.Privileged = true, m.Status, m.Privileged
		user.StatusFresh, user.StatusUpdatedAt = true, now
		if m.Status == soulseek.UserStatusOffline {
			user.AddressFresh = false
		}
	case soulseek.UserStatistics:
		user.Stats, user.StatsFresh, user.StatsUpdatedAt = m.Stats, true, now
	case soulseek.PeerAddress:
		user.IP, user.Port, user.AddressFresh, user.AddressUpdatedAt = m.IP, m.Port, true, now
		if ip, err := netip.ParseAddr(m.IP); err == nil {
			if code := country.Lookup(ip); code != "" {
				user.Country = code
			}
		}
	}
	s.community.users[username] = user
	s.community.revision++
	return nil
}

// WatchCommunityUsers replaces one frontend's interest atomically. Polling renews
// a lease, so an unclean detach can't leave permanent server subscriptions.
// Empty users releases this owner only; daemon-owned interests are independent.
func (s *Service) WatchCommunityUsers(identity CommunityIdentity, frontend string, users []string) error {
	if frontend == "" || len(frontend) > 128 || len(users) > 200 || strings.ContainsAny(frontend, "\r\n\x00") {
		return errors.New("community: invalid watch lease (maximum 200 users)")
	}
	for _, username := range users {
		if err := soulseek.ValidateUsername(username); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.shuttingDown {
		return ErrClosed
	}
	if s.community.identity != identity || accountKey(s.cfg) != identity.Account {
		return ErrCommunitySession
	}
	s.desiredUserWatchesLocked(time.Now()) // Reclaim expired leases even while offline.
	owner := "frontend:" + frontend
	if _, exists := s.community.watches[owner]; !exists && len(users) > 0 {
		frontends := 0
		for key := range s.community.watches {
			if strings.HasPrefix(key, "frontend:") {
				frontends++
			}
		}
		if frontends >= 64 {
			return errors.New("community: too many frontend watch leases")
		}
	}
	s.setUserWatchesLocked(owner, users, time.Now().Add(time.Minute))
	return nil
}

func (s *Service) setUserWatchesLocked(owner string, users []string, expiresAt time.Time) {
	if len(users) == 0 {
		delete(s.community.watches, owner)
	} else {
		s.community.watches[owner] = userWatchLease{users: slices.Clone(users), expiresAt: expiresAt}
	}
	s.desiredUserWatchesLocked(time.Now())
	select {
	case s.community.wake <- struct{}{}:
	default:
	}
}

func (s *Service) desiredUserWatchesLocked(now time.Time) map[string]uint64 {
	wanted := map[string]uint64{}
	for owner, lease := range s.community.watches {
		if !lease.expiresAt.IsZero() && !now.Before(lease.expiresAt) {
			delete(s.community.watches, owner)
			continue
		}
		for _, username := range lease.users {
			wanted[username] = 1
		}
	}
	for username := range s.community.users {
		if wanted[username] == 0 {
			delete(s.community.users, username)
		}
	}
	for username := range wanted {
		user := s.community.users[username]
		if user.watchVersion == 0 {
			s.community.nextWatch++
			user.Username, user.watchVersion = username, s.community.nextWatch
			s.community.users[username] = user
		}
		wanted[username] = user.watchVersion
	}
	return wanted
}

// One session worker serializes watch writes; the callback never waits for it.
// sent is private to that worker and starts empty after each reconnect.
func (s *Service) syncUserWatches(ctx context.Context, client *soulseek.Client, identity CommunityIdentity, sent map[string]uint64) error {
	s.mu.Lock()
	if !s.communityCurrentLocked(identity) || s.client != client {
		s.mu.Unlock()
		return ErrCommunitySession
	}
	wanted := s.desiredUserWatchesLocked(time.Now())
	s.mu.Unlock()
	var add, remove []string
	for username := range wanted {
		if sent[username] != wanted[username] {
			add = append(add, username)
		}
	}
	for username := range sent {
		if wanted[username] == 0 {
			remove = append(remove, username)
		}
	}
	slices.Sort(add)
	slices.Sort(remove)
	for _, usernames := range [][]string{remove, add} {
		for _, username := range usernames {
			s.mu.RLock()
			current := s.communityCurrentLocked(identity) && s.client == client
			s.mu.RUnlock()
			if !current {
				return ErrCommunitySession
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var err error
			if wanted[username] != 0 {
				err = client.WatchUser(writeCtx, username)
			} else {
				err = client.UnwatchUser(writeCtx, username)
			}
			cancel()
			if err != nil {
				return err
			}
			if wanted[username] != 0 {
				sent[username] = wanted[username]
			} else {
				delete(sent, username)
			}
		}
	}
	return nil
}
