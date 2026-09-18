package tui

import (
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/diagnostics"
)

func TestFilterLogRecords(t *testing.T) {
	records := []diagnostics.Record{
		{Level: "INFO", Msg: "share scan done"},
		{Level: "WARN", Msg: "peer slow", Text: "username=bob"},
		{Level: "ERROR", Msg: "download failed"},
	}
	if got := filterLogRecords(records, "", ""); len(got) != 3 {
		t.Fatalf("no filter = %d, want 3", len(got))
	}
	if got := filterLogRecords(records, "WARN", ""); len(got) != 2 || got[0].Level != "WARN" {
		t.Fatalf("warn filter = %+v", got)
	}
	if got := filterLogRecords(records, "ERROR", ""); len(got) != 1 || got[0].Level != "ERROR" {
		t.Fatalf("error filter = %+v", got)
	}
	if got := filterLogRecords(records, "", "BOB"); len(got) != 1 || got[0].Msg != "peer slow" {
		t.Fatalf("search = %+v", got)
	}
}

func TestDigitWorkspaceJump(t *testing.T) {
	m := model{cfg: config.Default()}
	m.key(key("6"))
	if m.workspace != workspaceStats {
		t.Fatalf("digit 6 went to workspace %d, want %d", m.workspace, workspaceStats)
	}
	m.key(key("1"))
	if m.workspace != workspaceSearch {
		t.Fatalf("digit 1 went to workspace %d, want %d", m.workspace, workspaceSearch)
	}
}
