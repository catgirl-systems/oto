package daemon

import (
	"errors"
	"testing"
)

func TestSearchFilterParsingAndMatching(t *testing.T) {
	result := SearchResult{Username: "Alice", Path: `Music\Live Radio Session.flac`, Extension: "flac", CountryCode: "US", Size: 25 << 20, Bitrate: 320, Duration: 185, SlotFree: true, Public: true}
	tests := []struct {
		expression string
		match      bool
	}{
		{`in:"alice/.+radio session"`, true},
		{`out:remix`, true},
		{`out:radio`, false},
		{`type:audio,!mp3`, true},
		{`type:video,mp3`, false},
		{`country:us`, true},
		{`country:CA,US`, true},
		{`country:CA,!GB`, false},
		{`country:US,!GB`, true},
		{`country:!US,!DE`, false},
		{`country:!GB,!DE`, true},
		{`size:>20MiB size:<=25MiB`, true},
		{`size:>=26MB`, true},
		{`size:<25000000B`, false},
		{`bitrate:320 bitrate:!=0 duration:>3:00 duration:<03:10`, true},
		{`bitrate:!0`, true},
		{`free:true public:true`, true},
		{`free:false`, false},
		{`public:false`, false},
	}
	for _, test := range tests {
		filter, err := parseSearchFilter(test.expression)
		if err != nil {
			t.Fatalf("parse %q: %v", test.expression, err)
		}
		if got := filter.matches(result); got != test.match {
			t.Errorf("matches(%q) = %v, want %v", test.expression, got, test.match)
		}
	}

	unknown := result
	unknown.Bitrate, unknown.Duration = 0, 0
	filter, _ := parseSearchFilter(`bitrate:!0 duration:>0`)
	if filter.matches(unknown) {
		t.Fatal("missing media attributes matched non-zero filters")
	}

	unknownCountry := result
	unknownCountry.CountryCode = ""
	included, _ := parseSearchFilter(`country:US`)
	excluded, _ := parseSearchFilter(`country:!GB`)
	if included.matches(unknownCountry) || !excluded.matches(unknownCountry) {
		t.Fatal("unknown country did not follow include/exclude semantics")
	}
}

func TestSearchFilterValuesAndErrors(t *testing.T) {
	sizes := map[string]uint64{"1B": 1, "1KB": 1000, "1MB": 1000000, "1GB": 1000000000, "1TB": 1000000000000, "1KiB": 1 << 10, "1MiB": 1 << 20, "1GiB": 1 << 30, "1TiB": 1 << 40, "1.5MiB": 1572864}
	for input, want := range sizes {
		got, err := parseSize(input)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	durations := map[string]uint64{"59": 59, "2:00": 120, "1:02:03": 3723}
	for input, want := range durations {
		got, err := parseDuration(input)
		if err != nil || got != want {
			t.Errorf("parseDuration(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	comparators := map[string]bool{"<": false, "<=": true, "=": true, "==": true, "!=": false, ">=": true, ">": false}
	for operator, want := range comparators {
		condition, err := parseCondition(operator+"10", parseUnsigned)
		if err != nil || matchesConditions(10, []numberCondition{condition}) != want {
			t.Errorf("comparator %q failed: %+v %v", operator, condition, err)
		}
	}
	categories := map[string]string{"audio": "flac", "video": "mkv", "image": "png", "document": "pdf", "text": "txt", "archive": "zip", "executable": "exe"}
	for category, extension := range categories {
		filter, err := parseSearchFilter("type:" + category)
		if err != nil || !filter.matches(SearchResult{Extension: extension}) {
			t.Errorf("category %q did not match %q: %v", category, extension, err)
		}
	}
	for _, expression := range []string{`wat:true`, `in:[`, `type:audio,!`, `country:`, `country:U`, `country:USA`, `country:US,`, `country:U1`, `country:!!US`, `size:12XB`, `duration:1:60`, `free:maybe`, `in:"open`} {
		if _, err := parseSearchFilter(expression); !errors.Is(err, ErrInvalidFilter) {
			t.Errorf("parseSearchFilter(%q) error = %v", expression, err)
		}
	}
}

func TestFilteredSearchPagesUseFullCache(t *testing.T) {
	results := make([]SearchResult, 205)
	for i := range results {
		extension := "mp3"
		if i%2 == 0 {
			extension = "flac"
		}
		results[i] = SearchResult{Path: extension, Extension: extension, Public: true}
	}
	search := Search{ID: "search", Query: "music", Results: results}
	first, err := searchPage(search, 0, "type:flac")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Results) != 100 || first.Total != 103 || first.FoundTotal != 205 || first.NextCursor != 100 {
		t.Fatalf("first filtered page: %+v", first)
	}
	second, err := searchPage(search, first.NextCursor, "type:flac")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Results) != 3 || second.Results[0].Extension != "flac" || second.NextCursor != 0 {
		t.Fatalf("second filtered page: %+v", second)
	}

	service := &Service{searches: map[string]Search{"search": search}}
	if _, err := service.SearchPage("search", 0, "size:nope"); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("invalid page filter: %v", err)
	}
	page, err := service.SearchPage("search", 0, "")
	if err != nil || page.FoundTotal != 205 {
		t.Fatalf("cache changed after invalid filter: %+v %v", page, err)
	}
}
