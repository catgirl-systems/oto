package soulseek

import (
	"context"
	"fmt"
	"net"
)

// Race established connections, not just dial attempts: one failed route must
// not cancel the other, and every losing or late socket belongs to this function.
func racePeerConnections(ctx context.Context, direct, reverse func(context.Context) (net.Conn, error), connected func(string)) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		peer  net.Conn
		err   error
		route string
	}
	results := make(chan result)
	for route, connect := range map[string]func(context.Context) (net.Conn, error){"direct": direct, "reverse": reverse} {
		go func() {
			peer, err := connect(ctx)
			select {
			case results <- result{peer, err, route}:
			case <-ctx.Done():
				if peer != nil {
					_ = peer.Close()
				}
			}
		}()
	}
	failures := make(map[string]error, 2)
	for range 2 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-results:
			if r.err == nil && ctx.Err() == nil {
				if connected != nil {
					connected(r.route)
				}
				return r.peer, nil
			}
			if r.peer != nil {
				_ = r.peer.Close()
			}
			failures[r.route] = r.err
		}
	}
	return nil, fmt.Errorf("direct: %v; indirect: %w", failures["direct"], failures["reverse"])
}
