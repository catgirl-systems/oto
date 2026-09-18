package daemon

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

// Censorship matches whole whitespace-delimited tokens, case insensitively.
// * matches zero or more characters; ? matches one. Other characters are literal.
// Matching tokens become ***; punctuation is part of a token (use bad* for bad!).
type CommunityTextTools = config.CommunityTextTools
type CommunitySubstitution = config.CommunitySubstitution
type communityTextPolicy struct {
	settings CommunityTextTools
	censor   []*regexp.Regexp
}

func compileCommunityTextTools(settings CommunityTextTools) (*communityTextPolicy, error) {
	if len(settings.Keywords) > 32 || len(settings.Substitutions) > 32 || len(settings.Censorship) > 32 {
		return nil, errors.New("community: at most 32 rules of each text-tool kind")
	}
	valid := func(s string) bool { return len(s) <= 1024 && utf8.ValidString(s) && communityDisplayText(s) == s }
	p := &communityTextPolicy{settings: settings}
	for _, keyword := range settings.Keywords {
		if strings.TrimSpace(keyword) == "" || !valid(keyword) {
			return nil, errors.New("community: invalid keyword")
		}
	}
	for _, rule := range settings.Substitutions {
		if rule.From == "" || !valid(rule.From) || !valid(rule.To) {
			return nil, errors.New("community: invalid substitution")
		}
	}
	for _, pattern := range settings.Censorship {
		if pattern == "" || !valid(pattern) || strings.IndexFunc(pattern, unicode.IsSpace) >= 0 {
			return nil, errors.New("community: censorship patterns must be single tokens within 1024 bytes")
		}
		expr := "(?i)\\A"
		for _, r := range pattern {
			switch r {
			case '*':
				expr += ".*"
			case '?':
				expr += "."
			default:
				expr += regexp.QuoteMeta(string(r))
			}
		}
		re, err := regexp.Compile(expr + "\\z")
		if err != nil {
			return nil, err
		}
		p.censor = append(p.censor, re)
	}
	// Published snapshots do not retain mutable caller-owned slices.
	p.settings.Keywords = append([]string(nil), settings.Keywords...)
	p.settings.Censorship = append([]string(nil), settings.Censorship...)
	p.settings.Substitutions = append([]CommunitySubstitution(nil), settings.Substitutions...)
	return p, nil
}
func (p *communityTextPolicy) outgoing(text string) (string, error) {
	if len(text) > soulseek.MaxChatBytes || !utf8.ValidString(text) {
		return "", errors.New("community: outgoing text exceeds limit or is not UTF-8")
	}
	if p != nil {
		for _, rule := range p.settings.Substitutions {
			n := strings.Count(text, rule.From)
			if len(text)+n*(len(rule.To)-len(rule.From)) > soulseek.MaxChatBytes {
				return "", errors.New("community: substituted text exceeds limit; nothing sent")
			}
			text = strings.ReplaceAll(text, rule.From, rule.To)
		}
	}
	return communityOutgoingText(text)
}
func mentionWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_'
}
func containsCommunityMention(text, keyword string) bool {
	if keyword == "" {
		return false
	}
	// Unicode simple lowercase preserves rune positions, unlike byte offsets.
	text = strings.Map(unicode.ToLower, text)
	keyword = strings.Map(unicode.ToLower, keyword)
	for offset := 0; offset <= len(text); {
		i := strings.Index(text[offset:], keyword)
		if i < 0 {
			return false
		}
		i += offset
		end := i + len(keyword)
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if (i == 0 || !mentionWord(before)) && (end == len(text) || !mentionWord(after)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		offset = i + size
	}
	return false
}
func (p *communityTextPolicy) incoming(text, username string) (string, bool) {
	if text == communityCTCPVersionRequest {
		text = "[CTCP] VERSION request"
	}
	text = communityDisplayText(text)
	mention := containsCommunityMention(text, username)
	if p == nil {
		return text, mention
	}
	for _, keyword := range p.settings.Keywords {
		mention = mention || containsCommunityMention(text, keyword)
	}
	if len(p.censor) == 0 {
		return text, mention
	}
	var out strings.Builder
	// Preserve whitespace, order and line breaks exactly; never expose uncensored
	// tokens to notifications or storage. Regex evaluation is Go's bounded engine.
	for len(text) > 0 {
		end := strings.IndexFunc(text, unicode.IsSpace)
		if end < 0 {
			end = len(text)
		}
		token := text[:end]
		if token != "" {
			for _, re := range p.censor {
				if re.MatchString(token) {
					token = "***"
					break
				}
			}
		}
		out.WriteString(token)
		text = text[end:]
		if len(text) > 0 {
			_, size := utf8.DecodeRuneInString(text)
			out.WriteString(text[:size])
			text = text[size:]
		}
	}
	return out.String(), mention
}
