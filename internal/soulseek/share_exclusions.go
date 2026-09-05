package soulseek

import (
	"regexp"
	"strings"

	"github.com/catgirl-systems/oto/internal/config"
)

// ShareExclusions is immutable. F/D tags keep file rules separate from folders.
type ShareExclusions struct {
	pattern *regexp.Regexp
	rules   []string
}

func NewShareExclusions(rules []string) (*ShareExclusions, error) {
	rules, err := config.NormalizeShareExclusions(rules)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		tag := "F"
		if strings.HasSuffix(rule, "/*") {
			tag, rule = "D", strings.TrimSuffix(rule, "/*")
		} else if strings.HasSuffix(rule, "/") {
			tag, rule = "D", strings.TrimSuffix(rule, "/")
		}
		parts = append(parts, tag+`(?:.*/)?`+strings.ReplaceAll(regexp.QuoteMeta(rule), `\*`, `.*`))
	}
	e := &ShareExclusions{rules: rules}
	if len(parts) > 0 {
		e.pattern, err = regexp.Compile(`(?is)^(?:` + strings.Join(parts, "|") + `)$`)
	}
	return e, err
}

// Excluded checks the entry and folder ancestors, never the explicit share root.
func (e *ShareExclusions) Excluded(virtual string, directory bool) bool {
	return e.excluded(virtual, directory, true)
}

// DownloadExcluded also applies folder rules to remote top-level folders.
func (e *ShareExclusions) DownloadExcluded(virtual string) bool {
	return e.excluded(virtual, false, false)
}

func (e *ShareExclusions) excluded(virtual string, directory, exemptRoot bool) bool {
	if e == nil || e.pattern == nil {
		return false
	}
	virtual = strings.TrimSuffix(strings.ReplaceAll(virtual, `\`, "/"), "/")
	if !directory {
		if e.pattern.MatchString("F" + virtual) {
			return true
		}
		if i := strings.LastIndexByte(virtual, '/'); i >= 0 {
			virtual = virtual[:i]
		} else {
			return false
		}
	}
	for i := strings.LastIndexByte(virtual, '/'); i >= 0; i = strings.LastIndexByte(virtual, '/') {
		if e.pattern.MatchString("D" + virtual) {
			return true
		}
		virtual = virtual[:i]
	}
	if !exemptRoot && virtual != "" {
		return e.pattern.MatchString("D" + virtual)
	}
	return false
}
