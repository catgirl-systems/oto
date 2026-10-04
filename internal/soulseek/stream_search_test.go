package soulseek

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

func TestStreamSearchKeepsLateResponses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientSide, server := net.Pipe()
		defer server.Close()
		c := NewClient(ClientConfig{})
		c.conn = clientSide
		defer c.Close()
		go func() {
			command, payload, err := ReadFrame(server)
			if err != nil || command != ServerFileSearch {
				t.Errorf("search request: %d %v", command, err)
				return
			}
			token := NewDecoder(payload).U32()
			c.mu.Lock()
			response := c.pending[token]
			c.mu.Unlock()
			response <- SearchResponse{Username: "fast", Results: []SearchResult{{Username: "fast", Path: "song.flac"}}}
			// A blocking search would already have returned here.
			time.Sleep(20 * time.Second)
			response <- SearchResponse{Username: "slow", Results: []SearchResult{{Username: "slow", Path: "song.mp3"}, {Username: "slow", Path: "other.txt"}}}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var batches [][]SearchResult
		err := c.StreamSearch(ctx, "song", nil, nil, func(batch []SearchResult) { batches = append(batches, batch) })
		failIfFmt(t, err != nil, "stream ended with %v", err)
		failIfFmt(t, len(batches) != 2 || batches[0][0].Username != "fast" || len(batches[1]) != 1 || batches[1][0].Username != "slow", "batches %+v", batches)
		c.mu.Lock()
		remaining := len(c.pending)
		c.mu.Unlock()
		failIf(t, remaining != 0, "search token leaked")
	})
}
