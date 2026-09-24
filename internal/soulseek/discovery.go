package soulseek

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ServerAddLike               uint32 = 51
	ServerRemoveLike            uint32 = 52
	ServerRecommendations       uint32 = 54
	ServerGlobalRecommendations uint32 = 56
	ServerUserInterests         uint32 = 57
	ServerSimilarUsers          uint32 = 110
	ServerItemRecommendations   uint32 = 111
	ServerItemSimilarUsers      uint32 = 112
	ServerAddDislike            uint32 = 117
	ServerRemoveDislike         uint32 = 118
	MaxInterestBytes                   = 1024
	MaxDiscoveryEntries                = 10000
	MaxDiscoveryBytes                  = 4 << 20
)

// Interests are normalized preferences, not usernames or protocol identities.
func NormalizeInterest(item string) (string, error) {
	if !utf8.ValidString(item) || strings.IndexFunc(item, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) >= 0 {
		return "", fmt.Errorf("%w: invalid interest text", ErrMalformed)
	}
	item = strings.ToLower(strings.TrimSpace(item))
	if item == "" || len(item) > MaxInterestBytes {
		return "", fmt.Errorf("%w: interest must fit 1024 bytes", ErrMalformed)
	}
	return item, nil
}

type InterestChangeRequest struct {
	Item         string
	Like, Remove bool
}

func (m InterestChangeRequest) command() uint32 {
	if m.Like {
		if m.Remove {
			return ServerRemoveLike
		}
		return ServerAddLike
	}
	if m.Remove {
		return ServerRemoveDislike
	}
	return ServerAddDislike
}
func (m InterestChangeRequest) encode(e *Encoder) error {
	item, err := NormalizeInterest(m.Item)
	if err != nil {
		return err
	}
	e.String(item)
	return nil
}

type DiscoveryRequest struct{ Kind, Target string }

func (m DiscoveryRequest) command() uint32 {
	switch m.Kind {
	case "personal":
		return ServerRecommendations
	case "global":
		return ServerGlobalRecommendations
	case "item":
		return ServerItemRecommendations
	case "similar":
		return ServerSimilarUsers
	case "item-users":
		return ServerItemSimilarUsers
	case "user-interests":
		return ServerUserInterests
	}
	return 0
}
func (m DiscoveryRequest) encode(e *Encoder) error {
	switch m.Kind {
	case "personal", "global", "similar":
		if m.Target != "" {
			return fmt.Errorf("%w: discovery target not allowed", ErrMalformed)
		}
		return nil
	case "item", "item-users":
		item, err := NormalizeInterest(m.Target)
		if err != nil {
			return err
		}
		e.String(item)
		return nil
	case "user-interests":
		return encodeUsername(e, m.Target)
	default:
		return fmt.Errorf("%w: unknown discovery query", ErrMalformed)
	}
}

type ScoredInterest struct {
	Item  string
	Score int32
}
type SimilarUser struct {
	Username string
	Rating   uint32
}
type DiscoveryResponse struct {
	Kind, Target    string
	Recommendations []ScoredInterest
	Users           []SimilarUser
	Likes, Dislikes []string
}

func (DiscoveryResponse) socialMessage() {}

func discoveryCount(d *Decoder, minBytes int, remaining int) int {
	count := d.U32()
	if uint64(count) > uint64(remaining) || uint64(count) > uint64(d.Remaining()/minBytes) {
		d.fail(fmt.Errorf("%w: discovery count", ErrMalformed))
		return 0
	}
	return int(count)
}
func decodeInterest(d *Decoder) string {
	raw := d.Bytes()
	if len(raw) > MaxInterestBytes {
		d.fail(ErrTooLarge)
		return ""
	}
	return string(raw) // Preserve wire text; the daemon decodes/sanitizes display text.
}
func DecodeDiscoveryResponse(command uint32, payload []byte) (DiscoveryResponse, error) {
	var m DiscoveryResponse
	if len(payload) > MaxDiscoveryBytes {
		return m, ErrTooLarge
	}
	d := NewDecoder(payload)
	switch command {
	case ServerRecommendations:
		m.Kind = "personal"
	case ServerGlobalRecommendations:
		m.Kind = "global"
	case ServerItemRecommendations:
		m.Kind = "item"
	case ServerSimilarUsers:
		m.Kind = "similar"
	case ServerItemSimilarUsers:
		m.Kind = "item-users"
	case ServerUserInterests:
		m.Kind = "user-interests"
	default:
		return m, fmt.Errorf("%w: discovery command", ErrMalformed)
	}
	if m.Kind == "item" || m.Kind == "item-users" {
		// Keep the echoed wire target exact for correlation; never normalize a reply.
		m.Target = d.String()
		if len(m.Target) > MaxInterestBytes {
			return m, ErrTooLarge
		}
	} else if m.Kind == "user-interests" {
		m.Target = decodeUsername(d)
	}
	switch m.Kind {
	case "personal", "global", "item":
		// Older servers send one list; modern servers may append an unrecommendation
		// list. Scores are signed regardless of which list contains the entry.
		for list := 0; list < 2; list++ {
			if list == 1 && d.Remaining() == 0 {
				break
			}
			count := discoveryCount(d, 8, MaxDiscoveryEntries-len(m.Recommendations))
			for range count {
				m.Recommendations = append(m.Recommendations, ScoredInterest{decodeInterest(d), int32(d.U32())})
			}
		}
	case "similar", "item-users":
		stride := 4
		if m.Kind == "similar" {
			stride = 8
		}
		for range discoveryCount(d, stride, MaxDiscoveryEntries) {
			username := decodeUsername(d)
			var rating uint32
			if m.Kind == "similar" {
				rating = d.U32()
			}
			m.Users = append(m.Users, SimilarUser{username, rating})
		}
	case "user-interests":
		remaining := MaxDiscoveryEntries
		for _, list := range []*[]string{&m.Likes, &m.Dislikes} {
			count := discoveryCount(d, 4, remaining)
			remaining -= count
			for range count {
				*list = append(*list, decodeInterest(d))
			}
		}
	}
	return m, d.Done()
}

// Queries only reserve a write here. The daemon owns response correlation and
// keeps ambiguous requests pending: these server replies have no request token.
func (c *Client) SendDiscovery(ctx context.Context, request DiscoveryRequest, beforeWrite func() error) (bool, error) {
	return c.sendTracked(ctx, request, beforeWrite)
}
func (c *Client) ChangeInterest(ctx context.Context, request InterestChangeRequest, beforeWrite func() error) (bool, error) {
	return c.sendTracked(ctx, request, beforeWrite)
}
