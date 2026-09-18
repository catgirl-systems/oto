package config

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Receiving is account-scoped. Empty Mode disables unsolicited receiving.
type Receiving struct {
	Mode            string   `json:"mode"`
	Users           []string `json:"users,omitempty"`
	Directory       string   `json:"directory,omitempty"`
	CompletionHooks bool     `json:"completion_hooks"`
}

func (r Receiving) Validate() error {
	if !slices.Contains([]string{"", "off", "users", "buddies", "trusted"}, r.Mode) {
		return errors.New("receiving mode must be off, users, buddies, or trusted; everyone is not supported")
	}
	if len(r.Users) > 256 {
		return errors.New("receiving allows at most 256 explicit usernames")
	}
	seen := make(map[string]bool, len(r.Users))
	budget := 0
	for _, user := range r.Users {
		budget += len(user)
		if user == "" || strings.TrimSpace(user) != user || len(user) > 1024 || !utf8.ValidString(user) || seen[user] || strings.ContainsFunc(user, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) {
			return errors.New("receiving usernames must be unique exact valid names, at most 1024 bytes each, without controls")
		}
		seen[user] = true
	}
	if budget > 64<<10 {
		return errors.New("receiving usernames exceed 64 KiB")
	}
	if r.Directory != "" && (!filepath.IsAbs(r.Directory) || !utf8.ValidString(r.Directory) || len(r.Directory) > 4096 || strings.ContainsFunc(r.Directory, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) })) {
		return errors.New("received-files directory must be an absolute path without controls")
	}
	return nil
}

// Allows expresses consent only. The caller must still enforce bans, identity,
// filters, storage limits and session ownership before accepting a transfer.
func (r Receiving) Allows(username string, buddy, trusted bool) bool {
	switch r.Mode {
	case "users":
		return slices.Contains(r.Users, username)
	case "buddies":
		return buddy
	case "trusted":
		return buddy && trusted
	default:
		return false
	}
}
