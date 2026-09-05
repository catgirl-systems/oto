package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/catgirl-systems/oto/internal/config"
)

const (
	settingUploadFileCap settingID = 100 + iota
	settingUploadByteCap
	settingAudioMetadata
	settingDownloadFilters
	settingDownloadRule
	settingAddDownloadRule
	settingRestoreDownloadRules
	settingStatsLogRetention
	settingStatsDailyRetention
	settingStatsASCII
	settingStatsPrune
)

func (m model) downloadFilterFields() []settingField {
	fields := []settingField{{settingDownloadFilters, "Filename filters", strconv.FormatBool(m.cfg.Downloads.FiltersEnabled), settingBool}}
	for i, rule := range m.cfg.Downloads.FilterPatterns {
		fields = append(fields, settingField{settingDownloadRule, fmt.Sprintf("Filter %d", i+1), rule, settingText})
	}
	return append(fields, settingField{settingAddDownloadRule, "Add filename filter", "", settingText}, settingField{settingRestoreDownloadRules, "Restore filter defaults", "Press Enter", settingAction})
}
func (m model) downloadRuleIndex() int {
	n := 0
	for i, field := range m.settingFields() {
		if i == m.cursor {
			if field.id == settingDownloadRule {
				return n
			}
			return -1
		}
		if field.id == settingDownloadRule {
			n++
		}
	}
	return -1
}
func parseByteLimit(value string) (uint64, error) {
	parts := strings.Fields(value)
	if len(parts) < 1 || len(parts) > 2 {
		return 0, errors.New("use integer bytes or an integer with B, KiB, MiB, GiB, TiB")
	}
	n, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	unit := uint64(1)
	if len(parts) == 2 {
		switch strings.ToUpper(parts[1]) {
		case "B":
		case "KIB":
			unit = 1 << 10
		case "MIB":
			unit = 1 << 20
		case "GIB":
			unit = 1 << 30
		case "TIB":
			unit = 1 << 40
		default:
			return 0, errors.New("unknown byte unit")
		}
	}
	if n > ^uint64(0)/unit {
		return 0, errors.New("byte limit overflows uint64")
	}
	return n * unit, nil
}
func (m *model) setDownloadRule(value string, add bool) error {
	rules := append([]string{}, m.cfg.Downloads.FilterPatterns...)
	if add {
		rules = append(rules, value)
	} else {
		i := m.downloadRuleIndex()
		if i < 0 {
			return errors.New("select a filter")
		}
		rules[i] = value
	}
	rules, err := config.NormalizeDownloadFilters(rules)
	if err != nil {
		return err
	}
	m.cfg.Downloads.FilterPatterns = rules
	return nil
}
