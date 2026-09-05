package soulseek

import (
	"errors"
	"strings"
	"unicode"
)

const ServerUserSearch uint32 = 42

type UserSearchRequest struct {
	Username string
	Token    uint32
	Query    string
}

func (UserSearchRequest) command() uint32 { return ServerUserSearch }
func (m UserSearchRequest) encode(e *Encoder) error {
	if err := e.String(m.Username); err != nil {
		return err
	}
	e.U32(m.Token)
	return e.String(m.Query)
}

func NormalizeSearchUsers(users []string) ([]string, error) {
	if len(users) > 32 {
		return nil, errors.New("search: maximum 32 target users")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, user := range users {
		if strings.IndexFunc(user, unicode.IsControl) >= 0 {
			return nil, errors.New("search: invalid target username")
		}
		user = strings.TrimSpace(user)
		if user == "" || len(user) > 1024 || strings.IndexFunc(user, unicode.IsControl) >= 0 {
			return nil, errors.New("search: invalid target username")
		}
		if !seen[user] {
			seen[user] = true
			out = append(out, user)
		}
	}
	return out, nil
}
