package daemon

import (
	"context"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func (s *Service) consumeClientEvents(ctx context.Context, client *soulseek.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-client.Events():
			transfer, ok := event.Message.(soulseek.TransferEvent)
			if !ok {
				continue
			}
			id := "upload:" + transfer.Username + ":" + transfer.Filename
			s.mu.Lock()
			s.transfers[id] = Transfer{ID: id, Username: transfer.Username, Filename: transfer.Filename, Direction: transfer.Direction, State: transfer.State, Done: transfer.Done, Total: transfer.Total, Error: transfer.Error}
			s.mu.Unlock()
		}
	}
}
