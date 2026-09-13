package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

type SharedSendRequest struct {
	CommunityIdentity
	RequestID string   `json:"request_id"`
	Username  string   `json:"username"`
	Files     []string `json:"files,omitempty"`
	Folder    string   `json:"folder,omitempty"`
}
type SharedSendFile struct {
	Filename string `json:"filename"`
	Size     uint64 `json:"size"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
	UploadID string `json:"upload_id,omitempty"`
}
type sharedSendFile struct {
	File        SharedSendFile
	Fingerprint string
}
type sharedSend struct {
	Identity                          CommunityIdentity
	RequestID, Token, Username, State string
	CreatedAt                         time.Time
	Files                             []sharedSendFile
	Bytes                             uint64
	Eligible                          int
	SizesKnown                        bool
}
type SharedSendPage struct {
	CommunityIdentity
	Captured   CommunityIdentity `json:"captured"`
	RequestID  string            `json:"request_id"`
	Token      string            `json:"token"`
	Username   string            `json:"username"`
	State      string            `json:"state"`
	CreatedAt  time.Time         `json:"created_at"`
	Total      int               `json:"total"`
	Eligible   int               `json:"eligible"`
	Bytes      uint64            `json:"bytes"`
	SizesKnown bool              `json:"sizes_known"`
	NextCursor int               `json:"next_cursor"`
	Files      []SharedSendFile  `json:"files"`
}

func sharedSendPage(id CommunityIdentity, b sharedSend, cursor int) (SharedSendPage, error) {
	if cursor < 0 || cursor > len(b.Files) {
		return SharedSendPage{}, errors.New("invalid shared-send cursor")
	}
	out := SharedSendPage{CommunityIdentity: id, Captured: b.Identity, RequestID: b.RequestID, Token: b.Token, Username: b.Username, State: b.State, CreatedAt: b.CreatedAt, Total: len(b.Files), Eligible: b.Eligible, Bytes: b.Bytes, SizesKnown: b.SizesKnown, Files: []SharedSendFile{}}
	if b.State == "preview" && id != b.Identity {
		out.State = "stale-preview"
	}
	size := 0
	for i := cursor; i < len(b.Files); i++ {
		row := b.Files[i].File
		data, _ := json.Marshal(row)
		if len(out.Files) == 200 || size+len(data) > 128<<10 {
			out.NextCursor = i
			break
		}
		out.Files = append(out.Files, row)
		size += len(data)
	}
	return out, nil
}
func loadSharedSend(ctx context.Context, q *db.Queries, account, requestID string) (sharedSend, error) {
	var b sharedSend
	row, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: account, RequestID: requestID})
	if err != nil {
		return b, err
	}
	if row.Kind != "shared-send" || len(row.Result) > 16<<20 {
		return b, errors.New("invalid shared-send receipt")
	}
	err = json.Unmarshal([]byte(row.Result), &b)
	return b, err
}
func (s *Service) PreviewSharedSend(ctx context.Context, req SharedSendRequest) (SharedSendPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := validateCommunityRequestID(req.RequestID); err != nil {
		return SharedSendPage{}, err
	}
	if err := soulseek.ValidateUsername(req.Username); err != nil {
		return SharedSendPage{}, err
	}
	if len(req.Files) > soulseek.MaxUploadSelectionFiles {
		return SharedSendPage{}, errors.New("too many selected files")
	}
	if len(req.Folder) > 16<<10 || !utf8.ValidString(req.Folder) {
		return SharedSendPage{}, errors.New("folder must be UTF-8 within 16 KiB")
	}
	size := len(req.Folder)
	for _, name := range req.Files {
		if len(name) > 16<<10 || !utf8.ValidString(name) {
			return SharedSendPage{}, errors.New("selected paths must be UTF-8 within 16 KiB each")
		}
		size += len(name)
	}
	if size > 2<<20 {
		return SharedSendPage{}, errors.New("selected paths exceed byte budget")
	}
	req.Files = slices.Clone(req.Files)
	for i, name := range req.Files {
		clean, err := soulseek.NormalizePath(name)
		if err != nil {
			return SharedSendPage{}, err
		}
		req.Files[i] = clean
	}
	sort.Strings(req.Files)
	req.Files = slices.Compact(req.Files)
	if req.Folder != "" {
		var err error
		req.Folder, err = soulseek.NormalizePath(req.Folder)
		if err != nil {
			return SharedSendPage{}, err
		}
	}
	data, _ := json.Marshal(struct {
		Username, Folder string
		Files            []string
	}{req.Username, req.Folder, req.Files})
	fingerprint := sha256.Sum256(data)
	s.mu.Lock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		s.mu.Unlock()
		return SharedSendPage{}, err
	}
	previous, err := s.stateDB.Queries().GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID})
	if err == nil {
		if previous.Kind != "shared-send" || !bytes.Equal(previous.Fingerprint, fingerprint[:]) {
			s.mu.Unlock()
			return SharedSendPage{}, errors.New("request ID already used for a different submission")
		}
		b, err := loadSharedSend(ctx, s.stateDB.Queries(), req.Account, req.RequestID)
		s.mu.Unlock()
		if err != nil {
			return SharedSendPage{}, err
		}
		return sharedSendPage(req.CommunityIdentity, b, 0)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.mu.Unlock()
		return SharedSendPage{}, err
	}
	client, index := s.client, s.shares
	if client == nil || index == nil || !s.community.online {
		s.mu.Unlock()
		return SharedSendPage{}, errors.New("connect before previewing a shared-file send")
	}
	if s.community.sharedPreview != nil {
		s.mu.Unlock()
		return SharedSendPage{}, errors.New("another shared-file preview is running")
	}
	s.community.sharedPreview = &req
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.community.sharedPreview == &req {
			s.community.sharedPreview = nil
		}
		s.mu.Unlock()
	}()
	names, err := index.SelectUploadFiles(ctx, req.Files, req.Folder)
	if err != nil {
		return SharedSendPage{}, err
	}
	b := sharedSend{Identity: req.CommunityIdentity, RequestID: req.RequestID, Token: rand.Text(), Username: req.Username, State: "preview", CreatedAt: time.Now().UTC(), SizesKnown: true}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return SharedSendPage{}, err
		}
		snapshot, err := client.PreviewUpload(req.Username, name)
		file := sharedSendFile{File: SharedSendFile{Filename: strings.ReplaceAll(name, "/", "\\"), Size: snapshot.Size, State: "preview"}, Fingerprint: snapshot.Fingerprint}
		if snapshot.Fingerprint == "" {
			b.SizesKnown = false
		}
		if err != nil {
			file.File.State = "denied"
			file.File.Error = "File unavailable or recipient permission denied; refresh shares/user permissions"
		} else {
			b.Eligible++
		}
		if math.MaxUint64-b.Bytes < snapshot.Size {
			return SharedSendPage{}, errors.New("selected byte total overflows")
		}
		b.Bytes += snapshot.Size
		b.Files = append(b.Files, file)
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		return SharedSendPage{}, err
	}
	if len(encoded) > 16<<20 {
		return SharedSendPage{}, errors.New("shared-send snapshot exceeds budget")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return SharedSendPage{}, err
	}
	if s.client != client || s.shares != index {
		return SharedSendPage{}, errors.New("shares or connection changed; preview again")
	}
	err = s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		n, err := q.InsertCommunitySubmission(ctx, db.InsertCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID, Kind: "shared-send", Fingerprint: fingerprint[:], Result: string(encoded), CreatedAt: b.CreatedAt.UnixMilli()})
		if err != nil {
			return err
		}
		if n == 0 {
			old, err := q.GetCommunitySubmission(ctx, db.GetCommunitySubmissionParams{Account: req.Account, RequestID: req.RequestID})
			if err != nil {
				return err
			}
			if old.Kind != "shared-send" || !bytes.Equal(old.Fingerprint, fingerprint[:]) {
				return errors.New("request ID already used")
			}
			b, err = loadSharedSend(ctx, q, req.Account, req.RequestID)
			return err
		}
		return nil
	})
	if err != nil {
		return SharedSendPage{}, err
	}
	return sharedSendPage(req.CommunityIdentity, b, 0)
}
func (s *Service) SharedSend(ctx context.Context, id CommunityIdentity, requestID string, cursor int) (SharedSendPage, error) {
	if err := validateCommunityRequestID(requestID); err != nil {
		return SharedSendPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, id); err != nil {
		return SharedSendPage{}, err
	}
	b, err := loadSharedSend(ctx, s.stateDB.Queries(), id.Account, requestID)
	if err != nil {
		return SharedSendPage{}, err
	}
	out, err := sharedSendPage(id, b, cursor)
	if err != nil {
		return out, err
	}
	if b.State == "running" && (id != b.Identity || s.community.sharedSend == nil || s.community.sharedSend.requestID != requestID) {
		out.State = "interrupted"
	}
	for i := range out.Files {
		row := &out.Files[i]
		if b.State == "running" || row.State == "preview" || row.State == "unknown" {
			if err := refreshSharedSendFile(ctx, s.stateDB.Queries(), b, b.Files[cursor+i], row); err != nil {
				return SharedSendPage{}, err
			}
		}
		if row.UploadID != "" {
			if tr, ok := s.transfers[row.UploadID]; ok && tr.Username == b.Username && tr.Filename == row.Filename {
				row.State = tr.State
				row.Error = tr.Error
			}
		}
		if row.State == "preview" && out.State == "interrupted" {
			row.State = "not-submitted"
		}
	}
	return out, nil
}
