package tui

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

type chatCompletion struct {
	identity                  daemon.CommunityIdentity
	key                       chatKey
	text, kind                string
	cursor, start, end, index int
	candidates                []string
}
type chatCompletionMsg struct {
	request, navigation uint64
	identity            daemon.CommunityIdentity
	key                 chatKey
	text, kind          string
	cursor              int
	candidates          []string
	reverse             bool
	err                 error
}

func quoteCompletion(s string) string {
	if strings.IndexFunc(s, unicode.IsSpace) < 0 && !strings.ContainsAny(s, "'\"\\") {
		return s
	}
	return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(s) + "\""
}
func (m *model) completionContext(text string, cursor int) (kind, prefix, room string, start, end int, ok bool) {
	r := []rune(text)
	cursor = max(0, min(cursor, len(r)))
	command := strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//")
	var quote rune
	escape := false
	step := func(ch rune) bool {
		if !command {
			return unicode.IsSpace(ch)
		}
		if escape {
			escape = false
			return false
		}
		if ch == '\\' {
			escape = true
			return false
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			return false
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			return false
		}
		return unicode.IsSpace(ch)
	}
	for i := 0; i < cursor; i++ {
		if step(r[i]) {
			start = i + 1
		}
	}
	open := quote
	if escape {
		return "", "", "", 0, 0, false
	}
	end = cursor
	for end < len(r) && !step(r[end]) {
		end++
	}
	raw := string(r[start:cursor])
	kind = "user"
	prefix = raw
	if command && start == 0 {
		return "command", strings.TrimPrefix(raw, "/"), "", start, end, true
	}
	if command {
		if open != 0 {
			raw += string(open)
		}
		if raw != "" {
			_, args, err := daemon.ParseCommand("x " + raw)
			if err != nil || len(args) != 1 {
				return "", "", "", 0, 0, false
			}
			prefix = args[0]
		}
		fields := strings.Fields(text)
		if len(fields) > 0 && (fields[0] == "/join" || fields[0] == "/leave" || fields[0] == "/say" || fields[0] == "/search" && len(fields) > 1 && fields[1] == "rooms") {
			kind = "room"
		}
	}
	if m.community.chats.conversation.Kind == "room" {
		room = m.community.chats.conversation.Target
	}
	return kind, prefix, room, start, end, true
}
func (m *model) applyCompletionCandidate(candidate string) {
	c := &m.community.chats
	x := c.completion
	r := []rune(x.text)
	if x.start < 0 || x.start > x.end || x.end > len(r) {
		return
	}
	value := candidate
	if x.kind == "command" {
		value = "/" + candidate
	} else if strings.HasPrefix(x.text, "/") && !strings.HasPrefix(x.text, "//") {
		value = quoteCompletion(candidate)
	}
	text := string(r[:x.start]) + value + string(r[x.end:])
	cursor := x.start + utf8.RuneCountInString(value)
	if validateChatDraft(text, true) != nil {
		return
	}
	m.setChatDraft(text, cursor)
	x.text, x.cursor, x.end = text, cursor, cursor
	c.completion = x
}
func (m *model) completeChat(reverse bool) tea.Cmd {
	c := &m.community.chats
	d := c.drafts[m.chatKey()]
	id := m.community.summary.CommunityIdentity
	if x := &c.completion; x.identity == id && x.key == m.chatKey() && x.text == d.text && x.cursor == d.cursor && len(x.candidates) > 0 {
		delta := 1
		if reverse {
			delta = -1
		}
		x.index = (x.index + delta + len(x.candidates)) % len(x.candidates)
		m.applyCompletionCandidate(x.candidates[x.index])
		return nil
	}
	kind, prefix, room, start, end, ok := m.completionContext(d.text, d.cursor)
	if !ok {
		return nil
	}
	c.cancelCompletion()
	c.completion = chatCompletion{identity: id, key: m.chatKey(), text: d.text, cursor: d.cursor, start: start, end: end, kind: kind}
	if m.client == nil {
		if kind == "user" && prefix != "" && strings.HasPrefix(strings.ToLower(c.conversation.Target), strings.ToLower(prefix)) {
			c.completion.candidates = []string{c.conversation.Target}
			m.applyCompletionCandidate(c.conversation.Target)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Second)
	c.completionCancel = cancel
	request, navigation, key, client := c.completionRequest, c.navigation, m.chatKey(), m.client
	req := daemon.CommunityCompletionRequest{CommunityIdentity: id, Kind: kind, Prefix: prefix, Room: room}
	target := ""
	if kind == "user" && c.conversation.Kind == "private" && strings.HasPrefix(strings.ToLower(c.conversation.Target), strings.ToLower(prefix)) {
		target = c.conversation.Target
	}
	return func() tea.Msg {
		defer cancel()
		out, err := client.CompleteCommunity(ctx, req)
		if err == nil && target != "" && !slices.Contains(out.Candidates, target) {
			out.Candidates = append(out.Candidates, target)
			slices.Sort(out.Candidates)
			out.Candidates = out.Candidates[:min(200, len(out.Candidates))]
		}
		if err == nil && out.CommunityIdentity != id {
			err = daemon.ErrCommunitySession
		}
		return chatCompletionMsg{request: request, navigation: navigation, identity: id, key: key, text: d.text, cursor: d.cursor, kind: kind, candidates: out.Candidates, reverse: reverse, err: err}
	}
}
func (m *model) applyChatCompletion(x chatCompletionMsg) tea.Cmd {
	c := &m.community.chats
	d := c.drafts[m.chatKey()]
	if x.request != c.completionRequest || x.navigation != c.navigation || x.identity != m.community.summary.CommunityIdentity || x.key != m.chatKey() || d.text != x.text || d.cursor != x.cursor || d.requestID != "" || c.busy {
		return nil
	}
	c.completionCancel = nil
	if x.err != nil {
		c.err = x.err.Error()
		return nil
	}
	c.completion.candidates = x.candidates
	c.completion.index = 0
	if x.reverse {
		c.completion.index = len(x.candidates) - 1
	}
	if len(x.candidates) > 0 {
		m.applyCompletionCandidate(x.candidates[c.completion.index])
	}
	return nil
}
