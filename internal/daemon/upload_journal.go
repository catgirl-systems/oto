package daemon

import (
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/stats"
)

type Upload struct {
	QueueOrder uint64 `json:"queue_order"`
	Transfer
	Account     string    `json:"account"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
	QueuedAt    time.Time `json:"queued_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Recoverable bool      `json:"recoverable,omitempty"`
}

func accountKey(c config.Config) string {
	host, port, err := net.SplitHostPort(c.Soulseek.Server)
	server := c.Soulseek.Server
	if err == nil {
		server = net.JoinHostPort(strings.ToLower(host), port)
	}
	return server + "/" + c.Soulseek.Username
}
func uploadKey(account, user, file string) string { return account + "\x00" + user + "\x00" + file }
func (s *Service) uploadAccountLocked(epoch uint64) string {
	if account := s.uploadAccounts[epoch]; account != "" {
		return account
	}
	return accountKey(s.cfg)
}
func (s *Service) uploadEventIDLocked(e soulseek.TransferEvent) string {
	return s.uploadKeys[uploadKey(s.uploadAccountLocked(s.uploadEpoch), e.Username, e.Filename)]
}

// uploadAccepted commits admission before the client starts network work.
func (s *Service) uploadAccepted(session uint64, e soulseek.TransferEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || session != s.uploadEpoch {
		return errors.New("daemon: upload session retired")
	}
	if s.uploadKeys == nil {
		s.uploadKeys = make(map[string]string)
	}
	if s.uploadOwners == nil {
		s.uploadOwners = make(map[string]uploadOwner)
	}
	key := uploadKey(s.uploadAccountLocked(session), e.Username, e.Filename)
	id := s.uploadKeys[key]
	previous := s.uploadOwners[id]
	if previous.session == session && previous.target.Attempt >= e.Attempt {
		return nil
	}
	old := slices.Clone(s.journal.Uploads)
	sequence := s.journal.UploadSequence
	pending := []stats.Event(nil)
	if s.telemetry != nil {
		pending = slices.Clone(s.telemetry.pending)
	}
	queueSequence := s.journal.UploadQueueSequence
	at := time.Now().UTC()
	index := -1
	for i := range s.journal.Uploads {
		if s.journal.Uploads[i].ID == id && s.journal.Uploads[i].State != "completed" {
			index = i
			break
		}
	}
	if index < 0 {
		if s.journal.UploadSequence == ^uint64(0) {
			return errors.New("upload ID sequence exhausted")
		}
		s.journal.UploadSequence++
		id = fmt.Sprintf("upload:%d", s.journal.UploadSequence)
		s.journal.Uploads = append(s.journal.Uploads, Upload{Account: s.uploadAccountLocked(session), CreatedAt: at})
		index = len(s.journal.Uploads) - 1
	}
	tr := Transfer{ID: id, Username: e.Username, Filename: e.Filename, Direction: "upload", State: "queued", Total: e.Total}
	u := &s.journal.Uploads[index]
	if (!e.Restored || u.QueueOrder == 0) && queueSequence == ^uint64(0) {
		s.journal.Uploads, s.journal.UploadSequence = old, sequence
		return errors.New("upload queue sequence exhausted")
	}
	u.QueuedAt = at
	u.Transfer, u.Fingerprint, u.UpdatedAt, u.Recoverable = tr, e.Fingerprint, at, true
	if !e.Restored || u.QueueOrder == 0 {
		s.journal.UploadQueueSequence++
		u.QueueOrder = s.journal.UploadQueueSequence
	}
	event := stats.Event{Account: u.Account, TransferID: id, Peer: e.Username, Filename: e.Filename, Direction: "upload", Kind: stats.KindQueued}
	s.statsEventLocked(event)
	if err := s.persistUploadRowLocked(*u); err != nil {
		s.journal.Uploads, s.journal.UploadSequence = old, sequence
		s.journal.UploadQueueSequence = queueSequence
		if s.telemetry != nil {
			s.telemetry.pending = pending
		}
		return err
	}
	s.uploadKeys[key] = id
	s.uploadOwners[id] = uploadOwner{session: session, target: soulseek.UploadTarget{Username: e.Username, Filename: e.Filename, Attempt: e.Attempt}}
	s.transfers[id] = tr
	s.prepareTransferLocked(id)
	return nil
}

func (s *Service) persistUploadLocked(id string) error {
	tr, exists := s.transfers[id]
	for i := range s.journal.Uploads {
		if s.journal.Uploads[i].ID != id {
			continue
		}
		if !exists {
			if err := s.deleteUploadRowLocked(id); err != nil {
				return err
			}
			s.journal.Uploads = slices.Delete(s.journal.Uploads, i, i+1)
			return nil
		}
		u := &s.journal.Uploads[i]
		u.Transfer, u.UpdatedAt = tr, time.Now().UTC()
		u.Recoverable = liveUpload(tr.State) || tr.State == "interrupted"
		return s.persistUploadRowLocked(*u)
	}
	return nil
}

func (s *Service) restoreUploadJournal() {
	s.uploadKeys = make(map[string]string)
	s.uploadOwners = make(map[string]uploadOwner)
	for i := range s.journal.Uploads {
		u := &s.journal.Uploads[i]
		if liveUpload(u.State) {
			u.State, u.Recoverable, u.Error = "interrupted", true, "Daemon interrupted; waiting for connection"
		}
		u.ElapsedMS, u.ETASeconds, u.SpeedBPS = nil, nil, 0
		s.transfers[u.ID] = u.Transfer
		s.uploadKeys[uploadKey(u.Account, u.Username, u.Filename)] = u.ID
	}
}

func (s *Service) recoverUploads(client *soulseek.Client, epoch uint64) {
	s.mu.RLock()
	account := s.uploadAccountLocked(epoch)
	uploads := slices.Clone(s.journal.Uploads)
	slices.SortStableFunc(uploads, func(a, b Upload) int {
		if a.QueueOrder < b.QueueOrder {
			return -1
		}
		if a.QueueOrder > b.QueueOrder {
			return 1
		}
		return 0
	})
	s.mu.RUnlock()
	for _, u := range uploads {
		if u.Account != account || !u.Recoverable {
			continue
		}
		s.mu.RLock()
		valid := !s.closed && s.client == client && s.uploadEpoch == epoch
		s.mu.RUnlock()
		if !valid {
			return
		}
		_, err := client.RestoreUpload(u.Username, u.Filename, u.Fingerprint)
		if err == nil {
			continue
		}
		s.mu.Lock()
		tr := s.transfers[u.ID]
		tr.State, tr.Error = "failed", err.Error()
		s.transfers[u.ID] = tr
		if err := s.persistUploadLocked(u.ID); err != nil {
			log.Printf("persist upload recovery: %v", err)
		}
		s.mu.Unlock()
	}
}
