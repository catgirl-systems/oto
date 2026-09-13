package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCommunityBrowseExactIdentityAndLegacyArchives(t *testing.T) {
	for _, paged := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "paged"}[paged], func(t *testing.T) {
			s := remoteShareService(t)
			s.client = &soulseek.Client{}
			s.fullBrowse = func(_ context.Context, _ *soulseek.Client, user string, _ func(uint64, uint64)) ([]soulseek.ShareEntry, error) {
				return []soulseek.ShareEntry{{Name: user, Directory: true}}, nil
			}
			s.fullBrowseDirectories = func(_ context.Context, _ *soulseek.Client, user string, _ func(uint64, uint64)) ([]soulseek.ShareDirectory, error) {
				return []soulseek.ShareDirectory{{Name: user}}, nil
			}
			open := func(user string) (uint64, bool, error) {
				if paged {
					p, e := s.OpenBrowse(context.Background(), user, "", "")
					return p.Revision, p.Cached, e
				}
				p, e := s.BrowseComplete(context.Background(), user)
				return p.Revision, p.Cached, e
			}
			upper, cached, err := open("Alice")
			if err != nil || cached {
				t.Fatal(upper, cached, err)
			}
			lower, cached, err := open("alice")
			if err != nil || cached || lower == upper {
				t.Fatal(lower, cached, err)
			}
			if _, err := s.SaveBrowse("ALICE", upper); !errors.Is(err, ErrBrowseNotLoaded) {
				t.Fatal("folded live identity", err)
			}
			if _, err := s.SaveBrowse("Alice", lower); !errors.Is(err, ErrBrowseRevision) {
				t.Fatal("cross-user revision", err)
			}
			if _, err := s.SaveBrowse("Alice", upper); err != nil {
				t.Fatal("upper identity overwritten", err)
			}
			if _, err := s.SaveBrowse("alice", lower); err != nil {
				t.Fatal("lower identity overwritten", err)
			}
			if paged {
				for user, revision := range map[string]uint64{"Alice": upper, "alice": lower} {
					p, err := s.BrowsePage(context.Background(), BrowsePageRequest{Username: user, Revision: revision})
					if err != nil || len(p.Entries) != 1 || p.Entries[0].Name != user {
						t.Fatal("cross-user page", p, err)
					}
				}
			}
			s.mu.Lock()
			s.retireCommunityLocked()
			s.client = nil
			s.mu.Unlock()
			if _, err := s.SaveBrowse("Alice", upper); !errors.Is(err, ErrBrowseNotLoaded) {
				t.Fatal("retired live cache", err)
			}
			archives, err := s.SavedBrowses()
			if err != nil || len(archives) != 1 || archives[0].Username != "alice" {
				t.Fatal("legacy archive changed", archives, err)
			}
			_, cached, err = open("ALICE")
			if err != nil || !cached {
				t.Fatal("archive presented as live", cached, err)
			}
		})
	}
}

func TestCommunityBrowseRetiresLateResults(t *testing.T) {
	for _, paged := range []bool{false, true} {
		for _, event := range []string{"disconnect", "account-roundtrip", "cancel"} {
			t.Run(map[bool]string{false: "complete/", true: "paged/"}[paged]+event, func(t *testing.T) {
				s := remoteShareService(t)
				s.client = &soulseek.Client{}
				started := make(chan func(uint64, uint64), 1)
				release := make(chan struct{})
				defer close(release)
				s.fullBrowse = func(_ context.Context, _ *soulseek.Client, _ string, progress func(uint64, uint64)) ([]soulseek.ShareEntry, error) {
					started <- progress
					<-release
					return []soulseek.ShareEntry{{Name: "Old", Directory: true}}, nil
				}
				s.fullBrowseDirectories = func(ctx context.Context, c *soulseek.Client, u string, p func(uint64, uint64)) ([]soulseek.ShareDirectory, error) {
					_, err := s.fullBrowse(ctx, c, u, p)
					return []soulseek.ShareDirectory{{Name: "Old"}}, err
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					var err error
					if paged {
						_, err = s.OpenBrowse(ctx, "Alice", "", "")
					} else {
						_, err = s.BrowseComplete(ctx, "Alice")
					}
					done <- err
				}()
				var progress func(uint64, uint64)
				select {
				case progress = <-started:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				want := ErrBrowseRevision
				if event == "cancel" {
					cancel()
					want = context.Canceled
				} else {
					if event == "disconnect" {
						s.mu.Lock()
						s.retireCommunityLocked()
						s.mu.Unlock()
					} else {
						s.SetConfigPath(filepath.Join(t.TempDir(), "oto.toml"))
						s.mu.Lock()
						original := s.cfg
						s.client = nil
						s.mu.Unlock()
						other := original
						other.Soulseek.Username = "other-account"
						if err := s.UpdateConfig(other); err != nil {
							t.Fatal(err)
						}
						if err := s.UpdateConfig(original); err != nil {
							t.Fatal(err)
						}
					}
					progress(100, 100)
					if p := s.BrowseProgress("Alice"); p != nil {
						t.Fatal("retired progress", p)
					}
				}
				release <- struct{}{}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatal("late response", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("late browse stalled")
				}
				s.mu.RLock()
				loaded := s.browses["Alice"]
				s.mu.RUnlock()
				if loaded.result.Revision != 0 || loaded.snapshot != nil {
					t.Fatal("late snapshot published", loaded)
				}
			})
		}
	}
}

func TestCommunityBrowseAdmissionAndPublicationGuards(t *testing.T) {
	s := remoteShareService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.rememberBrowse(ctx, "Alice", nil, false, time.Time{}, 0); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled publication", err)
	}
	if len(s.browses) != 0 {
		t.Fatal("cancelled snapshot published")
	}
	source := loadedBrowse{username: "Alice", result: BrowseResult{Revision: 1}, snapshot: &browseSnapshot{}}
	s.browses["Alice"] = source
	loaded, err := s.loadedBrowse("Alice", 1)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.retireCommunityLocked()
	s.mu.Unlock()
	reqs := []DownloadRequest{{Username: "Alice", Files: []DownloadItem{{Filename: "song", Size: 1}}}}
	if _, err := s.queueDownloads(context.Background(), reqs, &loaded); !errors.Is(err, ErrBrowseRevision) {
		t.Fatal("retired selection admitted", err)
	}
	s.browses["Alice"] = source
	if _, err := s.queueDownloads(ctx, reqs, &source); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled selection admitted", err)
	}
	if len(s.Downloads()) != 0 {
		t.Fatal("rejected selection persisted")
	}
}
