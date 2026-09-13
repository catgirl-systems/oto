package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityUploadPoliciesLiveFlagsAndPrivileges(t *testing.T) {
	s := downloadService(t)
	path := filepath.Join(t.TempDir(), "oto.json")
	s.SetConfigPath(path)
	manager := soulseek.NewUploadManager(1)
	s.client = soulseek.NewClient(soulseek.ClientConfig{Uploads: manager})
	s.community.online = true
	identity := s.community.identity
	ctx := context.Background()
	active := manager.Enqueue("active", soulseek.TransferRequest{})
	normal := manager.Enqueue("normal", soulseek.TransferRequest{Filename: "normal", Size: 1})
	buddyJob := manager.Enqueue("Buddy", soulseek.TransferRequest{Filename: "buddy", Size: 100})
	supporter := manager.Enqueue("Supporter", soulseek.TransferRequest{Filename: "supporter", Size: 50})
	assertPosition := func(job *soulseek.UploadJob, want uint32) {
		t.Helper()
		p, err := manager.Position(ctx, job)
		if err != nil || p != want {
			t.Fatal(job.User, p, want, err)
		}
	}
	buddy, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: identity, Username: "Buddy", Priority: true})
	if err != nil {
		t.Fatal(err)
	}
	assertPosition(buddyJob, 1)
	assertPosition(normal, 2)
	assertPosition(supporter, 3)
	fixtures := map[string]testutil.WireFixture{}
	for _, f := range testutil.SocialFixtures(t) {
		fixtures[f.Name] = f
	}
	update := func(name string) {
		t.Helper()
		f := fixtures[name]
		decoded, err := soulseek.DecodeServerMessage(f.Code, f.Payload(t))
		if err != nil {
			t.Fatal(err)
		}
		if err = s.communityUpdate(ctx, identity, decoded.(soulseek.SocialMessage)); err != nil {
			t.Fatal(err)
		}
	}
	update("privileged-users")
	assertPosition(supporter, 3) // Supporter priority is explicitly enabled below.
	cfg := s.cfg
	cfg.Uploads.PrioritizePrivileged = true
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	assertPosition(supporter, 2)
	assertPosition(normal, 3)
	update("supporter-expired")
	assertPosition(supporter, 3)
	if _, ok := s.community.users["Supporter"]; ok {
		t.Fatal("unwatched privilege update created a presence cache")
	}
	update("supporter-connection")
	assertPosition(supporter, 2)
	if err := s.WatchCommunityUsers(identity, "test", []string{"Supporter"}); err != nil {
		t.Fatal(err)
	}
	if user := s.community.users["Supporter"]; !user.Privileged || !user.PrivilegeFresh || user.PrivilegeUpdatedAt.IsZero() {
		t.Fatal("known supporter not hydrated", user)
	}
	buddy, err = s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: identity, Username: "Buddy", Revision: &buddy.Buddy.Revision})
	if err != nil {
		t.Fatal(err)
	}
	assertPosition(supporter, 1)
	assertPosition(buddyJob, 3)
	cfg.Uploads.PrioritizeBuddies = true
	cfg.Uploads.ExemptBuddiesFromQueueLimits = true
	cfg.Uploads.MaxQueuedFilesPerUser = 1
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	assertPosition(buddyJob, 1)
	extra, err := manager.TryEnqueue("Buddy", soulseek.TransferRequest{Filename: "extra", Size: 1})
	if err != nil {
		t.Fatal("buddy exemption not applied", err)
	}
	if _, err := s.SetCommunityBuddy(ctx, CommunityBuddyRequest{CommunityIdentity: identity, Username: "Buddy", Revision: &buddy.Buddy.Revision, Remove: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.TryEnqueue("Buddy", soulseek.TransferRequest{Filename: "rejected"}); !errors.Is(err, soulseek.ErrTooManyUploadFiles) {
		t.Fatal("removed buddy remained exempt", err)
	}
	assertPosition(active, 0)
	assertPosition(supporter, 1)
	saved, err := config.Load(path)
	if err != nil || saved.Uploads != cfg.Uploads {
		t.Fatal("preferences not persisted", err)
	}
	s.SetConfigPath(filepath.Join(path, "blocked"))
	failed := cfg
	failed.Uploads.PrioritizePrivileged = false
	if err := s.UpdateConfig(failed); err == nil {
		t.Fatal("invalid configuration destination accepted")
	}
	assertPosition(supporter, 1)
	s.SetConfigPath(path)
	s.mu.Lock()
	s.retireCommunityLocked()
	s.mu.Unlock()
	assertPosition(normal, 1)
	if len(s.community.privileged) != 0 {
		t.Fatal("privileges retained across disconnect")
	}
	if err := s.communityUpdate(ctx, identity, soulseek.PrivilegedUsers{Users: []string{"late"}}); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("late privileges accepted", err)
	}
	for _, job := range []*soulseek.UploadJob{active, normal, buddyJob, supporter, extra} {
		manager.Done(job)
	}
}

func TestCommunityUploadPrivilegeBudgetAndExactIdentity(t *testing.T) {
	s := downloadService(t)
	s.client = soulseek.NewClient(soulseek.ClientConfig{})
	s.community.online = true
	identity := s.community.identity
	ctx := context.Background()
	for _, message := range []soulseek.SocialMessage{soulseek.PrivilegedUsers{Users: []string{"Alice", "alice"}}, soulseek.UserPresence{Username: "Alice", Status: soulseek.UserStatusOnline}} {
		if err := s.communityUpdate(ctx, identity, message); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.community.privileged) != 1 || s.community.privileged["alice"].IsZero() {
		t.Fatal("privilege identities folded", s.community.privileged)
	}
	s.mu.Lock()
	s.community.privileged = make(map[string]time.Time, soulseek.MaxPrivilegedUsers)
	for i := 0; i < soulseek.MaxPrivilegedUsers; i++ {
		s.community.privileged[time.Unix(int64(i), 0).String()] = time.Now()
	}
	s.mu.Unlock()
	if err := s.communityUpdate(ctx, identity, soulseek.PrivilegedUsers{Users: []string{"new"}}); !errors.Is(err, soulseek.ErrTooLarge) {
		t.Fatal("privilege budget", err)
	}
	if len(s.community.privileged) != soulseek.MaxPrivilegedUsers {
		t.Fatal("partial oversized roster published")
	}
}
