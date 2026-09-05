package config

import (
	"fmt"
	"strings"
	"unicode"
)

var defaultShareExclusions = []string{
	".*", ".*/", "@eaDir/", "#recycle/", "#snapshot/", "desktop.ini", "Thumbs.db",
	"System Volume Information/", "$RECYCLE.BIN/", "lost+found/", "*.part", "*.partial",
	"*.crdownload", "*.tmp", "*.temp", "*.bak", "*~",
}

// DefaultShareExclusions returns a newly allocated copy of the built-in rules.
func DefaultShareExclusions() []string { return append([]string(nil), defaultShareExclusions...) }

// NormalizeShareExclusions validates rules and normalizes Windows separators.
// A nil input selects the defaults; a non-nil empty input stays non-nil empty.
func NormalizeShareExclusions(rules []string) ([]string, error) {
	if rules == nil {
		rules = DefaultShareExclusions()
	}
	if len(rules) > 256 {
		return nil, fmt.Errorf("config: too many share exclusion rules")
	}
	out := make([]string, len(rules))
	for i, rule := range rules {
		rule = strings.ReplaceAll(rule, `\`, "/")
		if len(rule) == 0 || strings.TrimSpace(rule) == "" || len(rule) > 1024 || strings.IndexFunc(rule, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("config: invalid share exclusion rule %q", rule)
		}
		if strings.HasPrefix(rule, "/") || len(rule) >= 3 && rule[1] == ':' && rule[2] == '/' && ((rule[0] >= 'A' && rule[0] <= 'Z') || (rule[0] >= 'a' && rule[0] <= 'z')) {
			return nil, fmt.Errorf("config: absolute share exclusion rule %q", rule)
		}
		base := strings.TrimSuffix(strings.TrimSuffix(rule, "/*"), "/")
		for _, component := range strings.Split(base, "/") {
			if component == "." || component == ".." {
				return nil, fmt.Errorf("config: traversal in share exclusion rule %q", rule)
			}
		}
		out[i] = rule
	}
	return out, nil
}
