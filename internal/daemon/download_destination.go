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

// setFolderDestinations preserves each file's path beneath the selected remote folder.
func setFolderDestinations(items []DownloadItem, folder, destination string) error {
	if destination == "" {
		return nil
	}
	if err := validateDownloadDestination(destination); err != nil {
		return err
	}
	destination, err := soulseek.NormalizePath(destination)
	if err != nil {
		return err
	}
	folder, err = soulseek.NormalizePath(folder)
	if err != nil {
		return err
	}
	segments := strings.Split(folder, "/")
	for i := range items {
		relative := items[i].Filename // Both queue paths supply normalized remote names.
		for _, segment := range segments {
			part, rest, ok := strings.Cut(relative, "/")
			if !ok || !strings.EqualFold(part, segment) {
				return errors.New("daemon: file is outside the selected folder")
			}
			relative = rest
		}
		items[i].Destination = destination + "/" + relative
	}
	return nil
}
