package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func waitCommunityProfile(t *testing.T, s *Service, req CommunityProfileRequest) CommunityProfile {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := s.CommunityProfile(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if out.State != "pending" {
			return out
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("profile request did not complete")
	return CommunityProfile{}
}
func TestCommunityProfilesCoalescingPicturesAndPartialFailure(t *testing.T) {
	s := downloadService(t)
	_, _, id := communityTestConnection(t, s)
	ctx := context.Background()
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	s.profileFetch = func(ctx context.Context, _ *soulseek.Client, user string) (soulseek.PeerProfile, error) {
		calls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return soulseek.PeerProfile{}, ctx.Err()
		}
		return soulseek.PeerProfile{Description: "hello 猫\x1b[2J", Picture: picture.Bytes(), UploadSlots: 3, QueueLength: 7, SlotsAvailable: true}, nil
	}
	req := CommunityProfileRequest{CommunityIdentity: id, Username: "Alice", Frontend: "one"}
	first, err := s.StartCommunityProfile(ctx, req)
	if err != nil || first.State != "pending" {
		t.Fatal(first, err)
	}
	second := req
	second.Frontend = "two"
	coalesced, err := s.StartCommunityProfile(ctx, second)
	if err != nil || coalesced.Generation != first.Generation {
		t.Fatal(coalesced, err)
	}
	close(release)
	ready := waitCommunityProfile(t, s, req)
	if ready.State != "ready" || ready.PictureType != "image/png" || ready.PictureWidth != 2 || ready.PictureHeight != 3 || ready.UploadSlots != 3 || ready.QueueLength != 7 || ready.UploadAllowedKnown || strings.ContainsRune(ready.Description, '\x1b') {
		t.Fatal(ready)
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate fetch", calls.Load())
	}
	saved, err := s.CommunityProfilePicture(ctx, CommunityProfilePictureRequest{CommunityIdentity: id, Username: "Alice", Revision: ready.PictureRevision})
	if err != nil || !bytes.Equal(saved.Data, picture.Bytes()) {
		t.Fatal(saved, err)
	}
	saved.Data[0] = 0 // Export is not a mutable alias of the cache.
	again, err := s.CommunityProfilePicture(ctx, CommunityProfilePictureRequest{CommunityIdentity: id, Username: "Alice", Revision: ready.PictureRevision})
	if err != nil || again.Data[0] == 0 {
		t.Fatal("mutable cached picture", err)
	}
	s.mu.Lock()
	s.profileFetch = func(context.Context, *soulseek.Client, string) (soulseek.PeerProfile, error) {
		return soulseek.PeerProfile{}, errors.New("private peer diagnostic")
	}
	s.mu.Unlock()
	req.Refresh = true
	if _, err := s.StartCommunityProfile(ctx, req); err != nil {
		t.Fatal(err)
	}
	stale := waitCommunityProfile(t, s, req)
	if stale.State != "stale" || stale.Description != ready.Description || strings.Contains(stale.Error, "private peer diagnostic") {
		t.Fatal("partial failure", stale)
	}
	s.mu.Lock()
	s.profileFetch = func(context.Context, *soulseek.Client, string) (soulseek.PeerProfile, error) {
		return soulseek.PeerProfile{Description: "new", Picture: picture.Bytes()}, nil
	}
	s.mu.Unlock()
	if _, err := s.StartCommunityProfile(ctx, req); err != nil {
		t.Fatal(err)
	}
	changed := waitCommunityProfile(t, s, req)
	if _, err := s.CommunityProfilePicture(ctx, CommunityProfilePictureRequest{CommunityIdentity: id, Username: "Alice", Revision: ready.PictureRevision}); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("stale picture revision", err)
	}
	s.mu.Lock()
	total := s.community.profiles.pictureBytes
	s.mu.Unlock()
	if total != picture.Len() {
		t.Fatal("replacement double-counted", total)
	}
	lower := req
	lower.Username = "alice"
	empty, err := s.CommunityProfile(ctx, lower)
	if err != nil || empty.State != "idle" || empty.Description != "" {
		t.Fatal("case-folded profile", empty, err)
	}
	s.mu.Lock()
	_, aliceWatched := s.community.users["Alice"]
	s.retireCommunityLocked()
	s.mu.Unlock()
	if !aliceWatched {
		t.Fatal("another frontend watch removed")
	}
	offline, err := s.CommunityProfile(ctx, req)
	if err != nil || offline.State != "offline" || offline.Description != changed.Description {
		t.Fatal(offline, err)
	}
}
func TestCommunityProfilesLimitsCancellationAndAccountFencing(t *testing.T) {
	s := downloadService(t)
	_, _, id := communityTestConnection(t, s)
	ctx := context.Background()
	entered := make(chan struct{}, 8)
	cancelled := make(chan struct{}, 8)
	s.profileFetch = func(ctx context.Context, _ *soulseek.Client, _ string) (soulseek.PeerProfile, error) {
		entered <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		return soulseek.PeerProfile{Description: "late"}, nil
	}
	for i := 0; i < 8; i++ {
		if _, err := s.StartCommunityProfile(ctx, CommunityProfileRequest{CommunityIdentity: id, Username: strings.Repeat("u", i+1), Frontend: "one"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request not started")
		}
	}
	if _, err := s.StartCommunityProfile(ctx, CommunityProfileRequest{CommunityIdentity: id, Username: "ninth", Frontend: "one"}); err == nil {
		t.Fatal("unbounded active requests")
	}
	s.mu.Lock()
	s.cfg.Soulseek.Username = "other"
	err := s.loadCommunityLocked(ctx)
	other := s.community.identity
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("account switch did not cancel fetch")
		}
	}
	if _, err := s.CommunityProfile(ctx, CommunityProfileRequest{CommunityIdentity: id, Username: "u", Frontend: "one"}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("old session accepted", err)
	}
	out, err := s.CommunityProfile(ctx, CommunityProfileRequest{CommunityIdentity: other, Username: "u", Frontend: "one"})
	if err != nil || out.Description != "" {
		t.Fatal("old account leaked", out, err)
	}
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.StartCommunityProfile(cancelledCtx, CommunityProfileRequest{CommunityIdentity: other, Username: "u", Frontend: "one"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, data := range [][]byte{nil, []byte("<svg onload=invalid>"), make([]byte, soulseek.MaxProfilePictureBytes+1)} {
		if kind, _, _ := profilePictureInfo(data); kind != "" {
			t.Fatal("invalid picture accepted")
		}
	}
	var large bytes.Buffer
	if err := png.Encode(&large, image.NewNRGBA(image.Rect(0, 0, 4097, 1))); err != nil {
		t.Fatal(err)
	}
	if kind, _, _ := profilePictureInfo(large.Bytes()); kind != "" {
		t.Fatal("oversized dimensions accepted")
	}
}

func TestCommunityProfilesCacheEvictionAndPictureRemoval(t *testing.T) {
	s := downloadService(t)
	_, _, id := communityTestConnection(t, s)
	ctx := context.Background()
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	picture := append(pngData.Bytes(), make([]byte, soulseek.MaxProfilePictureBytes-pngData.Len())...)
	setFetch := func(data []byte) {
		s.mu.Lock()
		s.profileFetch = func(context.Context, *soulseek.Client, string) (soulseek.PeerProfile, error) {
			return soulseek.PeerProfile{Description: "retained", Picture: data}, nil
		}
		s.mu.Unlock()
	}
	fetch := func(user int) CommunityProfile {
		t.Helper()
		req := CommunityProfileRequest{CommunityIdentity: id, Username: fmt.Sprint("user", user), Frontend: "cache-test", Refresh: true}
		if _, err := s.StartCommunityProfile(ctx, req); err != nil {
			t.Fatal(err)
		}
		return waitCommunityProfile(t, s, req)
	}
	check := func(entries, pictures, bytes int) {
		t.Helper()
		s.mu.Lock()
		defer s.mu.Unlock()
		count, total := 0, 0
		for _, entry := range s.community.profiles.entries {
			total += len(entry.picture)
			if len(entry.picture) > 0 {
				count++
			} else if entry.PictureType != "" || entry.PictureWidth != 0 || entry.PictureHeight != 0 || entry.PictureRevision != 0 {
				t.Fatal("eviction left picture metadata", entry.CommunityProfile)
			}
		}
		if len(s.community.profiles.entries) != entries || count != pictures || total != bytes || s.community.profiles.pictureBytes != total {
			t.Fatal("cache accounting", len(s.community.profiles.entries), count, total, s.community.profiles.pictureBytes)
		}
	}
	setFetch(picture)
	for i := 0; i < 6; i++ {
		if p := fetch(i); p.State != "ready" || p.PictureType != "image/png" {
			t.Fatal(p)
		}
	}
	check(6, 4, MaxCommunityProfilePictureCacheBytes)
	setFetch(nil)
	if p := fetch(5); p.PictureType != "" || p.Description != "retained" || p.Error != "" {
		t.Fatal("empty replacement", p)
	}
	check(6, 3, 3*soulseek.MaxProfilePictureBytes)
	setFetch([]byte("unsupported"))
	if p := fetch(4); p.PictureType != "" || p.Description != "retained" || p.Error == "" {
		t.Fatal("unsupported replacement", p)
	}
	check(6, 2, 2*soulseek.MaxProfilePictureBytes)
	setFetch(nil)
	for i := 6; i < 38; i++ {
		fetch(i)
	}
	check(32, 0, 0)
	missing, err := s.CommunityProfile(ctx, CommunityProfileRequest{CommunityIdentity: id, Username: "user0", Frontend: "cache-test"})
	if err != nil || missing.State != "idle" || missing.Generation != 0 || missing.Description != "" {
		t.Fatal("evicted entry remained authoritative", missing, err)
	}
	check(32, 0, 0)
}
