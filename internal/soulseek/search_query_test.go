package soulseek

import "testing"

func TestSearchQuerySyntaxAndValidation(t *testing.T) {
	query, err := parseSearchQuery(`"live session" *radio -remix`)
	if err != nil {
		t.Fatal(err)
	}
	if query.wire != "live session radio" {
		t.Fatalf("wire query = %q", query.wire)
	}
	if !query.matches(SearchResult{Username: "peer", Path: `Music\Live Session Radio.flac`}) {
		t.Fatal("matching result rejected")
	}
	if query.matches(SearchResult{Username: "peer", Path: `Music\Live Session Radio Remix.flac`}) {
		t.Fatal("excluded result accepted")
	}
	if query.matches(SearchResult{Username: "peer", Path: `Music\Unrelated.flac`}) {
		t.Fatal("unrelated result accepted")
	}
	if _, err := parseSearchQuery(`-remix`); err == nil {
		t.Fatal("negative-only query accepted")
	}
	if _, err := parseSearchQuery(`"unfinished`); err == nil {
		t.Fatal("unclosed quote accepted")
	}
}
