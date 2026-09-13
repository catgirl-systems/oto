package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

var ErrReceivingDenied = errors.New("receiving unavailable: check account, consent, bans, and queue limits")

// The opaque ID prefix durably marks received files using the existing journal.
// StatsAccount is the immutable admitting account for these downloads.
func receivedDownload(d Download) bool { return strings.HasPrefix(d.ID, "d-received-") }

type receivedOffer struct {
	identity CommunityIdentity
	epoch    uint64
	offer    *soulseek.DownloadOffer
}

func receivedUserDirectory(username string) string {
	name := url.PathEscape(username)
	if name == "." || name == ".." || len(name) > 200 {
		sum := sha256.Sum256([]byte(username))
		// PathEscape always escapes !, keeping hashed names in a disjoint namespace.
		return fmt.Sprintf("!%x", sum)
	}
	return name
}
func (s *Service) receivingConsentLocked(account, username string) bool {
	if account == "" || account != accountKey(s.cfg) || username == "" || username == s.cfg.Soulseek.Username {
		return false
	}
	buddy, exists := s.community.buddies[username]
	return s.cfg.Receiving[account].Allows(username, exists, buddy.Trusted)
}
func (s *Service) receivingPermissionLocked(account, username string, address netip.Addr) error {
	if !s.receivingConsentLocked(account, username) {
		return ErrReceivingDenied
	}
	rule, unresolved := s.communityRuleLocked("ban", username, address)
	if rule != nil || unresolved {
		return ErrReceivingDenied
	}
	return nil
}
func (s *Service) queueReceivedOffer(identity CommunityIdentity, epoch uint64, offer *soulseek.DownloadOffer) error {
	rows, err := s.queueDownloadsWithOffer(context.Background(), nil, nil, &receivedOffer{identity: identity, epoch: epoch, offer: offer})
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].State == "filtered" {
		return ErrReceivingDenied
	}
	return nil
}
func (s *Service) prepareReceivedRequestLocked(ctx context.Context, r *receivedOffer) ([]DownloadRequest, error) {
	if err := s.checkCommunityIdentityLocked(ctx, r.identity); err != nil {
		return nil, err
	}
	o := r.offer
	if s.shuttingDown || s.client == nil || s.uploadEpoch != r.epoch || len(s.receivedOffers) >= 128 {
		return nil, ErrReceivingDenied
	}
	if err := s.receivingPermissionLocked(r.identity.Account, o.Username(), o.Address()); err != nil {
		return nil, err
	}
	if !s.receivingCapacityLocked(r.identity.Account, o.Username()) {
		return nil, ErrReceivingDenied
	}
	for _, d := range s.journal.Downloads {
		if d.Username != o.Username() || strings.ReplaceAll(d.Filename, "\\", "/") != o.Filename() {
			continue
		}
		switch d.State {
		case "queued", "incomplete", "running", "finalizing", "retrying", "paused":
			return nil, ErrReceivingDenied
		case "completed":
			if receivedDownload(d) && d.StatsAccount == r.identity.Account && d.Size == o.Size() {
				return nil, ErrReceivingDenied
			}
		}
	}
	return []DownloadRequest{{Username: o.Username(), DownloadDir: s.receivingSettingsLocked().EffectiveDirectory, Files: []DownloadItem{{Filename: o.Filename(), Size: o.Size(), Destination: receivedUserDirectory(o.Username()) + "/" + o.Filename()}}}}, nil
}

func (s *Service) receivingCapacityLocked(account, username string) bool {
	total, peer := 0, 0
	for _, d := range s.journal.Downloads {
		if !receivedDownload(d) || d.StatsAccount != account {
			continue
		}
		switch d.State {
		case "queued", "incomplete", "running", "finalizing", "retrying", "paused":
			total++
			if d.Username == username {
				peer++
			}
		}
	}
	return total < 128 && peer < 16
}
func (s *Service) receivingAuthorization(ctx context.Context, client *soulseek.Client, d Download) func(netip.Addr) error {
	return func(address netip.Addr) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.closed || s.shuttingDown || s.client != client {
			return ErrReceivingDenied
		}
		if err := s.receivingPermissionLocked(d.StatsAccount, d.Username, address); err != nil {
			return err
		}
		if s.receivedAddresses == nil {
			s.receivedAddresses = make(map[string]netip.Addr)
		}
		s.receivedAddresses[d.ID] = address
		return nil
	}
}

// Stop streams immediately even if saving their paused state subsequently fails.
func (s *Service) revalidateReceivedDownloads() {
	s.mu.Lock()
	var ids []string
	for _, d := range s.journal.Downloads {
		if !receivedDownload(d) {
			continue
		}
		switch d.State {
		case "queued", "incomplete", "running", "retrying":
		default:
			continue
		}
		if s.receivingPermissionLocked(d.StatsAccount, d.Username, s.receivedAddresses[d.ID]) == nil {
			continue
		}
		if cancel := s.downloadCancels[d.ID]; cancel != nil {
			cancel()
		}
		ids = append(ids, d.ID)
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.TransferAction(id, "pause")
	}
}
