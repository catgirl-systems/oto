package soulseek

import (
	"net/netip"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestUploadSnapshotRejectsChangedFilesAndUsesNormalTransfer(t *testing.T) {
	address, _, received := uploadPeer(t, "normal", 0)
	c, events, path := uploadClient(t, address, []byte("first"))
	snapshot, err := c.PreviewUpload("peer", `Music\song`)
	failIf(t, err != nil || snapshot.Size != 5 || snapshot.Fingerprint == "", snapshot, err)
	select {
	case e := <-events:
		t.Fatal("preview queued upload", e)
	default:
	}
	must(t, os.WriteFile(path, []byte("replacement"), 0600))
	if _, _, err := c.QueueUploadSnapshot("peer", snapshot); err == nil {
		t.Fatal("changed file accepted")
	}
	snapshot, err = c.PreviewUpload("peer", `Music\song`)
	must(t, err)
	target, started, err := c.QueueUploadSnapshot("peer", snapshot)
	failIf(t, err != nil || !started || target.Attempt == 0, target, started, err)
	uploadEvent(t, events, "completed")
	select {
	case body := <-received:
		failIf(t, string(body) != "replacement", "wrong bytes")
	case <-time.After(time.Second):
		t.Fatal("no received bytes")
	}
	if _, err := c.PreviewUpload("peer", path); err == nil {
		t.Fatal("arbitrary local path accepted")
	}
	if _, _, err := c.QueueUploadSnapshot("peer", UploadSnapshot{Filename: snapshot.Filename}); err == nil {
		t.Fatal("missing fingerprint")
	}
}
func TestUploadSnapshotRechecksPermissionsAndReturnsPreviewFailures(t *testing.T) {
	var deny atomic.Bool
	c := permissionTestClient(t, func(string, netip.Addr) SharePermission {
		if deny.Load() {
			return SharePermission{Banned: true, Reason: "blocked"}
		}
		return SharePermission{Roots: map[string]ShareVisibility{"Public": ShareAllowed, "Locked": ShareLocked}}
	})
	defer c.Close()
	locked, err := c.PreviewUpload("peer", `Locked\song.mp3`)
	failIf(t, err == nil || locked.Size == 0 || locked.Filename == "", "permission failure missing preview metadata", locked, err)
	snapshot, err := c.PreviewUpload("peer", `Public\song.mp3`)
	must(t, err)
	deny.Store(true)
	if _, _, err := c.QueueUploadSnapshot("peer", snapshot); err == nil {
		t.Fatal("revoked permission accepted")
	}
	if _, err := c.PreviewUpload("peer", `Public\song.mp3`); err == nil {
		t.Fatal("banned preview accepted")
	}
}
