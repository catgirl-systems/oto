package daemon

import (
	"errors"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

func validateDownloadName(name string) error {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("download filename must be a nonempty name without separators, traversal or control characters")
	}
	return nil
}

// DownloadAsDestination changes only the basename of the normal per-user destination.
func DownloadAsDestination(username, filename, name string) (string, error) {
	if err := validateDownloadName(name); err != nil {
		return "", err
	}
	remote, err := soulseek.NormalizePath(filename)
	if err != nil {
		return "", err
	}
	return path.Join(safeSegment(username), path.Dir(remote), name), nil
}

func validateDownloadDestination(destination string) error {
	if !utf8.ValidString(destination) || strings.IndexFunc(destination, unicode.IsControl) >= 0 {
		return errors.New("download destination contains invalid text or control characters")
	}
	// Validate before path cleaning, so a trailing separator or dot is not hidden.
	parts := strings.ReplaceAll(destination, "\\", "/")
	return validateDownloadName(parts[strings.LastIndexByte(parts, '/')+1:])
}
