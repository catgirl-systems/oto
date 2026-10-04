package daemon

import "testing"

func TestTransfersSinceSkipsOnlyStaticUnchangedLists(t *testing.T) {
	s := &Service{transfers: map[string]Transfer{
		"d-1": {ID: "d-1", Username: "alice", Filename: "a.flac", Direction: "download", State: "completed", Done: 10, Total: 10},
		"d-2": {ID: "d-2", Username: "bob", Filename: "b.flac", Direction: "download", State: "failed", Error: "gone", Total: 5},
	}}
	first := s.TransfersSince("")
	failIfFmt(t, first.Unchanged || len(first.Transfers) != 2 || first.Fingerprint == "", "first poll %+v", first)
	again := s.TransfersSince(first.Fingerprint)
	failIfFmt(t, !again.Unchanged || again.Transfers != nil || again.Fingerprint != first.Fingerprint, "static list resent: %+v", again)

	changed := s.transfers["d-2"]
	changed.Error = "different"
	s.transfers["d-2"] = changed
	afterEdit := s.TransfersSince(first.Fingerprint)
	failIf(t, afterEdit.Unchanged || afterEdit.Fingerprint == first.Fingerprint, "a changed field was reported unchanged")

	active := s.transfers["d-1"]
	active.State = "running"
	s.transfers["d-1"] = active
	running := s.TransfersSince("")
	failIf(t, s.TransfersSince(running.Fingerprint).Unchanged, "active transfers have live clocks and must always be resent")

	empty := (&Service{transfers: map[string]Transfer{}}).TransfersSince("")
	failIf(t, !(&Service{transfers: map[string]Transfer{}}).TransfersSince(empty.Fingerprint).Unchanged, "an empty list was resent")
}
