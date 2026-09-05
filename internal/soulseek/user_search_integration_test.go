package soulseek

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestSoulfindTargetedSearch(t *testing.T) {
	addr := soulfindAddress(t)
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	name := "targeted-" + stamp + ".flac"
	a := startSoulfindClient(t, addr, "a"+stamp, map[string][]byte{name: []byte("one")}, nil)
	b := startSoulfindClient(t, addr, "b"+stamp, map[string][]byte{name: []byte("two")}, nil)
	observer := startSoulfindClient(t, addr, "c"+stamp, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, targets := range [][]string{{a.cfg.Username}, {a.cfg.Username, b.cfg.Username}, {"missing" + stamp}} {
		results, err := observer.Search(ctx, name, targets...)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		if len(targets) == 1 && targets[0] == "missing"+stamp {
			if len(results) != 0 {
				t.Fatal("missing target fell back to global")
			}
			continue
		}
		for _, user := range targets {
			want[user] = true
		}
		seen := map[string]bool{}
		for _, r := range results {
			if !want[r.Username] {
				t.Fatalf("out of scope: %+v", r)
			}
			seen[r.Username] = true
		}
		if len(seen) != len(want) {
			t.Fatalf("target search missed results: targets=%v results=%+v", targets, results)
		}
	}
}
