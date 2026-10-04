package daemon

import (
	"encoding/binary"
	"hash/maphash"
	"strconv"
	"time"
)

// TransferChanges answers a poll for transfers. When nothing visible changed
// since the client's fingerprint and every transfer is static, Unchanged is
// set and Transfers is omitted, sparing the list build, sort and encode that
// pollers otherwise repeat every second.
type TransferChanges struct {
	Fingerprint string     `json:"fingerprint"`
	Unchanged   bool       `json:"unchanged,omitempty"`
	Transfers   []Transfer `json:"transfers,omitempty"`
}

var transferSeed = maphash.MakeSeed()

// staticTransfer reports whether a transfer's fields cannot change on their
// own; active, queued and retrying transfers carry clocks and rates.
func staticTransfer(state string) bool {
	switch state {
	case "completed", "failed", "cancelled", "filtered", "paused":
		return true
	}
	return false
}

// transferFingerprintLocked hashes every visible field of every transfer,
// independently of map order, and reports whether all of them are static.
func (s *Service) transferFingerprintLocked() (string, bool) {
	var sum uint64
	static := true
	var h maphash.Hash
	var buf [8]byte
	for id, x := range s.transfers {
		static = static && staticTransfer(x.State)
		h.SetSeed(transferSeed)
		for _, field := range []string{id, x.Username, x.Filename, x.Direction, x.State, x.Error} {
			h.WriteString(field)
			h.WriteByte(0)
		}
		for _, n := range []uint64{x.Done, x.Total, uint64(x.Queue)} {
			binary.LittleEndian.PutUint64(buf[:], n)
			h.Write(buf[:])
		}
		sum += h.Sum64()
	}
	return strconv.FormatUint(sum, 16) + "-" + strconv.Itoa(len(s.transfers)), static
}

// TransfersSince returns the transfer list, or Unchanged when since still
// matches and nothing is in motion.
func (s *Service) TransfersSince(since string) TransferChanges {
	now := time.Now()
	s.mu.RLock()
	fingerprint, static := s.transferFingerprintLocked()
	if since != "" && since == fingerprint && static {
		s.mu.RUnlock()
		return TransferChanges{Fingerprint: fingerprint, Unchanged: true}
	}
	client := s.client
	transfers := s.transferValuesLocked(now)
	s.mu.RUnlock()
	addDownloadWaits(transfers, client, now)
	return TransferChanges{Fingerprint: fingerprint, Transfers: transfers}
}
