package soulseek

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"testing/synctest"
)

func TestRoomSearchIndependentFixture(t *testing.T) {
	fixture := communityFixture(t, "room-search-request")
	frame, err := EncodeMessage(RoomSearchRequest{Room: "lounge", Token: 41, Query: "song"})
	must(t, err)
	code, payload, err := ReadFrame(bytes.NewReader(frame))
	failIf(t, err != nil || code != fixture.Code || !bytes.Equal(payload, fixture.Payload(t)), code, payload, err)
}
func TestScopedSearchCompleteFanoutAndNoFallback(t *testing.T) {
	for _, rooms := range []bool{false, true} {
		t.Run(fmt.Sprint(rooms), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clientSide, server := net.Pipe()
				defer server.Close()
				c := NewClient(ClientConfig{})
				c.conn = clientSide
				defer c.Close()
				names := make([]string, 100)
				for i := range names {
					names[i] = fmt.Sprintf("target %03d", i)
				}
				go func() {
					var token uint32
					for _, want := range names {
						code, payload, err := ReadFrame(server)
						if err != nil {
							t.Error(err)
							return
						}
						expected := ServerUserSearch
						if rooms {
							expected = ServerRoomSearch
						}
						if code != expected {
							t.Errorf("scope fallback: %d", code)
							return
						}
						d := NewDecoder(payload)
						name, err := d.String()
						if err != nil || name != want {
							t.Error(name, err)
							return
						}
						next, err := d.U32()
						if err != nil || token != 0 && next != token {
							t.Error("token", next, err)
							return
						}
						token = next
						query, err := d.String()
						if err != nil || query != "song" {
							t.Error(query, err)
							return
						}
						c.mu.Lock()
						responses := c.pending[token]
						c.mu.Unlock()
						responses <- SearchResponse{Username: name, Results: []SearchResult{{Username: name, Path: "song.mp3"}}}
					}
				}()
				users, roomNames := names, []string(nil)
				if rooms {
					users, roomNames = nil, names
				}
				results, err := c.SearchScoped(context.Background(), "song", users, roomNames)
				if err != nil || len(results) != 100 {
					t.Fatal("incomplete target set", len(results), err)
				}
				if len(c.pending) != 0 {
					t.Fatal("subscription retained")
				}
			})
		})
	}
}
func TestScopedSearchRejectsEmptyOrMixedTargets(t *testing.T) {
	c := NewClient(ClientConfig{})
	defer c.Close()
	for _, targets := range []struct{ users, rooms []string }{{nil, nil}, {[]string{"Alice"}, []string{"lounge"}}, {[]string{""}, nil}, {nil, []string{""}}} {
		if _, err := c.SearchScoped(context.Background(), "song", targets.users, targets.rooms); err == nil {
			t.Fatal("invalid scoped search accepted")
		}
	}
}
