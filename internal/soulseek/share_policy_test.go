package soulseek

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func permissionTestClient(t *testing.T, policy func(string, netip.Addr) SharePermission) *Client {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"Public", "Locked", "Hidden"} {
		must(t, os.Mkdir(filepath.Join(root, name), 0700))
		must(t, os.WriteFile(filepath.Join(root, name, "song.mp3"), []byte(name), 0600))
	}
	index := NewShareIndex()
	for _, name := range []string{"Public", "Locked", "Hidden"} {
		must(t, index.AddRoot(name, filepath.Join(root, name)))
	}
	must(t, index.ScanContext(context.Background()))
	return NewClient(ClientConfig{Share: index, SharePolicy: policy, IncomingSearch: &IncomingSearchPolicy{Respond: true, MaximumResults: 2}})
}

func TestSharePolicyServingMatrix(t *testing.T) {
	wantAddress := netip.MustParseAddr("192.0.2.7")
	var calls atomic.Int32
	client := permissionTestClient(t, func(username string, address netip.Addr) SharePermission {
		if username != "peer" || address != wantAddress {
			t.Errorf("policy identity = %q/%s", username, address)
		}
		calls.Add(1)
		return SharePermission{Roots: map[string]ShareVisibility{
			"Public": ShareAllowed,
			"Locked": ShareLocked,
			"Hidden": ShareHidden,
		}}
	})
	defer client.Close()

	permission := client.sharePermission("peer", wantAddress)
	results := client.incomingSearchResultsFor("song", "peer", wantAddress)
	failIfFmt(t, len(results) != 2, "search policy filtering = %+v", results)
	for _, result := range results {
		failIfFmt(t, shareRoot(result.Path) == "Hidden", "hidden search result disclosed: %+v", result)
		failIfFmt(t, shareRoot(result.Path) == "Locked" && result.Public, "locked search result marked public: %+v", result)
	}
	entries := client.shareEntriesFor("peer", wantAddress)
	failIfFmt(t, len(entries) != 4, "shared list entries = %d, want public+locked files/directories", len(entries))
	for _, entry := range entries {
		failIfFmt(t, shareRoot(entry.Name) == "Hidden", "hidden entry disclosed: %+v", entry)
		failIfFmt(t, shareRoot(entry.Name) == "Locked" && !entry.Private, "locked entry not marked private: %+v", entry)
	}
	locked, err := client.shareIndex().Subtree("Locked")
	must(t, err)
	locked = client.filterShareEntries(locked, permission)
	failIf(t, len(locked) != 0, "folder wire format cannot mark locked files")
	hidden, err := client.shareIndex().Subtree("Hidden")
	must(t, err)
	if got := client.filterShareEntries(hidden, permission); len(got) != 0 {
		t.Fatalf("hidden folder disclosed: %+v", got)
	}
	failIf(t, calls.Load() != 3, "serving paths did not query the policy")
}

func TestSharePolicyUploadAdmissionAndStreamRevalidation(t *testing.T) {
	var allow atomic.Bool
	allow.Store(true)
	client := permissionTestClient(t, func(_ string, _ netip.Addr) SharePermission {
		if allow.Load() {
			return SharePermission{Roots: map[string]ShareVisibility{"Public": ShareAllowed}}
		}
		return SharePermission{Banned: true, Reason: "blocked"}
	})
	defer client.Close()

	if _, _, err := client.registerUploadWithAddress("peer", "Locked/song.mp3", false, netip.MustParseAddr("127.0.0.1"), false, ""); err == nil {
		t.Fatal("locked upload admitted")
	}
	if _, _, err := client.registerUploadWithAddress("peer", "Public/song.mp3", false, netip.MustParseAddr("127.0.0.1"), false, ""); err != nil {
		t.Fatal(err)
	}
	client.StopUploads([]UploadTarget{{Username: "peer", Filename: `Public\song.mp3`, Attempt: 1}}, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempt := &uploadAttempt{target: UploadTarget{Username: "peer", Filename: `Public\song.mp3`}, address: netip.MustParseAddr("127.0.0.1"), cancel: cancel, ctx: ctx, observation: client.newObservation(ctx, "upload", 0, 2), job: &UploadJob{Request: TransferRequest{Size: 2}}}
	defer client.endObservation(attempt.observation)
	writer := uploadProgressWriter{Writer: new(bytes.Buffer), client: client, attempt: attempt}
	if n, err := writer.Write([]byte("a")); err != nil || n != 1 {
		t.Fatalf("first stream write = %d/%v", n, err)
	}
	allow.Store(false)
	if n, err := writer.Write([]byte("b")); err == nil || n != 0 {
		t.Fatalf("revoked stream write = %d/%v", n, err)
	}
}

func TestSharePolicyLegacyAndEmptyRoots(t *testing.T) {
	legacy := permissionTestClient(t, nil)
	defer legacy.Close()
	failIf(t, len(legacy.shareEntries()) == 0, "nil callback did not preserve public legacy shares")
	empty := permissionTestClient(t, func(string, netip.Addr) SharePermission {
		return SharePermission{Roots: map[string]ShareVisibility{}}
	})
	defer empty.Close()
	if len(empty.shareEntriesFor("peer", netip.Addr{})) != 0 {
		t.Fatal("explicit empty roots disclosed shares")
	}
}

func TestSharePermissionSnapshotCopiesRoots(t *testing.T) {
	var roots = map[string]ShareVisibility{"Public": ShareAllowed}
	client := permissionTestClient(t, func(string, netip.Addr) SharePermission { return SharePermission{Roots: roots} })
	defer client.Close()
	permission := client.sharePermission("peer", netip.Addr{})
	roots["Public"] = ShareHidden
	failIf(t, permission.Roots["Public"] != ShareAllowed, "permission was not snapshotted")
}
