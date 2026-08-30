package daemon

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var ErrInvalidFilter = errors.New("daemon: invalid search filter")

func ValidateSearchFilter(expression string) error {
	_, err := parseSearchFilter(expression)
	return err
}

type numberCondition struct {
	op    string
	value uint64
}

type searchFilter struct {
	include, exclude        []*regexp.Regexp
	includeTypes, denyTypes []string
	sizes, bitrates         []numberCondition
	durations               []numberCondition
	free, public            *bool
}

var fileTypeCategories = map[string]map[string]bool{
	"audio":      extensionSet("aac", "aiff", "alac", "ape", "flac", "m4a", "mp3", "ogg", "opus", "wav", "wma"),
	"video":      extensionSet("avi", "flv", "m4v", "mkv", "mov", "mp4", "mpeg", "mpg", "webm", "wmv"),
	"image":      extensionSet("avif", "bmp", "gif", "heic", "jpeg", "jpg", "png", "svg", "tif", "tiff", "webp"),
	"document":   extensionSet("csv", "doc", "docx", "epub", "html", "md", "odt", "pdf", "ppt", "pptx", "rtf", "xls", "xlsx"),
	"text":       extensionSet("csv", "log", "md", "nfo", "srt", "sub", "text", "txt"),
	"archive":    extensionSet("7z", "bz2", "gz", "rar", "tar", "tgz", "xz", "zip"),
	"executable": extensionSet("apk", "appimage", "deb", "dmg", "exe", "jar", "msi", "pkg", "rpm"),
}

func extensionSet(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func parseSearchFilter(expression string) (searchFilter, error) {
	var filter searchFilter
	tokens, err := splitFilter(expression)
	if err != nil {
		return filter, err
	}
	for _, token := range tokens {
		field, value, ok := strings.Cut(token, ":")
		if !ok || value == "" {
			return filter, invalidFilter("expected field:value near %q", token)
		}
		switch strings.ToLower(field) {
		case "in", "out":
			re, compileErr := regexp.Compile("(?i)" + value)
			if compileErr != nil {
				return filter, invalidFilter("%s regex: %v", field, compileErr)
			}
			if strings.EqualFold(field, "in") {
				filter.include = append(filter.include, re)
			} else {
				filter.exclude = append(filter.exclude, re)
			}
		case "type":
			for _, item := range strings.Split(value, ",") {
				item = strings.ToLower(strings.TrimSpace(item))
				denied := strings.HasPrefix(item, "!")
				item = strings.TrimPrefix(strings.TrimPrefix(item, "!"), ".")
				if item == "" || (!validExtension(item) && fileTypeCategories[item] == nil) {
					return filter, invalidFilter("unknown file type %q", item)
				}
				if denied {
					filter.denyTypes = append(filter.denyTypes, item)
				} else {
					filter.includeTypes = append(filter.includeTypes, item)
				}
			}
		case "size":
			condition, parseErr := parseCondition(value, parseSize)
			if parseErr != nil {
				return filter, invalidFilter("size: %v", parseErr)
			}
			filter.sizes = append(filter.sizes, condition)
		case "bitrate":
			condition, parseErr := parseCondition(value, parseUnsigned)
			if parseErr != nil {
				return filter, invalidFilter("bitrate: %v", parseErr)
			}
			filter.bitrates = append(filter.bitrates, condition)
		case "duration":
			condition, parseErr := parseCondition(value, parseDuration)
			if parseErr != nil {
				return filter, invalidFilter("duration: %v", parseErr)
			}
			filter.durations = append(filter.durations, condition)
		case "free":
			parsed, parseErr := strconv.ParseBool(value)
			if parseErr != nil {
				return filter, invalidFilter("free must be true or false")
			}
			filter.free = &parsed
		case "public":
			parsed, parseErr := strconv.ParseBool(value)
			if parseErr != nil {
				return filter, invalidFilter("public must be true or false")
			}
			filter.public = &parsed
		default:
			return filter, invalidFilter("unknown field %q", field)
		}
	}
	return filter, nil
}

func splitFilter(expression string) ([]string, error) {
	var tokens []string
	var token strings.Builder
	var quote rune
	for _, r := range strings.TrimSpace(expression) {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
		case quote == 0 && (r == ' ' || r == '\t' || r == '\n'):
			if token.Len() > 0 {
				tokens = append(tokens, token.String())
				token.Reset()
			}
		default:
			token.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, invalidFilter("unclosed quote")
	}
	if token.Len() > 0 {
		tokens = append(tokens, token.String())
	}
	return tokens, nil
}

func parseCondition(value string, parseValue func(string) (uint64, error)) (numberCondition, error) {
	condition := numberCondition{op: ">="}
	for _, op := range []string{"<=", ">=", "!=", "==", "<", ">", "=", "!"} {
		if strings.HasPrefix(value, op) {
			condition.op, value = op, strings.TrimSpace(strings.TrimPrefix(value, op))
			break
		}
	}
	parsed, err := parseValue(value)
	if err != nil {
		return condition, err
	}
	condition.value = parsed
	return condition, nil
}

func parseUnsigned(value string) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(value), 10, 64)
}

func parseSize(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	match := regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)([kmgt]?i?b)?$`).FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("invalid value %q", value)
	}
	amount, _ := strconv.ParseFloat(match[1], 64)
	units := map[string]float64{"": 1, "b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40}
	multiplier, ok := units[strings.ToLower(match[2])]
	if !ok || amount > math.MaxUint64/multiplier {
		return 0, fmt.Errorf("invalid value %q", value)
	}
	return uint64(amount * multiplier), nil
}

func parseDuration(value string) (uint64, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("invalid value %q", value)
	}
	var total uint64
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil || (i > 0 && n >= 60) {
			return 0, fmt.Errorf("invalid value %q", value)
		}
		if total > (math.MaxUint64-n)/60 {
			return 0, fmt.Errorf("invalid value %q", value)
		}
		total = total*60 + n
	}
	return total, nil
}

func (filter searchFilter) matches(result SearchResult) bool {
	name := result.Username + "/" + result.Path
	for _, re := range filter.include {
		if !re.MatchString(name) {
			return false
		}
	}
	for _, re := range filter.exclude {
		if re.MatchString(name) {
			return false
		}
	}
	extension := strings.ToLower(strings.TrimPrefix(result.Extension, "."))
	if extension == "" {
		extension = strings.TrimPrefix(strings.ToLower(filepath.Ext(result.Path)), ".")
	}
	if len(filter.includeTypes) > 0 && !matchesAnyType(extension, filter.includeTypes) {
		return false
	}
	if matchesAnyType(extension, filter.denyTypes) {
		return false
	}
	if !matchesConditions(result.Size, filter.sizes) || !matchesConditions(uint64(result.Bitrate), filter.bitrates) || !matchesConditions(uint64(result.Duration), filter.durations) {
		return false
	}
	if filter.free != nil && result.SlotFree != *filter.free {
		return false
	}
	return filter.public == nil || result.Public == *filter.public
}

func matchesAnyType(extension string, types []string) bool {
	for _, value := range types {
		if extension == value || fileTypeCategories[value][extension] {
			return true
		}
	}
	return false
}

func matchesConditions(value uint64, conditions []numberCondition) bool {
	for _, condition := range conditions {
		matched := false
		switch condition.op {
		case "<":
			matched = value < condition.value
		case "<=":
			matched = value <= condition.value
		case "=", "==":
			matched = value == condition.value
		case "!=", "!":
			matched = value != condition.value
		case ">=":
			matched = value >= condition.value
		case ">":
			matched = value > condition.value
		}
		if !matched {
			return false
		}
	}
	return true
}

func validExtension(value string) bool {
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return value != ""
}

func invalidFilter(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidFilter, fmt.Sprintf(format, args...))
}
