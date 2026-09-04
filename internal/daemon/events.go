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
			case soulseek.TransferEvent:
				id := "upload:" + message.Username + ":" + message.Filename
				s.mu.Lock()
				s.transfers[id] = Transfer{ID: id, Username: message.Username, Filename: message.Filename, Direction: message.Direction, State: message.State, Done: message.Done, Total: message.Total, Error: message.Error}
				s.mu.Unlock()
			}
		}
	}
}
