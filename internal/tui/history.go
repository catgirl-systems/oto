package tui

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/storage"
	storageDB "github.com/catgirl-systems/oto/internal/storage/db"
)

const (
	historySearchKind = "search"
	historyFilterKind = "filter"
	statsAccountPref  = "stats_account"
)

type historyState struct {
	StatsAccount string
	Searches     []string
	Filters      []string
}

type historyCursor struct {
	index         int
	draft         string
	skippedNewest bool
}

func loadHistory(db *storage.DB) (historyState, error) {
	var history historyState
	if db == nil {
		return history, nil
	}
	err := db.ReadSnapshot(context.Background(), func(snapshot *storage.ReadTx) error {
		queries := snapshot.Queries()
		for _, item := range []struct {
			kind string
			dst  *[]string
		}{{historySearchKind, &history.Searches}, {historyFilterKind, &history.Filters}} {
			rows, err := queries.ListHistory(context.Background(), item.kind)
			if err != nil {
				return err
			}
			values := make([]string, 0, len(rows))
			for _, row := range rows {
				if strings.TrimSpace(row.Value) != row.Value || row.Value == "" {
					return errors.New("tui: invalid history value")
				}
				if _, err := storage.DecodeUint64(row.Recency); err != nil {
					return err
				}
				values = append(values, row.Value)
			}
			*item.dst = values
		}
		pref, err := queries.GetUIPreference(context.Background(), statsAccountPref)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		history.StatsAccount = pref.Value
		return nil
	})
	return history, err
}

func normalizeHistory(items []string, limit int) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

func (h *historyState) normalize(settings config.Search) {
	h.Searches = normalizeHistory(h.Searches, settings.SearchHistoryLimit)
	h.Filters = normalizeHistory(h.Filters, settings.FilterHistoryLimit)
}

func (h *historyState) record(db *storage.DB, value string, filter bool, settings config.Search) error {
	value = strings.TrimSpace(value)
	if value == "" || filter && !settings.RememberFilters || !filter && !settings.RememberSearches {
		return nil
	}
	kind, limit := historySearchKind, settings.SearchHistoryLimit
	if filter {
		kind, limit = historyFilterKind, settings.FilterHistoryLimit
	}
	if db == nil {
		if filter {
			h.Filters = normalizeHistory(append([]string{value}, h.Filters...), limit)
		} else {
			h.Searches = normalizeHistory(append([]string{value}, h.Searches...), limit)
		}
		return nil
	}
	err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		queries := db.Queries().WithTx(tx)
		meta, err := queries.GetStateMeta(context.Background())
		if err != nil {
			return err
		}
		recency, err := storage.DecodeUint64(meta.HistorySequence)
		if err != nil {
			return err
		}
		if recency == ^uint64(0) {
			return errors.New("tui: history sequence exhausted")
		}
		recency++
		if err := queries.SetHistorySequence(context.Background(), storage.EncodeUint64(recency)); err != nil {
			return err
		}
		if err := queries.UpsertHistory(context.Background(), storageDB.UpsertHistoryParams{Kind: kind, Value: value, Recency: storage.EncodeUint64(recency)}); err != nil {
			return err
		}
		if limit > 0 {
			return queries.TrimHistory(context.Background(), storageDB.TrimHistoryParams{Kind: kind, Kind_2: kind, Offset: int64(limit)})
		}
		return nil
	})
	if err != nil {
		return err
	}
	latest, err := loadHistory(db)
	if err != nil {
		return err
	}
	latest.normalize(settings)
	*h = latest
	return nil
}

func (h *historyState) clear(db *storage.DB, filter bool, settings config.Search) error {
	kind := historySearchKind
	if filter {
		kind = historyFilterKind
	}
	if db != nil {
		if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
			return db.Queries().WithTx(tx).ClearHistoryKind(context.Background(), kind)
		}); err != nil {
			return err
		}
		latest, err := loadHistory(db)
		if err != nil {
			return err
		}
		latest.normalize(settings)
		*h = latest
		return nil
	}
	if filter {
		h.Filters = nil
	} else {
		h.Searches = nil
	}
	h.normalize(settings)
	return nil
}

func (h *historyState) applyLimits(db *storage.DB, settings config.Search) error {
	if db != nil {
		err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
			queries := db.Queries().WithTx(tx)
			for _, item := range []struct {
				kind  string
				limit int
			}{{historySearchKind, settings.SearchHistoryLimit}, {historyFilterKind, settings.FilterHistoryLimit}} {
				if item.limit == 0 {
					continue
				}
				if err := queries.TrimHistory(context.Background(), storageDB.TrimHistoryParams{Kind: item.kind, Kind_2: item.kind, Offset: int64(item.limit)}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		latest, err := loadHistory(db)
		if err != nil {
			return err
		}
		latest.normalize(settings)
		*h = latest
		return nil
	}
	h.normalize(settings)
	return nil
}

func (c *historyCursor) reset(draft string) {
	c.index, c.draft, c.skippedNewest = -1, draft, false
}

func (c *historyCursor) move(current string, items []string, older bool) (string, bool) {
	if len(items) == 0 {
		return current, false
	}
	if older {
		if c.index < 0 {
			c.draft = current
			c.index = 0
			c.skippedNewest = items[0] == c.draft
			if c.skippedNewest {
				c.index++
			}
		} else {
			c.index++
		}
		if c.index >= len(items) {
			c.index = len(items) - 1
			return current, false
		}
		return items[c.index], true
	}
	if c.index < 0 {
		return current, false
	}
	c.index--
	if c.index < 0 || c.index == 0 && c.skippedNewest {
		c.index = -1
		return c.draft, true
	}
	return items[c.index], true
}

func (m *model) initializeHistory() {
	history, err := loadHistory(m.stateDB)
	history.normalize(m.activeSearch)
	m.history = history
	if err != nil {
		m.historyErr = err.Error()
	}
}

func (m *model) recordHistory(value string, filter bool) {
	value = strings.TrimSpace(value)
	if value == "" || filter && !m.activeSearch.RememberFilters || !filter && !m.activeSearch.RememberSearches {
		return
	}
	if err := m.history.record(m.stateDB, value, filter, m.activeSearch); err != nil {
		m.historyErr = err.Error()
		return
	}
	m.historyErr = ""
}

func (m *model) clearHistory(filter bool) {
	name := "search"
	if filter {
		name = "filter"
	}
	if err := m.history.clear(m.stateDB, filter, m.activeSearch); err != nil {
		m.historyErr = err.Error()
		m.notice = ""
		return
	}
	m.historyErr = ""
	m.setNotice(name + " history cleared")
}

func (m *model) applyHistorySettings(settings config.Search) {
	m.activeSearch = settings
	m.acceptedSearchDefault = settings.DefaultFilter
	if err := m.history.applyLimits(m.stateDB, settings); err != nil {
		m.historyErr = err.Error()
		return
	}
	m.historyErr = ""
}

func (m *model) saveStatsAccount(value string) error {
	if m.stateDB == nil {
		m.history.StatsAccount = value
		return nil
	}
	if err := m.stateDB.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return m.stateDB.Queries().WithTx(tx).UpsertUIPreference(context.Background(), storageDB.UpsertUIPreferenceParams{Key: statsAccountPref, Value: value})
	}); err != nil {
		return err
	}
	m.history.StatsAccount = value
	return nil
}

func (m *model) recallHistory(older bool) {
	items := m.history.Searches
	enabled := m.activeSearch.RememberSearches
	if m.filterEditing {
		items = m.history.Filters
		enabled = m.activeSearch.RememberFilters
	}
	if !enabled {
		return
	}
	if value, ok := m.historyCursor.move(m.input, items, older); ok {
		m.input = value
		m.inputCursor = len([]rune(value))
	}
}
