package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
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

func TestTransferWaitingAndRetryStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, tc := range []struct {
		state, direction string
		wait, retry      *uint64
		want             string
	}{
		{"running", "download", nil, nil, "running"},
		{"running", "download", timingValue(29), nil, "running"},
		{"running", "download", timingValue(30), nil, "Waiting for peer data · 30s"},
		{"running", "download", timingValue(42), nil, "Waiting for peer data · 42s"},
		{"running", "download", timingValue(0), nil, "running"},
		{"retrying", "download", timingValue(42), timingValue(65), "Retry in 1:05"},
		{"retrying", "download", nil, timingValue(0), "Retry pending"},
		{"retrying", "download", nil, nil, "retrying"},
		{"paused", "download", timingValue(42), timingValue(65), "paused"},
		{"completed", "download", timingValue(42), timingValue(65), "completed"},
		{"queued", "download", timingValue(42), nil, "queued"},
		{"running", "upload", timingValue(42), nil, "running"},
	} {
		rows := toTransfers([]daemon.Transfer{{ID: "d-1", State: tc.state, Direction: tc.direction, WaitingForPeerSeconds: tc.wait, RetryInSeconds: tc.retry}})
		if got := transferStatus(rows[0], false); got != tc.want {
			t.Fatalf("%+v: got %q", tc, got)
		}
	}
	m := model{workspace: workspaceTransfers}
	for _, tc := range []struct {
		row  daemon.Transfer
		want string
	}{
		{daemon.Transfer{State: "running", WaitingForPeerSeconds: timingValue(42)}, "Waiting for peer data · 42s"},
		{daemon.Transfer{State: "retrying", RetryInSeconds: timingValue(65), Error: "remote upload failed"}, "Retry in 1:05: remote upload failed"},
		{daemon.Transfer{State: "retrying", RetryInSeconds: timingValue(64), Error: "remote upload failed"}, "Retry in 1:04: remote upload failed"},
		{daemon.Transfer{State: "retrying", RetryInSeconds: timingValue(0), Error: "remote upload failed"}, "Retry pending: remote upload failed"},
		{daemon.Transfer{State: "running", WaitingForPeerSeconds: timingValue(0)}, "running"},
	} {
		tc.row.ID, tc.row.Username, tc.row.Filename, tc.row.Direction = "d-1", "peer", "album/file", "download"
		updated, _ := m.Update(transferMsg{transfers: toTransfers([]daemon.Transfer{tc.row})})
		m = updated.(model)
		m.cursor = m.transferTrees[transferDownloads].cursorForSource(0)
		for _, width := range []int{40, 60, 90, 109, 110, 140} {
			view := m.renderTransfers(width, 12)
			if !strings.Contains(view, tc.want) {
				t.Fatalf("missing %q at width %d: %q", tc.want, width, view)
			}
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("overflow at %d: %q", width, line)
				}
			}
		}
		if tc.row.State == "running" && *tc.row.WaitingForPeerSeconds == 0 && strings.Contains(m.renderTransfers(140, 12), "Waiting") {
			t.Fatal("recovery retained waiting status")
		}
	}
}
