package soulseek

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestIncomingSearchPolicyAndServerExclusions(t *testing.T) {
	index := NewShareIndex()
	index.files = append(index.files,
		ShareFile{Root: "Music", Path: "猫猫.mp3"},
		ShareFile{Root: "Music", Path: "FORBIDDEN track.mp3"},
	)
	for i := 0; i < 600; i++ {
		index.files = append(index.files, ShareFile{Root: "Music", Path: fmt.Sprintf("song-%02d.mp3", i)})
	}
	policy := IncomingSearchPolicy{Respond: true, MinimumLength: 3, MaximumResults: 50}
	client := NewClient(ClientConfig{Share: index, IncomingSearch: &policy})

	if results := client.incomingSearchResults("猫猫"); len(results) != 0 {
		t.Fatal("two-rune search bypassed three-character minimum")
	}
	client.ConfigureIncomingSearch(IncomingSearchPolicy{Respond: true, MaximumResults: 50})
	if results := client.incomingSearchResults("猫猫"); len(results) != 1 {
		t.Fatalf("zero minimum rejected search: %+v", results)
	}
	if results := client.incomingSearchResults("song"); len(results) != 50 {
		t.Fatalf("result cap = %d, want 50", len(results))
	}
	client.ConfigureIncomingSearch(IncomingSearchPolicy{Respond: true, MaximumResults: 550})
	if results := client.incomingSearchResults("song"); len(results) != 550 {
		t.Fatalf("raised result cap = %d, want 550", len(results))
	}
	if results := client.incomingSearchResults("forbidden"); len(results) != 1 {
		t.Fatalf("pre-exclusion results: %+v", results)
	}

	client.route(ServerExcludedSearchPhrases, ExcludedSearchPhrases{Phrases: []string{"fOrBiDdEn"}})
	if results := client.incomingSearchResults("forbidden"); len(results) != 0 {
		t.Fatalf("server-excluded path returned: %+v", results)
	}
	client.ConfigureIncomingSearch(IncomingSearchPolicy{MaximumResults: 50})
	if results := client.incomingSearchResults("song"); len(results) != 0 {
		t.Fatal("disabled client returned incoming search results")
	}
	clientConn, serverConn := net.Pipe()
	disabledClient := NewClientOnConn(ClientConfig{Share: index, IncomingSearch: &IncomingSearchPolicy{MaximumResults: 50}}, clientConn)
	disabledClient.respondSearch(IncomingSearch{Username: "peer", Query: "song"})
	_ = serverConn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := ReadFrame(serverConn); err == nil {
		t.Fatal("disabled response attempted a peer connection")
	}
	_ = disabledClient.Close()
	_ = serverConn.Close()
	_ = client.Close()

	defaultClient := NewClient(ClientConfig{Share: index})
	if results := defaultClient.incomingSearchResults("song"); len(results) != 500 {
		t.Fatalf("direct client defaults changed: %d results", len(results))
	}
}

func TestExcludedSearchPhrasesProtocolAndSearchResponseLimit(t *testing.T) {
	var payload Encoder
	payload.U32(2)
	_ = payload.String("first phrase")
	_ = payload.String("second")
	message, err := DecodeMessage(ServerExcludedSearchPhrases, payload.Payload())
	phrases, ok := message.(ExcludedSearchPhrases)
	if err != nil || !ok || fmt.Sprint(phrases.Phrases) != "[first phrase second]" {
		t.Fatalf("excluded phrases: %#v %v", message, err)
	}

	var oversized Encoder
	oversized.U32(maxExcludedSearchPhrases + 1)
	if _, err := DecodeExcludedSearchPhrases(oversized.Payload()); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized phrase list: %v", err)
	}
	var truncated Encoder
	truncated.U32(1)
	if _, err := DecodeExcludedSearchPhrases(truncated.Payload()); !errors.Is(err, ErrTruncated) {
		t.Fatalf("truncated phrase list: %v", err)
	}

	clientConn, serverConn := net.Pipe()
	stateClient := NewClientOnConn(ClientConfig{}, clientConn)
	stateClient.route(ServerExcludedSearchPhrases, ExcludedSearchPhrases{Phrases: []string{"session only"}})
	runDone := make(chan error, 1)
	go func() { runDone <- stateClient.Run(context.Background()) }()
	_ = serverConn.Close()
	<-runDone
	stateClient.mu.Lock()
	remaining := len(stateClient.excludedSearchPhrases)
	stateClient.mu.Unlock()
	_ = stateClient.Close()
	if remaining != 0 {
		t.Fatal("server exclusions survived connection end")
	}

	results := make([]SearchResult, 501)
	for i := range results {
		results[i] = SearchResult{Path: fmt.Sprintf("Music\\song-%d.mp3", i), Public: true}
	}
	encoded, err := EncodeMessage(SearchResponse{Username: "peer", Results: results})
	if err != nil {
		t.Fatal(err)
	}
	command, responsePayload, err := ReadFrame(bytes.NewReader(encoded))
	if err != nil || command != PeerSearch {
		t.Fatalf("search response frame: %d %v", command, err)
	}
	response, err := DecodeSearchResponse(responsePayload)
	if err != nil || len(response.Results) != len(results) {
		t.Fatalf("large search response: %d %v", len(response.Results), err)
	}
	if _, err := EncodeMessage(SearchResponse{Results: make([]SearchResult, maxSearchResults+1)}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized search response: %v", err)
	}
}

func TestDisabledIncomingSearchStillForwardsDistributedRequest(t *testing.T) {
	policy := IncomingSearchPolicy{MaximumResults: 50}
	client := NewClient(ClientConfig{IncomingSearch: &policy})
	messages, err := client.distributed.AddChild("child")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := (DistributedSearchQuery{Username: "peer", Token: 7, Query: "song"}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	client.handleDistributedSearch(payload)
	message := <-messages
	if message.Command != DistributedSearchCommand || !bytes.Equal(message.Payload, payload) {
		t.Fatalf("forwarded message: %#v", message)
	}
}
