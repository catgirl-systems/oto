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
	if len(users)+len(rooms) == 0 || len(users) > 0 && len(rooms) > 0 || len(users)+len(rooms) > MaxScopedSearchTargets {
		return nil, errors.New("search: invalid scoped targets")
	}
	users = slices.Clone(users)
	rooms = slices.Clone(rooms)
	for _, user := range users {
		if err := ValidateUsername(user); err != nil {
			return nil, err
		}
	}
	for _, room := range rooms {
		if err := ValidateRoomName(room); err != nil {
			return nil, err
		}
	}
	slices.Sort(users)
	users = slices.Compact(users)
	slices.Sort(rooms)
	rooms = slices.Compact(rooms)
	return c.collectSearchTargets(ctx, query, false, users, rooms)
}
