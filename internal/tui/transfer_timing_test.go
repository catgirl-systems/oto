package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func timingValue(n uint64) *uint64 { return &n }

func TestTransferTimingRows(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{workspace: workspaceTransfers, transfers: []transfer{
		{id: "d1", user: "peer", filename: "album/a.flac", state: "running", direction: "download", done: 100, total: 201, speed: 100, elapsedMS: timingValue(1000), etaSeconds: timingValue(2), err: strings.Repeat("long error ", 10)},
		{id: "d2", user: "peer", filename: "album/b.flac", state: "queued", direction: "download", total: 100},
	}}
	m.transferTrees[transferDownloads], m.cursor = buildTransferTree(m.transfers, "download", treeState{}, 0)
	group := treeNode{kind: treeUser, leaves: []int{0, 1}}
	elapsed, eta := m.transferTimes(group)
	if elapsed == nil || *elapsed != 1000 || eta == nil || *eta != 3 {
		t.Fatalf("group elapsed/eta = %v/%v", elapsed, eta)
	}
	m.transfers[1].state = "paused"
	if _, eta := m.transferTimes(group); eta != nil {
		t.Fatal("paused child has aggregate ETA")
	}
	m.transfers[1].state = "queued"
	for _, width := range []int{60, 109, 110, 140} {
		view := m.renderTransfers(width, 12)
		if !strings.Contains(view, "Elapsed") || !strings.Contains(view, "ETA") || strings.Contains(view, "\x1b[") {
			t.Fatalf("missing/unreadable timing at %d: %q", width, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("line overflow at %d: %q", width, line)
			}
		}
	}
	if addSaturated(^uint64(0), 1) != ^uint64(0) || optionalDuration(nil, false) != "—" {
		t.Fatal("overflow/unknown formatting")
	}
}
