package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/country"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

const maxCommunityRules = 4096

type CommunityRule struct {
	ID      int64  `json:"id"`
	Action  string `json:"action"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Message string `json:"message"`
	prefix  netip.Prefix
}

type CommunityRulesRequest struct {
	CommunityIdentity
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}
type CommunityRulesPage struct {
	CommunityIdentity
	Rules      []CommunityRule `json:"rules"`
	Revision   uint64          `json:"revision"`
	NextCursor string          `json:"next_cursor"`
}
type CommunityRuleRequest struct {
	CommunityIdentity
	Rule     CommunityRule `json:"rule"`
	Revision uint64        `json:"revision"`
	Remove   bool          `json:"remove"`
	Confirm  bool          `json:"confirm"`
}

func NormalizeCommunityRule(rule CommunityRule) (CommunityRule, error) {
	if rule.Action != "ignore" && rule.Action != "ban" {
		return rule, errors.New("community: choose ignore or ban")
	}
	if !utf8.ValidString(rule.Message) || len(rule.Message) > 1024 || communityDisplayText(rule.Message) != rule.Message || strings.ContainsAny(rule.Message, "\n\t") {
		return rule, errors.New("community: invalid rejection message (maximum 1024 bytes)")
	}
	if rule.Action == "ignore" && rule.Message != "" {
		return rule, errors.New("community: ignore rules do not send rejection messages")
	}
	switch rule.Kind {
	case "username":
		if err := soulseek.ValidateUsername(rule.Value); err != nil {
			return rule, err
		}
	case "ip":
		if addr, err := netip.ParseAddr(rule.Value); err == nil && addr.Zone() == "" {
			addr = addr.Unmap()
			rule.prefix = netip.PrefixFrom(addr, addr.BitLen())
			rule.Value = addr.String()
		} else {
			prefix, err := netip.ParsePrefix(rule.Value)
			if err != nil || prefix.Addr().Is4In6() {
				return rule, errors.New("community: use an IP address or CIDR (IPv4-mapped CIDRs are not supported)")
			}
			rule.prefix = prefix.Masked()
			rule.Value = rule.prefix.String()
		}
	case "country":
		if rule.Action != "ban" {
			return rule, errors.New("community: countries can be banned, not ignored")
		}
		rule.Value = strings.ToUpper(rule.Value)
		if len(rule.Value) != 2 || rule.Value[0] < 'A' || rule.Value[0] > 'Z' || rule.Value[1] < 'A' || rule.Value[1] > 'Z' {
			return rule, errors.New("community: use a two-letter country code")
		}
	default:
		return rule, errors.New("community: choose username, ip or country")
	}
	return rule, nil
}

func loadCommunityRules(ctx context.Context, q *db.Queries, account string) ([]CommunityRule, error) {
	out := []CommunityRule{}
	for after := int64(0); ; {
		rows, err := q.ListCommunityRules(ctx, db.ListCommunityRulesParams{Account: account, AfterID: after, PageSize: 200})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			rule, err := NormalizeCommunityRule(CommunityRule{ID: row.ID, Action: row.Action, Kind: row.Kind, Value: row.Value, Message: row.Message})
			if err != nil {
				return nil, fmt.Errorf("community: invalid stored rule %d: %w", row.ID, err)
			}
			out = append(out, rule)
			after = row.ID
		}
		if len(out) > maxCommunityRules {
			return nil, errors.New("community: stored rule limit exceeded")
		}
		if len(rows) < 200 {
			return out, nil
		}
	}
}

func (s *Service) CommunityRules(ctx context.Context, req CommunityRulesRequest) (CommunityRulesPage, error) {
	out := CommunityRulesPage{CommunityIdentity: req.CommunityIdentity, Rules: []CommunityRule{}}
	limit, err := communityPageLimit(req.Limit)
	if err != nil {
		return out, err
	}
	var after int64
	if req.Cursor != "" {
		after, err = strconv.ParseInt(req.Cursor, 10, 64)
		if err != nil || after < 1 {
			return out, errors.New("community: invalid rule cursor")
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return out, err
	}
	account, err := s.stateDB.Queries().GetCommunityAccount(ctx, req.Account)
	if err != nil {
		return out, err
	}
	out.Revision = uint64(account.Revision)
	budget := 0
	for _, rule := range s.community.rules {
		if rule.ID <= after {
			continue
		}
		encoded, _ := json.Marshal(rule)
		if len(out.Rules) == int(limit) || budget+len(encoded)+1 > communityPageBytes {
			out.NextCursor = strconv.FormatInt(out.Rules[len(out.Rules)-1].ID, 10)
			break
		}
		out.Rules = append(out.Rules, rule)
		budget += len(encoded) + 1
	}
	return out, nil
}

func (s *Service) SetCommunityRule(ctx context.Context, req CommunityRuleRequest) (CommunityRulesPage, error) {
	out := CommunityRulesPage{CommunityIdentity: req.CommunityIdentity, Rules: []CommunityRule{}}
	if !req.Confirm {
		return out, errors.New("community: confirm this privacy rule change")
	}
	rule, err := NormalizeCommunityRule(req.Rule)
	if err != nil {
		return out, err
	}
	s.mu.Lock()
	var changedClient *soulseek.Client
	defer func() {
		s.mu.Unlock()
		if changedClient != nil {
			changedClient.RevalidateSharePolicy()
			s.revalidateReceivedDownloads()
		}
	}()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return out, err
	}
	var next []CommunityRule
	err = s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		account, err := q.GetCommunityAccount(ctx, req.Account)
		if err != nil {
			return err
		}
		if uint64(account.Revision) != req.Revision {
			return fmt.Errorf("community: privacy rules changed; reload: %w", ErrCommunityMessageState)
		}
		if req.Remove {
			found := false
			for _, old := range s.community.rules {
				if old.ID == rule.ID && old.Action == rule.Action && old.Kind == rule.Kind && old.Value == rule.Value {
					found = true
					break
				}
			}
			if !found {
				return errors.New("community: rule no longer exists; reload")
			}
			if _, err := q.DeleteCommunityRule(ctx, db.DeleteCommunityRuleParams{Account: req.Account, ID: rule.ID}); err != nil {
				return err
			}
		} else if rule.ID != 0 {
			if _, err := q.UpdateCommunityRule(ctx, db.UpdateCommunityRuleParams{Account: req.Account, ID: rule.ID, Action: rule.Action, Kind: rule.Kind, Value: rule.Value, Message: rule.Message}); err != nil {
				return err
			}
		} else {
			if _, err := q.PutCommunityRule(ctx, db.PutCommunityRuleParams{Account: req.Account, Action: rule.Action, Kind: rule.Kind, Value: rule.Value, Message: rule.Message}); err != nil {
				return err
			}
		}
		next, err = loadCommunityRules(ctx, q, req.Account)
		if err != nil {
			return err
		}
		revision, err := q.BumpCommunityRevision(ctx, req.Account)
		out.Revision = uint64(revision)
		return err
	})
	if err != nil {
		return out, err
	}
	s.community.rules = next
	s.community.revision++
	changedClient = s.client
	return out, nil
}

// Rules are evaluated from one daemon-owned snapshot under mu. Ignore and ban
// remain independent; explicit bans always override buddy/trust permissions.
func (s *Service) communityRuleLocked(action, username string, address netip.Addr) (matched *CommunityRule, unresolved bool) {
	address = address.Unmap()
	for i := range s.community.rules {
		rule := &s.community.rules[i]
		if rule.Action != action {
			continue
		}
		match := false
		switch rule.Kind {
		case "username":
			match = rule.Value == username
		case "ip":
			unresolved = unresolved || !address.IsValid()
			match = rule.prefix.Contains(address)
		case "country":
			unresolved = unresolved || !address.IsValid()
			code := country.Lookup(address)
			match = code != "" && code == rule.Value
		}
		if match {
			return rule, false
		}
	}
	return nil, unresolved
}

func (s *Service) communitySharePermission(epoch uint64, account, username string, address netip.Addr) soulseek.SharePermission {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := soulseek.SharePermission{Roots: map[string]soulseek.ShareVisibility{}}
	if s.closed || epoch != s.uploadEpoch || account != accountKey(s.cfg) {
		out.Banned = true
		return out
	}
	if username != "" || address.IsValid() {
		rule, unresolved := s.communityRuleLocked("ban", username, address)
		if rule != nil || unresolved {
			out.Banned = true
			out.NeedsAddress = unresolved
			if rule != nil {
				out.Reason = rule.Message
			} else {
				out.Reason = "Address verification required"
			}
			return out
		}
	}
	buddy, isBuddy := s.community.buddies[username]
	// Soulseek identities are not cryptographically authenticated; self is never
	// a shortcut into restricted peer shares, even if added as a buddy.
	isBuddy = isBuddy && username != "" && username != s.cfg.Soulseek.Username
	for _, root := range s.cfg.Shares {
		allowed := root.Access == "" || root.Access == "public" || root.Access == "buddy" && isBuddy || root.Access == "trusted" && isBuddy && buddy.Trusted
		if allowed {
			out.Roots[root.Name] = soulseek.ShareAllowed
		} else if root.Reveal && username != "" {
			out.Roots[root.Name] = soulseek.ShareLocked
		}
	}
	return out
}
