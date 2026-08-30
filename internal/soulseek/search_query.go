package soulseek

import (
	"errors"
	"strings"
	"unicode"
)

type searchQuery struct {
	wire             string
	include, exclude []string
}

func parseSearchQuery(raw string) (searchQuery, error) {
	var query searchQuery
	tokens, err := splitSearchQuery(raw)
	if err != nil {
		return query, err
	}
	positive := make([]string, 0, len(tokens))
	for _, token := range tokens {
		excluded := strings.HasPrefix(token, "-")
		term := strings.TrimLeft(token, "-*")
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		lower := strings.ToLower(term)
		if excluded {
			query.exclude = append(query.exclude, lower)
			continue
		}
		query.include = append(query.include, lower)
		positive = append(positive, term)
	}
	if len(positive) == 0 {
		return query, errors.New("soulseek: search needs a positive term")
	}
	query.wire = strings.Join(positive, " ")
	return query, nil
}

func splitSearchQuery(raw string) ([]string, error) {
	var tokens []string
	var token strings.Builder
	var quote rune
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
		case quote == 0 && unicode.IsSpace(r):
			if token.Len() > 0 {
				tokens = append(tokens, token.String())
				token.Reset()
			}
		default:
			token.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, errors.New("soulseek: unclosed quote in search")
	}
	if token.Len() > 0 {
		tokens = append(tokens, token.String())
	}
	return tokens, nil
}

func (query searchQuery) matches(result SearchResult) bool {
	path := strings.ToLower(result.Path)
	for _, term := range query.include {
		if !strings.Contains(path, term) {
			return false
		}
	}
	for _, term := range query.exclude {
		if strings.Contains(path, term) {
			return false
		}
	}
	return true
}
