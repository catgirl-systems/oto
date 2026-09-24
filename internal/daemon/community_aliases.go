package daemon

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/catgirl-systems/oto/internal/storage/db"
)

const MaxAliasBytes = 4096
const MaxAliasDepth = 8

type CommunityAlias struct {
	Name      string `json:"name"`
	Expansion string `json:"expansion"`
	Revision  string `json:"revision"`
}
type CommunityAliasesRequest struct {
	CommunityIdentity
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}
type CommunityAliasesPage struct {
	CommunityIdentity
	Aliases    []CommunityAlias `json:"aliases"`
	Revision   uint64           `json:"revision"`
	NextCursor string           `json:"next_cursor"`
}
type CommunityAliasRequest struct {
	CommunityIdentity
	Name      string `json:"name"`
	Expansion string `json:"expansion"`
	Revision  string `json:"revision"` // Empty creates only; use the loaded content revision to edit/remove.
	Remove    bool   `json:"remove"`
	Confirm   bool   `json:"confirm"`
}

func validCommandName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func builtinCommandName(name string) bool {
	if name == "help" || name == "exit" || name == "quit" {
		return true
	}
	for _, command := range commandBuiltins() {
		if command.spec.Name == name {
			return true
		}
	}
	return false
}
func aliasFromRow(row db.CommunityAlias) CommunityAlias {
	return CommunityAlias{Name: row.Name, Expansion: row.Expansion, Revision: fmt.Sprintf("%x", sha256.Sum256([]byte(row.Expansion)))}
}
func (s *Service) CommunityAliases(ctx context.Context, req CommunityAliasesRequest) (CommunityAliasesPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := CommunityAliasesPage{CommunityIdentity: req.CommunityIdentity, Aliases: []CommunityAlias{}, Revision: s.community.revision}
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return out, err
	}
	limit, err := communityPageLimit(req.Limit)
	if err != nil {
		return out, err
	}
	if req.Cursor != "" && !validCommandName(req.Cursor) {
		return out, errors.New("invalid alias cursor")
	}
	rows, err := s.stateDB.Queries().ListCommunityAliases(ctx, db.ListCommunityAliasesParams{Account: req.Account, AfterName: req.Cursor, PageSize: limit})
	if err != nil {
		return out, err
	}
	aliases, _, err := takePage(func(yield func(CommunityAlias) bool) {
		for _, row := range rows {
			if !yield(aliasFromRow(row)) {
				return
			}
		}
	}, int(limit), 64<<10) // Measured JSON stays within the IPC page budget.
	if err != nil {
		return out, err
	}
	out.Aliases = aliases
	if len(out.Aliases) > 0 && (len(rows) == int(limit) || len(out.Aliases) < len(rows)) {
		out.NextCursor = out.Aliases[len(out.Aliases)-1].Name
	}
	return out, nil
}
func (s *Service) SetCommunityAlias(ctx context.Context, req CommunityAliasRequest) (CommunityAlias, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityAlias{}, err
	}
	if !validCommandName(req.Name) || builtinCommandName(req.Name) {
		return CommunityAlias{}, errors.New("alias name must be 1–64 lowercase letters, digits, hyphens or underscores, and cannot replace a builtin")
	}
	if req.Remove && !req.Confirm {
		return CommunityAlias{}, errors.New("confirm alias removal")
	}
	if !req.Remove {
		if len(req.Expansion) > MaxAliasBytes {
			return CommunityAlias{}, errors.New("alias expansion exceeds 4096 bytes")
		}
		name, templates, err := ParseCommand(req.Expansion)
		if err != nil {
			return CommunityAlias{}, err
		}
		if !validCommandName(name) {
			return CommunityAlias{}, errors.New("alias command name must be literal")
		}
		for _, template := range templates {
			if _, err := expandAliasArgument(template, make([]string, 128)); err != nil {
				return CommunityAlias{}, err
			}
		}
	}
	var out CommunityAlias
	var revision int64
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		old, err := q.GetCommunityAlias(ctx, db.GetCommunityAliasParams{Account: req.Account, Name: req.Name})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && aliasFromRow(old).Revision != req.Revision || errors.Is(err, sql.ErrNoRows) && req.Revision != "" {
			return fmt.Errorf("%w: alias changed; reload", ErrCommunityMessageState)
		}
		if req.Remove {
			if err != nil {
				return err
			}
			_, err = q.DeleteCommunityAlias(ctx, db.DeleteCommunityAliasParams{Account: req.Account, Name: req.Name})
		} else {
			err = q.PutCommunityAlias(ctx, db.PutCommunityAliasParams{Account: req.Account, Name: req.Name, Expansion: req.Expansion})
			out = aliasFromRow(db.CommunityAlias{Name: req.Name, Expansion: req.Expansion})
		}
		if err != nil {
			return err
		}
		revision, err = q.BumpCommunityRevision(ctx, req.Account)
		return err
	})
	if err == nil {
		s.community.revision = uint64(revision)
	}
	return out, err
}

// Expand parameters only after parsing the template into arguments. Parameter
// values remain one argument, never executable command text. $* is rest text,
// $1..$128 positional arguments, and $$ a literal dollar sign.
func expandAliasArgument(template string, args []string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(template); {
		if template[i] != '$' {
			out.WriteByte(template[i])
			i++
		} else {
			i++
			if i == len(template) {
				return "", errors.New("unfinished alias parameter")
			}
			switch template[i] {
			case '$':
				out.WriteByte('$')
				i++
			case '*':
				out.WriteString(strings.Join(args, " "))
				i++
			default:
				start := i
				for i < len(template) && template[i] >= '0' && template[i] <= '9' {
					i++
				}
				if start == i {
					return "", errors.New("unknown alias parameter; use $1, $* or $$")
				}
				n, err := strconv.Atoi(template[start:i])
				if err != nil || n < 1 || n > len(args) {
					return "", errors.New("missing alias positional argument")
				}
				out.WriteString(args[n-1])
			}
		}
		if out.Len() > 128<<10 {
			return "", errors.New("expanded alias exceeds text budget")
		}
	}
	return out.String(), nil
}
func (s *Service) expandCommandAliases(ctx context.Context, req CommandRequest) (CommandRequest, error) {
	if builtinCommandName(req.Name) {
		return req, nil
	}
	if req.Confirm {
		return req, errors.New("confirm the canonical command from the preview, not an alias")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return req, err
	}
	seen := map[string]bool{}
	for depth := 0; !builtinCommandName(req.Name); depth++ {
		if depth >= MaxAliasDepth || seen[req.Name] {
			return req, errors.New("alias cycle or depth limit exceeded")
		}
		seen[req.Name] = true
		row, err := s.stateDB.Queries().GetCommunityAlias(ctx, db.GetCommunityAliasParams{Account: req.Account, Name: req.Name})
		if errors.Is(err, sql.ErrNoRows) {
			return req, fmt.Errorf("unknown command %q", req.Name)
		}
		if err != nil {
			return req, err
		}
		name, templates, err := ParseCommand(row.Expansion)
		if err != nil {
			return req, err
		}
		args := make([]string, len(templates))
		bytes := len(name)
		for i, template := range templates {
			args[i], err = expandAliasArgument(template, req.Args)
			if err != nil {
				return req, err
			}
			bytes += len(args[i])
			if bytes > 128<<10 {
				return req, errors.New("expanded alias exceeds text budget")
			}
		}
		req.Name, req.Args = name, args
	}
	return req, nil
}
