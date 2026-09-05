package daemon

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

var ErrUploadUnavailable = errors.New("daemon: connect before retrying uploads")

type UploadActionRequest struct {
	Action    string   `json:"action"`
	IDs       []string `json:"ids,omitempty"`
	Usernames []string `json:"usernames,omitempty"`
	States    []string `json:"states,omitempty"`
	All       bool     `json:"all,omitempty"`
}
type UploadActionError struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}
type UploadActionResult struct {
	Changed int                 `json:"changed"`
	Skipped int                 `json:"skipped"`
	Errors  []UploadActionError `json:"errors"`
}
type uploadOwner struct {
	session uint64
	target  soulseek.UploadTarget
}

func uploadID(username, filename string) string { return "upload:" + username + ":" + filename }
func liveUpload(state string) bool              { return state == "queued" || state == "running" }

func (s *Service) uploadUpdate(session uint64, event soulseek.TransferEvent) {
	if event.State == "queued" {
		if err := s.uploadAccepted(session, event); err != nil {
			log.Printf("upload admission: %v", err)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || session != s.uploadEpoch {
		s.statsRetiredUploadLocked(session, event)
		return
	}
	id := s.uploadEventIDLocked(event)
	owner := uploadOwner{session: session, target: soulseek.UploadTarget{Username: event.Username, Filename: event.Filename, Attempt: event.Attempt}}
	if id == "" || s.uploadOwners[id] != owner || !liveUpload(s.transfers[id].State) {
		return
	}
	s.progressTransferLocked(id, event.Done)
	oldState := s.transfers[id].State
	s.transfers[id] = Transfer{ID: id, Username: event.Username, Filename: event.Filename, Direction: "upload", State: event.State, Done: event.Done, Total: event.Total, Error: event.Error}
	if s.telemetry != nil {
		s.telemetry.dirtyUploads[id] = true
	}
	if event.State != "running" {
		s.stopTransferLocked(id)
	}
	if oldState != event.State {
		s.statsStateLocked(id, event.State)
		if err := s.persistUploadLocked(id); err != nil {
			log.Printf("save upload: %v", err)
			if s.telemetry != nil {
				s.telemetry.warning = "Persistence: " + err.Error()
			}
			return
		}
	}
	if event.State == "completed" && s.cfg.Uploads.AutoClearCompleted {
		tr := s.transfers[id]
		delete(s.transfers, id)
		if err := s.persistUploadLocked(id); err != nil {
			s.transfers[id] = tr
			log.Printf("clear upload: %v", err)
			return
		}
		s.forgetTransferLocked(id)
	}
}

func (s *Service) uploadStreamStart(session uint64, event soulseek.TransferEvent) {
	if event.Direction != "upload" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || session != s.uploadEpoch {
		s.statsRetiredUploadLocked(session, event)
		return
	}
	id := s.uploadEventIDLocked(event)
	owner := uploadOwner{session: session, target: soulseek.UploadTarget{Username: event.Username, Filename: event.Filename, Attempt: event.Attempt}}
	if s.uploadOwners[id] != owner || s.transfers[id].ID == "" {
		return
	}
	s.startTransferLocked(id, event.Done)
}

// Retire callbacks before closing a session. History remains available offline.
func (s *Service) retireUploadsLocked() {
	s.uploadEpoch++
	for id := range s.uploadOwners {
		if _, exists := s.transfers[id]; !exists {
			delete(s.uploadOwners, id)
		}
	}
	for id, transfer := range s.transfers {
		if transfer.Direction == "upload" && liveUpload(transfer.State) {
			transfer.State, transfer.Error = "interrupted", "Soulseek connection closed; waiting to recover"
			s.transfers[id] = transfer
			s.stopTransferLocked(id)
			s.statsStateLocked(id, "interrupted")
			if err := s.persistUploadLocked(id); err != nil {
				log.Printf("persist interrupted upload: %v", err)
			}
		}
	}
}

func validateUploadAction(req UploadActionRequest) error {
	selectors := 0
	for _, count := range []int{len(req.IDs), len(req.Usernames), len(req.States)} {
		if count > 0 {
			selectors++
		}
	}
	if req.All {
		selectors++
	}
	if selectors != 1 {
		return errors.New("daemon: specify exactly one nonempty upload selector")
	}
	for _, values := range [][]string{req.IDs, req.Usernames, req.States} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return errors.New("daemon: empty upload selector value")
			}
		}
	}
	for _, state := range req.States {
		switch state {
		case "completed", "cancelled", "failed", "queued", "interrupted":
		default:
			return fmt.Errorf("daemon: invalid upload state %q", state)
		}
	}
	switch req.Action {
	case "retry":
		if len(req.IDs) == 0 {
			return errors.New("daemon: retry requires upload IDs")
		}
	case "cancel":
		if len(req.IDs) == 0 && len(req.Usernames) == 0 {
			return errors.New("daemon: cancel requires upload IDs or usernames")
		}
	case "clear":
		if len(req.Usernames) > 0 {
			return errors.New("daemon: clear requires upload IDs, states or all")
		}
	default:
		return fmt.Errorf("daemon: unsupported upload action %q", req.Action)
	}
	return nil
}

func (s *Service) UploadAction(req UploadActionRequest) (UploadActionResult, error) {
	result := UploadActionResult{Errors: []UploadActionError{}}
	if err := validateUploadAction(req); err != nil {
		return result, err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return result, ErrClosed
	}
	// Reject the entire request, not just the offending download in a mixed batch.
	for _, id := range req.IDs {
		if t, ok := s.transfers[id]; (ok && t.Direction != "upload") || (!ok && strings.HasPrefix(id, "d-")) {
			s.mu.Unlock()
			return result, errors.New("daemon: upload action cannot target downloads")
		}
	}
	client := s.client
	if req.Action == "retry" {
		for _, id := range req.IDs {
			for _, u := range s.journal.Uploads {
				if u.ID == id && u.Account != s.uploadAccountLocked(s.uploadEpoch) {
					s.mu.Unlock()
					return result, errors.New("select this upload's local account before retrying")
				}
			}
		}
	}
	if req.Action == "retry" && (client == nil || s.status != StatusConnected) {
		s.mu.Unlock()
		return result, ErrUploadUnavailable
	}
	ids := make(map[string]bool)
	for _, id := range req.IDs {
		ids[id] = true
	}
	users, states := make(map[string]bool), make(map[string]bool)
	for _, user := range req.Usernames {
		users[user] = true
	}
	for _, state := range req.States {
		states[state] = true
	}
	if len(req.IDs) == 0 {
		for id, t := range s.transfers {
			if t.Direction == "upload" && (req.All || users[t.Username] || states[t.State]) {
				ids[id] = true
			}
		}
	}
	type selected struct {
		transfer Transfer
		owner    uploadOwner
	}
	targets := make([]selected, 0, len(ids))
	for id := range ids {
		t, exists := s.transfers[id]
		if !exists {
			result.Errors = append(result.Errors, UploadActionError{id, os.ErrNotExist.Error()})
			continue
		}
		targets = append(targets, selected{t, s.uploadOwners[id]})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].transfer.ID < targets[j].transfer.ID })
	s.mu.Unlock()
	if req.Action != "retry" && client != nil {
		var live []soulseek.UploadTarget
		for _, item := range targets {
			if liveUpload(item.transfer.State) {
				live = append(live, item.owner.target)
			}
		}
		client.StopUploads(live, req.Action == "cancel")
	}
	if req.Action == "clear" {
		if err := s.flushStats(); err != nil {
			return result, err
		}
	}
	for _, item := range targets {
		id := item.transfer.ID
		s.mu.Lock()
		current, exists := s.transfers[id]
		if !exists || s.uploadOwners[id] != item.owner {
			s.mu.Unlock()
			result.Skipped++
			continue
		}
		switch req.Action {
		case "retry":
			if current.State != "failed" && current.State != "cancelled" && current.State != "interrupted" {
				s.mu.Unlock()
				result.Skipped++
				continue
			}
			s.mu.Unlock()
			// No daemon mutex across the synchronous queued callback or network work.
			changed, err := client.QueueUpload(current.Username, current.Filename)
			if err != nil {
				result.Errors = append(result.Errors, UploadActionError{id, err.Error()})
			} else if changed {
				result.Changed++
			} else {
				result.Skipped++
			}
		case "cancel":
			if current.State == "completed" || item.transfer.State == "cancelled" {
				result.Skipped++
			} else {
				previousState := s.snapshotLocked(id)
				current.State, current.Error = "cancelled", ""
				s.transfers[id] = current
				s.stopTransferLocked(id)
				s.statsStateLocked(id, "cancelled")
				result.Changed++
				if err := s.persistUploadLocked(id); err != nil {
					s.restoreLocked(previousState)
					result.Changed--
					result.Errors = append(result.Errors, UploadActionError{id, err.Error()})
				}
			}
			s.mu.Unlock()
		case "clear":
			delete(s.transfers, id)
			if err := s.persistUploadLocked(id); err != nil {
				s.transfers[id] = current
				result.Errors = append(result.Errors, UploadActionError{id, err.Error()})
				s.mu.Unlock()
				continue
			}
			// Retain the attempt watermark until this session is retired.
			s.forgetTransferLocked(id)
			result.Changed++
			s.mu.Unlock()
		}
	}
	sort.Slice(result.Errors, func(i, j int) bool { return result.Errors[i].ID < result.Errors[j].ID })
	return result, nil
}
