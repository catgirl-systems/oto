package soulseek

import (
	"context"
	"encoding/hex"
	"net"
	"reflect"
	"testing"
	"testing/synctest"
)

func TestUserSearchEncodingAndValidation(t *testing.T) {
	frame, err := EncodeMessage(UserSearchRequest{Username: "alice", Token: 0x01020304, Query: "song"})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(frame); got != "190000002a00000005000000616c6963650403020104000000736f6e67" {
		t.Fatal(got)
	}
	users, err := NormalizeSearchUsers([]string{" alice ", "Alice Smith", "alice"})
	if err != nil || !reflect.DeepEqual(users, []string{"alice", "Alice Smith"}) {
		t.Fatalf("%v %v", users, err)
	}
	for _, users := range [][]string{{""}, {"\talice"}, {"alice\n"}, make([]string, 33)} {
		if _, err := NormalizeSearchUsers(users); err == nil {
			t.Fatalf("accepted invalid users %q", users)
		}
	}
}
func TestTargetedSearchIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientSide, server := net.Pipe()
		defer server.Close()
		c := NewClient(ClientConfig{})
		c.conn = clientSide
		defer c.Close()
		go func() {
			var token uint32
			for _, want := range []string{"alice", "Bob Smith"} {
				command, payload, err := ReadFrame(server)
				if err != nil {
					t.Error(err)
					return
				}
				if command != ServerUserSearch {
					t.Errorf("global fallback: %d", command)
					return
				}
				d := NewDecoder(payload)
				user, err := d.String()
				if err != nil || user != want {
					t.Errorf("target %q: %v", user, err)
					return
				}
				next, err := d.U32()
				if err != nil {
					t.Error(err)
					return
				}
				if token != 0 && token != next {
					t.Error("different tokens")
					return
				}
				token = next
			}
			c.mu.Lock()
			response := c.pending[token]
			c.mu.Unlock()
			response <- SearchResponse{Username: "mallory", Results: []SearchResult{{Username: "mallory", Path: "song.mp3"}}}
			response <- SearchResponse{Username: "alice", Results: []SearchResult{{Username: "alice", Path: "song.mp3"}, {Username: "mallory", Path: "song.mp3"}}}
			response <- SearchResponse{Username: "Bob Smith", Results: []SearchResult{{Username: "Bob Smith", Path: "song.flac"}}}
		}()
		results, err := c.Search(context.Background(), "song", "alice", "Bob Smith", "alice")
		if err != nil || len(results) != 2 || results[0].Username != "alice" || results[1].Username != "Bob Smith" {
			t.Fatalf("%+v %v", results, err)
		}
		c.mu.Lock()
		remaining := len(c.pending)
		c.mu.Unlock()
		if remaining != 0 {
			t.Fatal("late result subscription retained")
		}
	})
}
