package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"slices"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

const (
	MaxCommunityProfileActiveRequests    = 8
	MaxCommunityProfilePictureCacheBytes = 8 << 20
	MaxCommunityProfilePictureDimension  = 4096
)

type communityProfileState struct {
	entries      map[string]*communityProfileEntry
	generation   uint64
	pictureBytes int
}
type communityProfileEntry struct {
	CommunityProfile
	picture []byte
	cancel  context.CancelFunc
	used    time.Time
}
type CommunityProfile struct {
	CommunityIdentity
	Username           string        `json:"username"`
	User               CommunityUser `json:"user"`
	Description        string        `json:"description"`
	PictureType        string        `json:"picture_type,omitempty"`
	PictureWidth       int           `json:"picture_width,omitempty"`
	PictureHeight      int           `json:"picture_height,omitempty"`
	PictureRevision    uint64        `json:"picture_revision"`
	UploadSlots        uint32        `json:"upload_slots"`
	QueueLength        uint32        `json:"queue_length"`
	SlotsAvailable     bool          `json:"slots_available"`
	UploadAllowed      uint32        `json:"upload_allowed"`
	UploadAllowedKnown bool          `json:"upload_allowed_known"`
	State              string        `json:"state"`
	Error              string        `json:"error,omitempty"`
	Generation         uint64        `json:"generation"`
	Revision           uint64        `json:"revision"`
	UpdatedAt          time.Time     `json:"updated_at"`
}
type CommunityProfileRequest struct {
	CommunityIdentity
	Username string `json:"username"`
	Frontend string `json:"frontend"`
	Refresh  bool   `json:"refresh"`
}
type CommunityProfilePictureRequest struct {
	CommunityIdentity
	Username string `json:"username"`
	Revision uint64 `json:"revision"`
}
type CommunityProfilePicture struct {
	CommunityIdentity
	Username    string `json:"username"`
	ContentType string `json:"content_type"`
	Revision    uint64 `json:"revision"`
	Data        []byte `json:"data"`
}

func newCommunityProfileState() communityProfileState {
	return communityProfileState{entries: map[string]*communityProfileEntry{}}
}
func (s *Service) retireProfilesLocked() {
	for _, entry := range s.community.profiles.entries {
		if entry.cancel != nil {
			entry.cancel()
			entry.cancel = nil
		}
		entry.State, entry.Error = "offline", "connection ended; refresh to query again"
	}
}
func validProfileRequest(req CommunityProfileRequest) error {
	if err := soulseek.ValidateUsername(req.Username); err != nil {
		return err
	}
	if req.Frontend == "" || len(req.Frontend) > 128 || strings.ContainsAny(req.Frontend, "\r\n\x00") {
		return errors.New("community: invalid profile frontend")
	}
	return nil
}

// One visible inspector per frontend. Other consumers keep independent leases.
func (s *Service) profileWatchLocked(frontend, username string) error {
	now := time.Now()
	s.desiredUserWatchesLocked(now)
	owner := "profile:" + frontend
	frontends := 0
	for key := range s.community.watches {
		if strings.HasPrefix(key, "profile:") {
			frontends++
		}
	}
	if _, ok := s.community.watches[owner]; !ok && frontends >= 64 {
		return errors.New("community: too many profile frontends")
	}
	s.setUserWatchesLocked(owner, []string{username}, now.Add(time.Minute))
	return nil
}
func (s *Service) profileViewLocked(id CommunityIdentity, username string) CommunityProfile {
	out := CommunityProfile{CommunityIdentity: id, Username: username, State: "idle"}
	if entry := s.community.profiles.entries[username]; entry != nil {
		out = entry.CommunityProfile
		out.CommunityIdentity = id
		entry.used = time.Now()
	}
	out.User = s.community.users[username]
	out.User.Username = username
	if !s.community.online {
		out.State = "offline"
	}
	out.Revision = s.community.revision
	return out
}
func (s *Service) CommunityProfile(ctx context.Context, req CommunityProfileRequest) (CommunityProfile, error) {
	if err := validProfileRequest(req); err != nil {
		return CommunityProfile{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityProfile{}, err
	}
	if err := s.profileWatchLocked(req.Frontend, req.Username); err != nil {
		return CommunityProfile{}, err
	}
	return s.profileViewLocked(req.CommunityIdentity, req.Username), nil
}
func (s *Service) StartCommunityProfile(ctx context.Context, req CommunityProfileRequest) (CommunityProfile, error) {
	if err := validProfileRequest(req); err != nil {
		return CommunityProfile{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityProfile{}, err
	}
	if !s.communityCurrentLocked(req.CommunityIdentity) {
		return CommunityProfile{}, soulseek.ErrNotConnected
	}
	if err := s.profileWatchLocked(req.Frontend, req.Username); err != nil {
		return CommunityProfile{}, err
	}
	d := &s.community.profiles
	entry := d.entries[req.Username]
	if entry != nil && (entry.cancel != nil || !req.Refresh && entry.State != "offline") {
		return s.profileViewLocked(req.CommunityIdentity, req.Username), nil
	}
	active := 0
	for _, p := range d.entries {
		if p.cancel != nil {
			active++
		}
	}
	if active >= MaxCommunityProfileActiveRequests {
		return CommunityProfile{}, errors.New("community: profile request limit reached; retry later")
	}
	if entry == nil {
		if len(d.entries) >= 32 {
			var oldest *communityProfileEntry
			for _, p := range d.entries {
				if p.cancel == nil && (oldest == nil || p.used.Before(oldest.used)) {
					oldest = p
				}
			}
			if oldest == nil {
				return CommunityProfile{}, errors.New("community: profile cache is busy")
			}
			d.pictureBytes -= len(oldest.picture)
			delete(d.entries, oldest.Username)
		}
		entry = &communityProfileEntry{CommunityProfile: CommunityProfile{Username: req.Username}}
		d.entries[req.Username] = entry
	}
	d.generation++
	entry.Generation = d.generation
	entry.State, entry.Error = "pending", ""
	entry.used = time.Now()
	fetchCtx, cancel := context.WithTimeout(s.scanCtx, 15*time.Second)
	entry.cancel = cancel
	client, fetch, generation := s.client, s.profileFetch, entry.Generation
	if fetch == nil {
		fetch = func(ctx context.Context, c *soulseek.Client, user string) (soulseek.PeerProfile, error) {
			// Watch hydration does not include supporter status. Request it without
			// waiting for a reply, so missing server metadata cannot hide peer info.
			_ = c.RequestUserStatus(ctx, user)
			return c.ProfileUser(ctx, user)
		}
	}
	s.community.revision++
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		p, err := fetch(fetchCtx, client, req.Username)
		if err == nil {
			err = fetchCtx.Err()
		}
		kind, width, height := "", 0, 0
		if err == nil && len(p.Picture) > 0 {
			kind, width, height = profilePictureInfo(p.Picture)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.communityCurrentLocked(req.CommunityIdentity) || s.client != client || s.community.profiles.entries[req.Username] != entry || entry.Generation != generation {
			return
		}
		entry.cancel = nil
		s.community.revision++
		if err != nil {
			entry.State, entry.Error = "failed", "profile request failed; refresh to retry"
			if !entry.UpdatedAt.IsZero() {
				entry.State = "stale"
			}
			return
		}
		entry.Description = communityDisplayText(p.Description) // Legacy decoding may expand UTF-8; keep all bounded wire text.
		entry.UploadSlots, entry.QueueLength, entry.SlotsAvailable = p.UploadSlots, p.QueueLength, p.SlotsAvailable
		entry.UploadAllowed, entry.UploadAllowedKnown = p.UploadAllowed, p.UploadAllowedKnown
		entry.State, entry.Error, entry.UpdatedAt = "ready", "", time.Now()
		d := &s.community.profiles
		d.pictureBytes -= len(entry.picture)
		entry.picture = nil
		entry.PictureType = ""
		entry.PictureRevision = 0
		entry.PictureWidth, entry.PictureHeight = 0, 0
		if len(p.Picture) == 0 {
			return
		}
		if kind == "" {
			entry.Error = "picture format or dimensions unsupported"
			return
		}
		for d.pictureBytes+len(p.Picture) > MaxCommunityProfilePictureCacheBytes {
			var oldest *communityProfileEntry
			for _, candidate := range d.entries {
				if len(candidate.picture) > 0 && (oldest == nil || candidate.used.Before(oldest.used)) {
					oldest = candidate
				}
			}
			if oldest == nil {
				entry.Error = "picture cache full"
				return
			}
			d.pictureBytes -= len(oldest.picture)
			oldest.picture = nil
			oldest.PictureType = ""
			oldest.PictureRevision = 0
			oldest.PictureWidth, oldest.PictureHeight = 0, 0
		}
		entry.picture = slices.Clone(p.Picture)
		d.pictureBytes += len(entry.picture)
		entry.PictureType, entry.PictureWidth, entry.PictureHeight, entry.PictureRevision = kind, width, height, generation
	}()
	return s.profileViewLocked(req.CommunityIdentity, req.Username), nil
}

// Pictures are fetched explicitly from the reviewed cached revision, never via
// an implicit peer refresh, external program or server-side destination path.
func (s *Service) CommunityProfilePicture(ctx context.Context, req CommunityProfilePictureRequest) (CommunityProfilePicture, error) {
	if err := soulseek.ValidateUsername(req.Username); err != nil {
		return CommunityProfilePicture{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityProfilePicture{}, err
	}
	entry := s.community.profiles.entries[req.Username]
	if entry == nil || len(entry.picture) == 0 {
		return CommunityProfilePicture{}, errors.New("community: no cached picture; refresh the profile")
	}
	if req.Revision != entry.PictureRevision {
		return CommunityProfilePicture{}, fmt.Errorf("community: picture changed; review the profile again: %w", ErrCommunityMessageState)
	}
	entry.used = time.Now()
	return CommunityProfilePicture{req.CommunityIdentity, req.Username, entry.PictureType, entry.PictureRevision, slices.Clone(entry.picture)}, nil
}
func profilePictureInfo(data []byte) (string, int, int) {
	if len(data) > soulseek.MaxProfilePictureBytes {
		return "", 0, 0
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > MaxCommunityProfilePictureDimension || config.Height > MaxCommunityProfilePictureDimension {
		return "", 0, 0
	}
	kind := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif"}[format]
	return kind, config.Width, config.Height
}
