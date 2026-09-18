package tui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
)

func uploadModel() model {
	m := model{workspace: workspaceTransfers, transferTab: transferUploads, width: 80, height: 24, ctx: context.Background(), transfers: []transfer{
		{id: "upload:a:one", user: "a", filename: `Music\one`, direction: "upload", state: "queued"},
		{id: "upload:a:two", user: "a", filename: `Other\two`, direction: "upload", state: "failed"},
		{id: "upload:b:one", user: "b", filename: `Music\one`, direction: "upload", state: "running"},
		{id: "d-1", user: "a", filename: `Music\one`, direction: "download", state: "running"},
	}}
	m.transferTrees[transferUploads], _ = buildTransferTree(m.transfers, "upload", treeState{}, 0)
	return m
}
func pressUpload(m *model, key rune) tea.Cmd { return m.key(tea.KeyPressMsg(tea.Key{Code: key})) }

func TestUploadSelectionAndPolling(t *testing.T) {
	m := uploadModel()
	if ids := m.uploadActionIDs(); len(ids) != 2 {
		t.Fatalf("user subtree %v", ids)
	}
	m.toggleUploadSelection()
	tree := &m.transferTrees[transferUploads]
	if got := treeSelectionIDs(tree, tree.roots[0], m.transfers, m.uploadSelected); got != "●" {
		t.Fatal(got)
	}
	delete(m.uploadSelected, "upload:a:two")
	if got := treeSelectionIDs(tree, tree.roots[0], m.transfers, m.uploadSelected); got != "◐" {
		t.Fatal(got)
	}
	m.cursor = tree.cursorForSource(2)
	if ids := m.uploadActionIDs(); !reflect.DeepEqual(ids, []string{"upload:a:one"}) {
		t.Fatalf("marks must override cursor %v", ids)
	}
	before := m.transfers
	next, _ := m.Update(transferMsg{transfers: []transfer{before[2], before[1], before[0], before[3]}})
	m = next.(model)
	if ids := m.uploadActionIDs(); !reflect.DeepEqual(ids, []string{"upload:a:one"}) {
		t.Fatalf("mark moved after sort %v", ids)
	}
	m.switchTransferTab(transferDownloads)
	m.switchTransferTab(transferUploads)
	failIf(t, !m.uploadSelected["upload:a:one"], "tab switch removed mark")
	next, _ = m.Update(transferMsg{transfers: []transfer{before[2]}})
	m = next.(model)
	failIf(t, len(m.uploadSelected) != 0, "removed selection not pruned")
}

func TestUploadKeysBatchAndConfirmations(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	path := filepath.Join(t.TempDir(), "ipc.sock")
	ln, err := net.Listen("unix", path)
	must(t, err)
	requests := make(chan daemon.UploadActionRequest, 10)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/uploads/actions" {
			var req daemon.UploadActionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			requests <- req
			_ = json.NewEncoder(w).Encode(daemon.UploadActionResult{Changed: 2, Skipped: 1})
		} else {
			_ = json.NewEncoder(w).Encode([]daemon.Transfer{})
		}
	})}
	go server.Serve(ln)
	defer server.Close()
	m := uploadModel()
	m.client = ipc.NewClient(path)
	m.uploadSelected = map[string]bool{"upload:a:one": true, "upload:b:one": true}
	cmd := pressUpload(&m, 'd')
	failIf(t, cmd == nil || m.uploadConfirm, "d must abort selection directly")
	msg := cmd().(transferActionMsg)
	failIf(t, msg.result.Changed != 2, msg)
	req := <-requests
	failIfFmt(t, req.Action != "cancel" || len(req.IDs) != 2 || len(req.Usernames) != 0, "d scope %+v", req)
	cmd = pressUpload(&m, 'D')
	failIf(t, cmd != nil || !m.uploadConfirm || m.uploadConfirmChoice != 0, "D must confirm, default No")
	if !reflect.DeepEqual(m.uploadPending.Usernames, []string{"a", "b"}) {
		t.Fatal(m.uploadPending)
	}
	if cmd = m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); cmd != nil || m.uploadConfirm {
		t.Fatal("default Enter performed abort")
	}
	pressUpload(&m, 'D')
	cmd = pressUpload(&m, 'y')
	cmd()
	req = <-requests
	failIf(t, req.Action != "cancel" || len(req.Usernames) != 2 || len(req.IDs) != 0, req)
	pressUpload(&m, 'c')
	failIf(t, !m.uploadConfirm || !strings.Contains(m.uploadConfirmLabel, "stop"), "selected clear must warn")
	if cmd = m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})); cmd != nil {
		t.Fatal("Escape submitted")
	}
	for i, scope := range uploadClearScopes {
		pressUpload(&m, 'C')
		m.uploadStatusChoice = i
		cmd = m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		if i >= 5 {
			failIf(t, cmd != nil || !m.uploadConfirm || m.uploadConfirmChoice != 0, "live clear lacks confirmation")
			cmd = pressUpload(&m, 'y')
		}
		failIf(t, cmd == nil, "clear did not submit")
		cmd()
		req = <-requests
		failIfFmt(t, len(req.IDs) != 0 || len(req.Usernames) != 0 || req.All != scope.all || !reflect.DeepEqual(req.States, scope.states), "global clear scope %+v", req)
	}
	for _, width := range []int{24, 40, 80} {
		m.width = width
		for _, view := range []string{m.uploadConfirmView(), m.uploadStatusMenuView(), m.renderTransfers(width, 12)} {
			failIf(t, strings.Contains(view, "\x1b["), "NO_COLOR ignored")
			for _, line := range strings.Split(view, "\n") {
				failIfFmt(t, lipgloss.Width(line) > width, "view exceeds %d: %q", width, line)
			}
		}
	}
}
