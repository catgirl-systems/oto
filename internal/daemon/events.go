package daemon

import (
	"context"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func (s *Service) consumeClientEvents(ctx context.Context, client *soulseek.Client) {
	for {
		select {
		case <-ctx.Done():
			return
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
