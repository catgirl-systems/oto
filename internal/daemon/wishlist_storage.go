package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/storage"
	storageDB "github.com/catgirl-systems/oto/internal/storage/db"
)

func wishlistRow(row storageDB.Wishlist) (wishlistEntry, uint64, error) {
	if row.ID == "" || !strings.HasPrefix(row.ID, "w-") {
		return wishlistEntry{}, 0, errors.New("invalid wishlist id")
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(row.ID, "w-"), 10, 64)
	if err != nil || n == 0 {
		return wishlistEntry{}, 0, errors.New("invalid wishlist id")
	}
	if row.Query == "" || strings.TrimSpace(row.Query) != row.Query {
		return wishlistEntry{}, 0, errors.New("invalid wishlist query")
	}
	if _, err := parseSearchFilter(row.Filter); err != nil {
		return wishlistEntry{}, 0, fmt.Errorf("invalid wishlist filter for %q: %w", row.Query, err)
	}
	sequence, err := storage.DecodeUint64(row.NotificationSequence)
	if err != nil {
		return wishlistEntry{}, 0, err
	}
	if row.ResultCount < 0 || row.Unread < 0 || row.Unread > 1 {
		return wishlistEntry{}, 0, errors.New("invalid wishlist metadata")
	}
	var lastRun time.Time
	if row.LastRunAt != nil {
		lastRun = unixTime(*row.LastRunAt)
	}
	return wishlistEntry{WishlistItem: WishlistItem{
		ID: row.ID, Query: row.Query, Filter: row.Filter, AddedAt: unixTime(row.AddedAt), LastRunAt: lastRun,
		ResultCount: int(row.ResultCount), Unread: row.Unread != 0, NotificationSequence: sequence,
	}, ResultSignature: row.ResultSignature, generation: 1}, n, nil
}

// loadWishlist reads rows and the wishlist-owned sequence in one pinned snapshot.
func (s *Service) loadWishlist() error {
	if s.stateDB == nil {
		return errors.New("daemon: state database is not open")
	}
	var items []wishlistEntry
	var maximum uint64
	var sequence uint64
	err := s.stateDB.ReadSnapshot(context.Background(), func(snapshot *storage.ReadTx) error {
		queries := snapshot.Queries()
		meta, err := queries.GetStateMeta(context.Background())
		if err != nil {
			return err
		}
		sequence, err = storage.DecodeUint64(meta.WishlistSequence)
		if err != nil {
			return err
		}
		rows, err := queries.ListWishlist(context.Background())
		if err != nil {
			return err
		}
		ids := make(map[string]bool, len(rows))
		queriesSeen := make(map[string]bool, len(rows))
		items = make([]wishlistEntry, 0, len(rows))
		for _, row := range rows {
			item, id, err := wishlistRow(row)
			if err != nil {
				return err
			}
			if ids[item.ID] || queriesSeen[item.Query] {
				return errors.New("duplicate wishlist item")
			}
			ids[item.ID], queriesSeen[item.Query] = true, true
			maximum = max(maximum, id)
			items = append(items, item)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("daemon: load wishlist: %w", err)
	}
	s.wishlist, s.wishlistNextID = items, max(sequence, maximum)
	return nil
}

func (s *Service) persistWishlistLocked(item *wishlistEntry, advanceSequence bool) error {
	if s.stateDB == nil {
		return errors.New("daemon: state database is not open")
	}
	row := storageDB.UpsertWishlistParams{
		ID: item.ID, Query: item.Query, Filter: item.Filter, AddedAt: item.AddedAt.UnixNano(), ResultCount: int64(item.ResultCount), ResultSignature: item.ResultSignature,
		Unread: 0, NotificationSequence: storage.EncodeUint64(item.NotificationSequence),
	}
	if item.Unread {
		row.Unread = 1
	}
	if !item.LastRunAt.IsZero() {
		last := item.LastRunAt.UnixNano()
		row.LastRunAt = &last
	}
	return s.stateDB.WriteTx(context.Background(), func(tx *sql.Tx) error {
		queries := s.stateDB.Queries().WithTx(tx)
		if err := queries.UpsertWishlist(context.Background(), row); err != nil {
			return err
		}
		if advanceSequence {
			return queries.SetWishlistSequence(context.Background(), storage.EncodeUint64(s.wishlistNextID))
		}
		return nil
	})
}

func (s *Service) deleteWishlistLocked(id string) error {
	if s.stateDB == nil {
		return errors.New("daemon: state database is not open")
	}
	return s.stateDB.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return s.stateDB.Queries().WithTx(tx).DeleteWishlist(context.Background(), id)
	})
}
