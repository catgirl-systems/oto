package soulseek

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

func TestCommunityDiscoveryReferenceProtocol(t *testing.T) {
	for _, test := range []struct {
		name    string
		message Message
	}{
		{"interest-add-like", InterestChangeRequest{Item: "techno", Like: true}},
		{"interest-remove-like", InterestChangeRequest{Item: "techno", Like: true, Remove: true}},
		{"interest-add-dislike", InterestChangeRequest{Item: "techno"}},
		{"interest-remove-dislike", InterestChangeRequest{Item: "techno", Remove: true}},
		{"discovery-personal-request", DiscoveryRequest{Kind: "personal"}},
		{"discovery-global-request", DiscoveryRequest{Kind: "global"}},
		{"discovery-item-request", DiscoveryRequest{Kind: "item", Target: "techno"}},
		{"discovery-similar-request", DiscoveryRequest{Kind: "similar"}},
		{"discovery-item-users-request", DiscoveryRequest{Kind: "item-users", Target: "techno"}},
		{"discovery-user-interests-request", DiscoveryRequest{Kind: "user-interests", Target: "Alice"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := communityFixture(t, test.name)
			frame, err := EncodeMessage(test.message)
			failIf(t, err != nil || binary.LittleEndian.Uint32(frame[4:]) != f.Code || !bytes.Equal(frame[8:], f.Payload(t)), "request differs from independent reference", err)
		})
	}
	for _, kind := range []string{"personal", "global", "item", "similar", "item-users", "user-interests"} {
		t.Run(kind, func(t *testing.T) {
			f := communityFixture(t, "discovery-"+kind)
			value, err := DecodeServerMessage(f.Code, f.Payload(t))
			must(t, err)
			m, ok := value.(DiscoveryResponse)
			failIf(t, !ok || m.Kind != kind, "response not authoritative discovery", value)
			switch kind {
			case "personal", "global", "item":
				if !reflect.DeepEqual(m.Recommendations, []ScoredInterest{{"techno", 42}, {"noise", -7}}) {
					t.Fatal("signed recommendation ratings", m)
				}
			case "similar", "item-users":
				failIf(t, len(m.Users) != 2 || m.Users[0].Username != "Alice" || m.Users[1].Username != "alice", "folded discovery identities", m)
				failIf(t, kind == "similar" && m.Users[0].Rating != 9, "missing rating")
			case "user-interests":
				if m.Target != "Alice" || !reflect.DeepEqual(m.Likes, []string{"techno"}) || !reflect.DeepEqual(m.Dislikes, []string{"noise"}) {
					t.Fatal("user interests", m)
				}
			}
			failIf(t, (kind == "item" || kind == "item-users") && m.Target != "techno", "item correlation missing")
			if _, err := DecodeDiscoveryResponse(f.Code, append(f.Payload(t), 1)); err == nil {
				t.Fatal("accepted trailing data")
			}
			if _, err := DecodeDiscoveryResponse(f.Code, f.Payload(t)[:len(f.Payload(t))-1]); err == nil {
				t.Fatal("accepted truncated response")
			}
		})
	}
}

func TestCommunityDiscoveryBoundsAndNormalization(t *testing.T) {
	item, err := NormalizeInterest("  TECHno 世界  ")
	failIf(t, err != nil || item != "techno 世界", "normalization", item, err)
	for _, item := range []string{"", strings.Repeat("a", MaxInterestBytes+1), "a\x1b[2J", "a\u202eb", string([]byte{0xff})} {
		if _, err := NormalizeInterest(item); err == nil {
			t.Fatal("invalid interest accepted")
		}
	}
	for _, req := range []DiscoveryRequest{{Kind: "invalid"}, {Kind: "global", Target: "unexpected"}, {Kind: "item"}, {Kind: "user-interests", Target: "invalid\nuser"}} {
		if _, err := EncodeMessage(req); err == nil {
			t.Fatal("invalid query accepted", req)
		}
	}
	for _, code := range []uint32{ServerGlobalRecommendations, ServerSimilarUsers} {
		if _, err := DecodeDiscoveryResponse(code, []byte{255, 255, 255, 255}); err == nil {
			t.Fatal("unbounded count")
		}
	}
	f := communityFixture(t, "discovery-global")
	// Nicotine+ accepts the legacy one-list response and Latin-1 display text.
	legacy := f.Payload(t)[:18]
	m, err := DecodeDiscoveryResponse(ServerGlobalRecommendations, legacy)
	failIf(t, err != nil || len(m.Recommendations) != 1, "legacy response rejected", err)
	raw := []byte{1, 0, 0, 0, 4, 0, 0, 0, 'c', 'a', 'f', 0xe9, 1, 0, 0, 0}
	m, err = DecodeDiscoveryResponse(ServerGlobalRecommendations, raw)
	failIf(t, err != nil || m.Recommendations[0].Item != string(raw[8:12]), "legacy text rejected", err)
	if _, err := DecodeDiscoveryResponse(ServerGlobalRecommendations, make([]byte, MaxDiscoveryBytes+1)); err == nil {
		t.Fatal("unbounded discovery payload")
	}
	var payload []byte
	payload = binary.LittleEndian.AppendUint32(payload, MaxDiscoveryEntries+1)
	for range MaxDiscoveryEntries + 1 {
		payload = binary.LittleEndian.AppendUint32(payload, 0)
		payload = binary.LittleEndian.AppendUint32(payload, 0)
	}
	if _, err := DecodeDiscoveryResponse(ServerGlobalRecommendations, payload); err == nil {
		t.Fatal("entry ceiling ignored")
	}
}

func FuzzCommunityDiscoveryDecode(f *testing.F) {
	for _, kind := range []string{"personal", "global", "item", "similar", "item-users", "user-interests"} {
		fixture := communityFixture(f, "discovery-"+kind)
		f.Add(fixture.Code, fixture.Payload(f))
	}
	f.Fuzz(func(t *testing.T, code uint32, payload []byte) {
		m, err := DecodeDiscoveryResponse(code, payload)
		failIf(t, err == nil && len(m.Recommendations)+len(m.Users)+len(m.Likes)+len(m.Dislikes) > MaxDiscoveryEntries, "decoder exceeded entry ceiling")
	})
}
