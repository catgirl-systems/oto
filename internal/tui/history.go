package tui

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/catgirl-systems/oto/internal/config"
)

type historyState struct {
	Searches []string `json:"searches"`
	Filters  []string `json:"filters"`
}

type historyCursor struct {
	index         int
	draft         string
	skippedNewest bool
}

func loadHistory(path string) (historyState, error) {
	var history historyState
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return history, nil
	}
	if err != nil {
		return history, err
	}
	if err := json.Unmarshal(data, &history); err != nil {
		return historyState{}, err
	}
	return history, nil
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

func prependHistory(items []string, value string) []string {
	return append([]string{value}, items...)
}

func (h *historyState) record(path, value string, filter bool, settings config.Search) error {
	value = strings.TrimSpace(value)
	if value == "" || filter && !settings.RememberFilters || !filter && !settings.RememberSearches {
		return nil
	}

	current := *h
	if filter {
		current.Filters = prependHistory(current.Filters, value)
	} else {
		current.Searches = prependHistory(current.Searches, value)
	}
	current.normalize(settings)
	*h = current
	if path == "" {
		return nil
	}

	disk, err := loadHistory(path)
	if err != nil {
		return err
	}
	if filter {
		disk.Filters = prependHistory(disk.Filters, value)
	} else {
		disk.Searches = prependHistory(disk.Searches, value)
	}
	disk.normalize(settings)
	*h = disk
	return config.SaveJSON(path, disk)
}

func (h *historyState) clear(path string, filter bool, settings config.Search) error {
	next, err := loadHistory(path)
	if err != nil {
		next = *h
	}
	if filter {
		next.Filters = nil
	} else {
		next.Searches = nil
	}
	next.normalize(settings)
	*h = next
	if path == "" {
		return nil
	}
	return config.SaveJSON(path, next)
}

func (h *historyState) applyLimits(path string, settings config.Search) error {
	if path == "" {
		h.normalize(settings)
		return nil
	}
	next, err := loadHistory(path)
	if err != nil {
		h.normalize(settings)
		return err
	}
	next.normalize(settings)
	*h = next
	return config.SaveJSON(path, next)
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
	history, err := loadHistory(m.historyPath)
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
	if err := m.history.record(m.historyPath, value, filter, m.activeSearch); err != nil {
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
	if err := m.history.clear(m.historyPath, filter, m.activeSearch); err != nil {
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
	if err := m.history.applyLimits(m.historyPath, settings); err != nil {
		m.historyErr = err.Error()
		return
	}
	m.historyErr = ""
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
