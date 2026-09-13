package daemon

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

type communityIgnoreAddress struct {
	address netip.Addr
	expires time.Time
}

func (s *Service) communityIgnoreAddressLocked(username string) netip.Addr {
	if user := s.community.users[username]; user.AddressFresh {
		if address, err := netip.ParseAddr(user.IP); err == nil && !address.IsUnspecified() {
			return address.Unmap()
		}
	}
	if cached := s.community.ignoreAddresses[username]; time.Now().Before(cached.expires) {
		return cached.address
	}
	return netip.Addr{}
}

// Walls and feed remain bounded, non-durable caches. Filter them at the API
// boundary as well, so changing an ignore rule immediately hides cached content.
func (s *Service) nextTransientIgnoreSenderLocked(after, next string) string {
	hasIPRule := false
	for _, rule := range s.community.rules {
		if rule.Action == "ignore" && rule.Kind == "ip" {
			hasIPRule = true
			break
		}
	}
	if !hasIPRule {
		return next
	}
	consider := func(username string) {
		if username <= after || next != "" && username >= next || username == s.cfg.Soulseek.Username {
			return
		}
		if ignored, held := s.communityIgnoreLocked(username); !ignored && held {
			next = username
		}
	}
	for _, message := range s.community.feed {
		consider(message.Sender)
	}
	for _, room := range s.community.rooms {
		for username := range room.wall {
			consider(username)
		}
	}
	return next
}
func (s *Service) communityIgnoreLocked(username string) (ignored, held bool) {
	address := s.communityIgnoreAddressLocked(username)
	rule, unresolved := s.communityRuleLocked("ignore", username, address)
	return rule != nil, unresolved
}

// A single session-owned worker resolves held senders without blocking the server
// reader or watch writes. The durable queue survives restart; the cursor prevents
// a nonresponsive sender from starving later senders. No automatic replies run
// when releasing held messages, which may now be old or offline replays.
func (s *Service) resolveCommunityHeld(ctx context.Context, client *soulseek.Client, identity CommunityIdentity) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	after := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.mu.RLock()
		if !s.communityCurrentLocked(identity) || s.client != client {
			s.mu.RUnlock()
			return
		}
		sender, err := s.stateDB.Queries().NextHeldCommunitySender(ctx, db.NextHeldCommunitySenderParams{Account: identity.Account, AfterSender: after})
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		if err == nil {
			sender = s.nextTransientIgnoreSenderLocked(after, sender)
		}
		ignored, unresolved := s.communityIgnoreLocked(sender)
		s.mu.RUnlock()
		if err != nil {
			continue
		}
		if sender == "" {
			after = ""
			continue
		}
		after = sender
		var address netip.Addr
		if !ignored && unresolved {
			lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			resolved, err := client.ResolveUserAddress(lookupCtx, sender)
			cancel()
			if err != nil {
				continue
			}
			address, err = netip.ParseAddr(resolved.IP)
			if err != nil || address.IsUnspecified() {
				continue
			}
		}
		if err := s.releaseCommunityHeld(ctx, identity, sender, address); err != nil && ctx.Err() == nil {
			// Leave durable content held. A later pass retries storage failures.
			continue
		}
	}
}

func (s *Service) releaseCommunityHeld(ctx context.Context, identity CommunityIdentity, sender string, address netip.Addr) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.communityCurrentLocked(identity) {
		return ErrCommunitySession
	}
	if !address.IsValid() {
		address = s.communityIgnoreAddressLocked(sender)
	}
	if address.IsValid() && !address.IsUnspecified() {
		if s.community.ignoreAddresses == nil {
			s.community.ignoreAddresses = make(map[string]communityIgnoreAddress)
		}
		if len(s.community.ignoreAddresses) >= 200 {
			for username := range s.community.ignoreAddresses {
				delete(s.community.ignoreAddresses, username)
				break
			}
		}
		s.community.ignoreAddresses[sender] = communityIgnoreAddress{address.Unmap(), time.Now().Add(5 * time.Minute)}
		s.community.revision++
	}
	rule, unresolved := s.communityRuleLocked("ignore", sender, address)
	if unresolved {
		return nil
	}
	private := false
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		rows, err := q.ListHeldCommunityMessages(ctx, db.ListHeldCommunityMessagesParams{Account: identity.Account, Sender: sender, PageSize: 200})
		if err != nil || len(rows) == 0 {
			return err
		}
		if rule == nil {
			for _, row := range rows {
				// Allocate a new ordering ID, so read markers advanced while this
				// message was hidden cannot suppress its eventual unread state.
				if _, err := q.InsertCommunityMessage(ctx, db.InsertCommunityMessageParams{Account: identity.Account, ConversationID: row.ConversationID, Sender: sender, Direction: row.Direction, Body: row.Body, CreatedAt: row.CreatedAt, ServerTime: row.ServerTime, State: "received", Mention: row.Mention}); err != nil {
					return err
				}
				conversation, err := q.GetCommunityConversation(ctx, db.GetCommunityConversationParams{Account: identity.Account, ID: row.ConversationID})
				if err != nil {
					return err
				}
				if conversation.Kind == "private" {
					if _, err := q.SetCommunityConversationClosed(ctx, db.SetCommunityConversationClosedParams{Account: identity.Account, ID: conversation.ID}); err != nil {
						return err
					}
					private = true
				}
			}
		}
		if _, err := q.DeleteHeldCommunityMessages(ctx, db.DeleteHeldCommunityMessagesParams{Account: identity.Account, Sender: sender, ThroughID: rows[len(rows)-1].ID}); err != nil {
			return err
		}
		remaining, err := q.ListHeldCommunityMessages(ctx, db.ListHeldCommunityMessagesParams{Account: identity.Account, Sender: sender, PageSize: 1})
		if err != nil {
			return err
		}
		if len(remaining) == 0 {
			disposition := "stored"
			if rule != nil {
				disposition = "discarded"
			}
			if _, err := q.ResolveHeldCommunityReceipts(ctx, db.ResolveHeldCommunityReceiptsParams{Account: identity.Account, Sender: sender, Disposition: disposition}); err != nil {
				return err
			}
		}
		_, err = q.BumpCommunityRevision(ctx, identity.Account)
		return err
	})
	if err == nil && private {
		s.watchConversationLocked(sender, true)
	}
	return err
}
