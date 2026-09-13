package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

const MaxCommunityBroadcastRecipients = 10000

type CommunityBroadcastRequest struct {
	CommunityIdentity
	RequestID string   `json:"request_id"`
	Audience  string   `json:"audience"` // buddies or uploaders
	Text      string   `json:"text"`
	Offline   []string `json:"offline,omitempty"` // Explicit additional buddy usernames.
}
type CommunityBroadcastRecipient struct {
	Username  string `json:"username"`
	State     string `json:"state"`
	MessageID int64  `json:"message_id,omitempty"`
	Error     string `json:"error,omitempty"`
}
type communityBroadcast struct {
	Identity   CommunityIdentity             `json:"identity"`
	RequestID  string                        `json:"request_id"`
	Token      string                        `json:"token"`
	Audience   string                        `json:"audience"`
	Text       string                        `json:"text"`
	State      string                        `json:"state"`
	CreatedAt  time.Time                     `json:"created_at"`
	Recipients []CommunityBroadcastRecipient `json:"recipients"`
}
type CommunityBroadcastPage struct {
	CommunityIdentity
	Captured   CommunityIdentity             `json:"captured"`
	RequestID  string                        `json:"request_id"`
	Token      string                        `json:"token"`
	Audience   string                        `json:"audience"`
	Text       string                        `json:"text"`
	State      string                        `json:"state"`
	CreatedAt  time.Time                     `json:"created_at"`
	Total      int                           `json:"total"`
	NextCursor int                           `json:"next_cursor"`
	Recipients []CommunityBroadcastRecipient `json:"recipients"`
}

func broadcastPage(id CommunityIdentity, b communityBroadcast, cursor int) (CommunityBroadcastPage, error) {
	if cursor < 0 || cursor > len(b.Recipients) {
		return CommunityBroadcastPage{}, errors.New("invalid broadcast cursor")
	}
	out := CommunityBroadcastPage{CommunityIdentity: id, Captured: b.Identity, RequestID: b.RequestID, Token: b.Token, Audience: b.Audience, Text: b.Text, State: b.State, CreatedAt: b.CreatedAt, Total: len(b.Recipients), Recipients: []CommunityBroadcastRecipient{}}
	if b.State == "preview" && b.Identity != id {
		out.State = "stale-preview"
	}
	bytes := 0
	for i := cursor; i < len(b.Recipients); i++ {
		row := b.Recipients[i]
		encoded, _ := json.Marshal(row)
		if len(out.Recipients) == 200 || bytes+len(encoded) > 64<<10 {
			out.NextCursor = i
			break
		}
		out.Recipients = append(out.Recipients, row)
		bytes += len(encoded)
	}
	return out, nil
}
func loadBroadcast(ctx context.Context, q *db.Queries, account, requestID string) (communityBroadcast, error) {
	var out communityBroadcast
	row, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: account, RequestID: requestID})
	if err != nil {
		return out, err
	}
	if row.Kind != "broadcast" {
		return out, errors.New("request ID belongs to another operation")
	}
	if len(row.Result) > 8<<20 {
		return out, errors.New("stored broadcast exceeds budget")
	}
	err = json.Unmarshal([]byte(row.Result), &out)
	return out, err
}
func (s *Service) PreviewCommunityBroadcast(ctx context.Context, req CommunityBroadcastRequest) (CommunityBroadcastPage, error) {
	if err := validateCommunityRequestID(req.RequestID); err != nil {
		return CommunityBroadcastPage{}, err
	}
	if req.Audience != "buddies" && req.Audience != "uploaders" || req.Audience == "uploaders" && len(req.Offline) > 0 || len(req.Offline) > MaxCommunityBroadcastRecipients {
		return CommunityBroadcastPage{}, errors.New("choose buddies or uploaders; offline additions must be explicit buddies")
	}
	text, err := communityOutgoingText(req.Text)
	if err != nil {
		return CommunityBroadcastPage{}, err
	}
	req.Offline = slices.Clone(req.Offline)
	sort.Strings(req.Offline)
	req.Offline = slices.Compact(req.Offline)
	for _, name := range req.Offline {
		if err := soulseek.ValidateUsername(name); err != nil {
			return CommunityBroadcastPage{}, err
		}
	}
	fingerprintData, _ := json.Marshal(struct {
		Audience, Text string
		Offline        []string
	}{req.Audience, text, req.Offline})
	fingerprint := sha256.Sum256(fingerprintData)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return CommunityBroadcastPage{}, err
	}
	q := s.stateDB.Queries()
	previous, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID})
	if err == nil {
		if previous.Kind != "broadcast" || !bytes.Equal(previous.Fingerprint, fingerprint[:]) {
			return CommunityBroadcastPage{}, errors.New("request ID was already used for a different submission")
		}
		stored, err := loadBroadcast(ctx, q, req.Account, req.RequestID)
		if err != nil {
			return CommunityBroadcastPage{}, err
		}
		return broadcastPage(req.CommunityIdentity, stored, 0)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CommunityBroadcastPage{}, err
	}
	text, err = s.community.text.outgoing(text)
	if err != nil {
		return CommunityBroadcastPage{}, err
	}
	names := map[string]bool{}
	if req.Audience == "buddies" {
		if s.community.online {
			for name := range s.community.buddies {
				u := s.community.users[name]
				if u.StatusFresh && (u.Status == soulseek.UserStatusOnline || u.Status == soulseek.UserStatusAway) {
					names[name] = true
				}
			}
		}
		for _, name := range req.Offline {
			if _, ok := s.community.buddies[name]; !ok {
				return CommunityBroadcastPage{}, errors.New("offline addition is not a current buddy")
			}
			names[name] = true
		}
	} else if s.community.online {
		for id, t := range s.transfers {
			owner, ok := s.uploadOwners[id]
			if ok && owner.session == s.uploadEpoch && t.Direction == "upload" && t.State == "running" {
				names[t.Username] = true
			}
		}
	}
	delete(names, s.cfg.Soulseek.Username)
	if len(names) == 0 {
		return CommunityBroadcastPage{}, errors.New("audience is empty; no messages sent")
	}
	// ponytail: bound each immutable audience to 10k users/1 MiB of names;
	// partition explicitly if larger broadcast audiences are ever required.
	if len(names) > MaxCommunityBroadcastRecipients {
		return CommunityBroadcastPage{}, errors.New("broadcast audience exceeds 10000 recipients; no audience was truncated")
	}
	ordered := make([]string, 0, len(names))
	size := 0
	for name := range names {
		size += len(name)
		if size > 1<<20 {
			return CommunityBroadcastPage{}, errors.New("broadcast audience exceeds byte budget; no messages sent")
		}
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	b := communityBroadcast{Identity: req.CommunityIdentity, RequestID: req.RequestID, Token: rand.Text(), Audience: req.Audience, Text: text, State: "preview", CreatedAt: time.Now().UTC()}
	for _, name := range ordered {
		b.Recipients = append(b.Recipients, CommunityBroadcastRecipient{Username: name, State: "preview"})
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		return CommunityBroadcastPage{}, err
	}
	if len(encoded) > 8<<20 {
		return CommunityBroadcastPage{}, errors.New("broadcast snapshot exceeds budget")
	}
	err = s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := db.New(tx).InsertCommunitySubmission(ctx, db.InsertCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID, Kind: "broadcast", Fingerprint: fingerprint[:], Result: string(encoded), CreatedAt: b.CreatedAt.UnixMilli()})
		return err
	})
	if err != nil {
		return CommunityBroadcastPage{}, err
	}
	return broadcastPage(req.CommunityIdentity, b, 0)
}
func (s *Service) CommunityBroadcast(ctx context.Context, id CommunityIdentity, requestID string, cursor int) (CommunityBroadcastPage, error) {
	if err := validateCommunityRequestID(requestID); err != nil {
		return CommunityBroadcastPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, id); err != nil {
		return CommunityBroadcastPage{}, err
	}
	b, err := loadBroadcast(ctx, s.stateDB.Queries(), id.Account, requestID)
	if err != nil {
		return CommunityBroadcastPage{}, err
	}
	out, err := broadcastPage(id, b, cursor)
	if err != nil {
		return out, err
	}
	if b.State == "running" && (b.Identity != id || s.community.broadcast == nil || s.community.broadcast.requestID != requestID) {
		out.State = "interrupted"
	}
	for i := range out.Recipients {
		if err := refreshBroadcastRecipient(ctx, s.stateDB.Queries(), b, &out.Recipients[i]); err != nil {
			return CommunityBroadcastPage{}, err
		}
	}
	return out, nil
}
