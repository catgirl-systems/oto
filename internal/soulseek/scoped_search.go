package soulseek

import (
	"context"
	"errors"
	"slices"
)

const ServerRoomSearch uint32 = 120
const MaxScopedSearchTargets = 100000

type RoomSearchRequest struct {
	Room  string
	Token uint32
	Query string
}

func (RoomSearchRequest) command() uint32 { return ServerRoomSearch }
func (m RoomSearchRequest) encode(e *Encoder) error {
	if err := ValidateRoomName(m.Room); err != nil {
		return err
	}
	e.String(m.Room)
	e.U32(m.Token)
	e.String(m.Query)
	return nil
}

// SearchScoped searches an explicitly captured nonempty target set. It never
// converts an empty or unsupported scope into a global search.
func (c *Client) SearchScoped(ctx context.Context, query string, users, rooms []string) ([]SearchResult, error) {
	users, rooms, err := normalizeScopedTargets(users, rooms)
	if err != nil {
		return nil, err
	}
	return c.collectSearchTargets(ctx, query, false, users, rooms)
}

// StreamSearch sends one global (no targets) or scoped search and hands each
// response's matches to emit until ctx ends. Peers keep answering long after
// the first seconds, especially through indirect connections, so streaming
// keeps results a blocking five-second search would drop.
func (c *Client) StreamSearch(ctx context.Context, query string, users, rooms []string, emit func([]SearchResult)) error {
	if emit == nil {
		return errors.New("search: nil result callback")
	}
	if len(users)+len(rooms) > 0 {
		var err error
		if users, rooms, err = normalizeScopedTargets(users, rooms); err != nil {
			return err
		}
	}
	_, err := c.collectSearchStream(ctx, query, false, users, rooms, 0, emit)
	return err
}

func normalizeScopedTargets(users, rooms []string) ([]string, []string, error) {
	if len(users)+len(rooms) == 0 || len(users) > 0 && len(rooms) > 0 || len(users)+len(rooms) > MaxScopedSearchTargets {
		return nil, nil, errors.New("search: invalid scoped targets")
	}
	users = slices.Clone(users)
	rooms = slices.Clone(rooms)
	for _, user := range users {
		if err := ValidateUsername(user); err != nil {
			return nil, nil, err
		}
	}
	for _, room := range rooms {
		if err := ValidateRoomName(room); err != nil {
			return nil, nil, err
		}
	}
	slices.Sort(users)
	users = slices.Compact(users)
	slices.Sort(rooms)
	rooms = slices.Compact(rooms)
	return users, rooms, nil
}
