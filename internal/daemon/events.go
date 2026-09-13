package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func (s *Service) consumeClientEvents(ctx context.Context, client *soulseek.Client) {
	s.mu.RLock()
	identity, wake := s.community.identity, s.community.wake
	s.mu.RUnlock()
	heldCtx, cancelHeld := context.WithCancel(ctx)
	heldDone := make(chan struct{})
	go func() { defer close(heldDone); s.resolveCommunityHeld(heldCtx, client, identity) }()
	defer func() { cancelHeld(); <-heldDone }()
	sent := map[string]uint64{}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	refresh := true
	for {
		if refresh {
			err := s.syncUserWatches(ctx, client, identity, sent)
			if err == nil {
				err = s.syncCommunityRooms(ctx, client, identity)
			}
			if err == nil {
				err = s.syncCommunityOutbox(ctx, client, identity)
			}
			if err == nil {
				err = s.syncCommunityInterests(ctx, client, identity)
			}
			if err != nil {
				if ctx.Err() == nil {
					s.event(slog.LevelWarn, "community_sync_failed", err)
				}
				_ = client.Close() // Interrupted writes require a new session.
				return
			}
			refresh = false
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
			refresh = true
		case <-ticker.C:
			refresh = true
		case event := <-client.Events():
			switch message := event.Message.(type) {
			case soulseek.WishlistInterval:
				s.mu.Lock()
				if s.client == client {
					s.wishlistServerInterval = time.Duration(message.Seconds) * time.Second
				}
				s.mu.Unlock()
				s.wakeWishlist()
			}
		}
	}
}
