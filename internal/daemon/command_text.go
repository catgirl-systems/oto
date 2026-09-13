package daemon

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ParseCommand accepts quoted arguments and backslash escapes, not shell syntax.
// A leading slash is optional. Operators, dollar signs and backticks are data;
// nothing is evaluated, expanded from the environment, or launched as a process.
func ParseCommand(text string) (string, []string, error) {
	if len(text) > 128<<10 || !utf8.ValidString(text) {
		return "", nil, errors.New("command text is invalid or oversized")
	}
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "/")
	var words []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	finish := func() {
		if started {
			words = append(words, word.String())
			word.Reset()
			started = false
		}
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\t' {
			return "", nil, errors.New("command contains control characters")
		}
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
			started = true
		case r == '\\':
			escaped = true
			started = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			started = true
		case unicode.IsSpace(r):
			finish()
		default:
			word.WriteRune(r)
			started = true
		}
		if len(words) > 128 {
			return "", nil, errors.New("command exceeds argument limits")
		}
	}
	if quote != 0 || escaped {
		return "", nil, errors.New("unfinished command quote or escape")
	}
	finish()
	if len(words) == 0 {
		return "", nil, errors.New("enter a command")
	}
	if len(words) > 129 || len(words[0]) > 64 {
		return "", nil, errors.New("command exceeds argument limits")
	}
	return words[0], words[1:], nil
}
