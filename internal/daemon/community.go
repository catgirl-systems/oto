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
// Session advances on account changes and uses the connection-generation counter.
type CommunityIdentity struct {
	Account string `json:"account"`
	Daemon  string `json:"daemon"` // Random process identity fences an attached TUI across restarts.
	Session uint64 `json:"session"`
}

type CommunityUser struct {
	Username           string              `json:"username"`
	Exists             bool                `json:"exists"`
	Status             soulseek.UserStatus `json:"status"`
	Privileged         bool                `json:"privileged"`
	PrivilegeFresh     bool                `json:"privilege_fresh"`
	PrivilegeUpdatedAt time.Time           `json:"privilege_updated_at"`
	Country            string              `json:"country"`
	IP                 string              `json:"ip"`
	Port               uint32              `json:"port"`
	Stats              soulseek.UserStats  `json:"stats"`
	StatusFresh        bool                `json:"status_fresh"`
	StatsFresh         bool                `json:"stats_fresh"`
	AddressFresh       bool                `json:"address_fresh"`
	StatusUpdatedAt    time.Time           `json:"status_updated_at"`
	StatsUpdatedAt     time.Time           `json:"stats_updated_at"`
	AddressUpdatedAt   time.Time           `json:"address_updated_at"`
	LastSeen           time.Time           `json:"last_seen"`
	watchVersion       uint64              // A recreated cache needs hydration even before an unwatch was sent.
}

type userWatchLease struct {
	users     []string
	expiresAt time.Time // Zero for daemon-owned buddies, conversations and rooms.
}

// All fields are protected by Service.mu. Network writes always happen outside it.
type communityState struct {
	identity                                           CommunityIdentity
	online                                             bool
	revision                                           uint64
	nextWatch                                          uint64
	users                                              map[string]CommunityUser
	privileged                                         map[string]time.Time
	watches                                            map[string]userWatchLease
	wake                                               chan struct{}
	rooms                                              map[string]*communityRoomState
	directory                                          map[string]communityRoomListing
	directoryFresh, directoryRefresh, directoryPending bool
	directoryDeadline                                  time.Time
	feedWanted, feedWritten                            bool
	feed                                               []CommunityFeedMessage
	feedID                                             int64
	feedBytes                                          int
	invitationsWanted                                  bool
	invitationsWritten, invitationsConfirmed           *bool
	invitationsDeadline                                time.Time
	wallBytes                                          int
	buddies                                            map[string]CommunityBuddy
	buddyNotification                                  DownloadNotification
	discovery                                          communityDiscoveryState
	profiles                                           communityProfileState
	rules                                              []CommunityRule
	ignoreAddresses                                    map[string]communityIgnoreAddress
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
	next := communityState{identity: CommunityIdentity{Account: account, Daemon: s.community.identity.Daemon}, users: map[string]CommunityUser{}, watches: map[string]userWatchLease{}, wake: make(chan struct{}, 1), profiles: newCommunityProfileState()}
	next.buddies = make(map[string]CommunityBuddy)
	err := s.stateDB.ReadSnapshot(ctx, func(tx *storage.ReadTx) error {
		q := tx.Queries()
		settings, err := q.GetCommunityAccount(ctx, account)
		if err != nil {
			return err
		}
		next.revision = uint64(settings.Revision)
		next.invitationsWanted = settings.AcceptInvitations != 0
		if err := loadCommunityDiscovery(ctx, q, settings, &next); err != nil {
			return err
		}
		next.rules, err = loadCommunityRules(ctx, q, account)
		if err != nil {
			return err
		}
		if err := loadCommunityRooms(ctx, q, account, &next); err != nil {
			return err
		}
		var buddies []string
		for after := ""; ; {
			rows, err := q.ListCommunityBuddies(ctx, db.ListCommunityBuddiesParams{Account: account, AfterUsername: after, PageSize: 200})
			if err != nil {
				return err
			}
			for _, row := range rows {
				buddies = append(buddies, row.Username)
				next.buddies[row.Username] = communityBuddyFromRow(row, next.revision)
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
				} else if row.Kind == "room" {
					r := next.rooms[row.Target]
					if r == nil {
						r = &communityRoomState{}
						next.rooms[row.Target] = r
					}
					r.conversationID = row.ID
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
	// Account changes must fence offline requests too, including A -> B -> A.
	s.uploadEpoch++
	next.identity.Session = s.uploadEpoch
	s.retireProfilesLocked()
	clear(s.browses)
	clear(s.browseProgress)
	s.community = next
	s.desiredUserWatchesLocked(time.Now())
	return nil
}

func (s *Service) communityCurrentLocked(identity CommunityIdentity) bool {
	// Incoming authority stays live while uploads drain; only new work is frozen.
	return !s.closed && s.client != nil && s.community.online && s.community.identity == identity && accountKey(s.cfg) == identity.Account
}

func (s *Service) retireCommunityLocked() {
	s.community.online = false
	clear(s.community.ignoreAddresses)
	clear(s.community.privileged)
	s.applyUploadUserPoliciesLocked()
	s.retireCommunityRoomsLocked()
	s.retireDiscoveryLocked()
	s.retireProfilesLocked()
	clear(s.browses)
	clear(s.browseProgress)
	for username, user := range s.community.users {
		user.StatusFresh, user.StatsFresh, user.AddressFresh = false, false, false
		user.PrivilegeFresh = false
		s.community.users[username] = user
	}
	s.community.revision++
	// A local disconnect says nothing about when any buddy was last online.
}

func (s *Service) communityUpdate(ctx context.Context, identity CommunityIdentity, message soulseek.SocialMessage) error {
	if private, ok := message.(soulseek.PrivateMessage); ok {
		return s.receiveCommunityPrivate(ctx, identity, private)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.communityCurrentLocked(identity) {
		return ErrCommunitySession
	}
	switch message.(type) {
	case soulseek.PrivilegedUsers, soulseek.ConnectPeerInstruction:
		_, err := s.updateUploadPrivilegesLocked(message)
		return err
	case soulseek.RoomDirectory, soulseek.RoomJoined, soulseek.RoomLeft, soulseek.RoomUserJoined, soulseek.RoomUserLeft, soulseek.RoomMessage:
		return s.updateCommunityRoomLocked(ctx, message)
	case soulseek.RoomRoleList, soulseek.RoomRoleUpdate, soulseek.RoomInvitations, soulseek.RoomWallSnapshot, soulseek.RoomWallUpdate:
		return s.updateCommunityRoomRolesLocked(ctx, message)
	}
	if response, ok := message.(soulseek.DiscoveryResponse); ok {
		s.applyDiscoveryLocked(response)
		return nil
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
		_, err := s.updateUploadPrivilegesLocked(message)
		return err
	} // Late unwatch replies and unsolicited users aren't cached.
	now := time.Now().UTC()
	buddyOnline := false
	switch m := message.(type) {
	case soulseek.WatchUserResponse:
		user.Exists, user.Status, user.Stats, user.Country = m.Exists, m.Status, m.Stats, m.Country
		user.StatusFresh, user.StatsFresh = true, m.Exists
		user.StatusUpdatedAt = now
		if m.Exists {
			user.StatsUpdatedAt = now
		}
		if !m.Exists || m.Status == soulseek.UserStatusOffline {
			user.AddressFresh = false
			delete(s.community.ignoreAddresses, username)
		}
	case soulseek.UserPresence:
		buddyOnline = user.StatusFresh && user.Status == soulseek.UserStatusOffline && m.Status != soulseek.UserStatusOffline
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
		user.PrivilegeFresh, user.PrivilegeUpdatedAt = true, now
		if m.Status == soulseek.UserStatusOffline {
			user.AddressFresh = false
			delete(s.community.ignoreAddresses, username)
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
	if _, err := s.updateUploadPrivilegesLocked(message); err != nil {
		return err
	}
	if buddyOnline {
		s.notifyBuddyOnlineLocked(username)
	}
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
			s.community.revision++
		}
	}
	for username := range wanted {
		user := s.community.users[username]
		if user.watchVersion == 0 {
			s.community.nextWatch++
			user.Username, user.watchVersion = username, s.community.nextWatch
			if observed, ok := s.community.privileged[username]; ok {
				user.Privileged, user.PrivilegeFresh, user.PrivilegeUpdatedAt = true, s.community.online, observed
			}
			s.community.revision++
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
	if s.shuttingDown {
		s.mu.Unlock()
		return nil
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
			draining := s.shuttingDown
			s.mu.RUnlock()
			if draining {
				return nil
			}
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
