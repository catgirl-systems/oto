package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

type CommunityRoomInvitationsRequest struct {
	CommunityIdentity
	Revision uint64 `json:"revision"`
	Enabled  bool   `json:"enabled"`
}
type CommunityRoomWallRequest struct {
	CommunityIdentity
	Room     string `json:"room"`
	Text     string `json:"text"`
	Revision uint64 `json:"revision"`
	Confirm  bool   `json:"confirm"`
}
type CommunityRoomWallEntry struct {
	Username string `json:"username"`
	Text     string `json:"text"`
}
type CommunityRoomWallPage struct {
	CommunityIdentity
	Room       CommunityRoom            `json:"room"`
	Entries    []CommunityRoomWallEntry `json:"entries"`
	OwnText    string                   `json:"own_text"`
	Fresh      bool                     `json:"fresh"`
	State      string                   `json:"state"`
	NextCursor string                   `json:"next_cursor"`
	Revision   uint64                   `json:"revision"`
}

func (s *Service) SetCommunityRoomInvitations(ctx context.Context, req CommunityRoomInvitationsRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return err
	}
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		account, err := q.GetCommunityAccount(ctx, req.Account)
		if err != nil {
			return err
		}
		if req.Revision != s.community.revision+uint64(account.Revision) {
			return ErrCommunityMessageState
		}
		_, err = q.EditCommunityAccount(ctx, db.EditCommunityAccountParams{Account: req.Account, Revision: account.Revision, Description: account.Description, AcceptInvitations: boolInt(req.Enabled), RetentionDays: account.RetentionDays, PublicFeedLogging: account.PublicFeedLogging})
		return err
	})
	if err == nil {
		s.community.invitationsWanted = req.Enabled
		s.community.invitationsWritten, s.community.invitationsConfirmed = nil, nil
		s.community.revision++
		s.wakeCommunityOutboxLocked()
	}
	return err
}
func (s *Service) SetCommunityRoomWall(ctx context.Context, req CommunityRoomWallRequest) error {
	if _, err := soulseek.EncodeMessage(soulseek.RoomWallRequest{Room: req.Room, Text: req.Text}); err != nil {
		return err
	}
	if communityDisplayText(req.Text) != req.Text {
		return errors.New("community: wall text contains terminal controls")
	}
	if req.Text == "" && !req.Confirm {
		return errors.New("community: clearing your room wall requires confirmation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return err
	}
	r := s.community.rooms[req.Room]
	if r == nil {
		return errors.New("community: open or join the room first")
	}
	err := s.stateDB.WriteTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		account, err := q.GetCommunityAccount(ctx, req.Account)
		if err != nil {
			return err
		}
		if req.Revision != s.community.revision+uint64(account.Revision) {
			return ErrCommunityMessageState
		}
		if err = q.PutCommunityRoom(ctx, db.PutCommunityRoomParams{Account: req.Account, Room: req.Room, Autojoin: boolInt(r.autojoin), PrivateRoom: boolInt(r.private), OwnWall: req.Text}); err != nil {
			return err
		}
		_, err = q.BumpCommunityRevision(ctx, req.Account)
		return err
	})
	if err == nil {
		r.ownWall = req.Text
		r.wallIntent++
		r.wallState = "saved; awaiting confirmed join"
		s.community.revision++
		s.wakeCommunityOutboxLocked()
	}
	return err
}
func (s *Service) CommunityRoomWall(ctx context.Context, req CommunityRoomMembersRequest) (CommunityRoomWallPage, error) {
	out := CommunityRoomWallPage{CommunityIdentity: req.CommunityIdentity, Entries: []CommunityRoomWallEntry{}}
	if err := soulseek.ValidateRoomName(req.Room); err != nil {
		return out, err
	}
	limit, err := communityPageLimit(req.Limit)
	if err != nil || validateCommunityRoomPage(req.Cursor, req.Query) != nil || !utf8.ValidString(req.Query) {
		return out, errors.New("community: invalid wall page")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkCommunityIdentityLocked(ctx, req.CommunityIdentity); err != nil {
		return out, err
	}
	account, err := s.stateDB.Queries().GetCommunityAccount(ctx, req.Account)
	if err != nil {
		return out, err
	}
	out.Room, out.Revision = s.communityRoomLocked(req.Room), s.community.revision+uint64(account.Revision)
	r := s.community.rooms[req.Room]
	if r == nil {
		return out, nil
	}
	out.OwnText, out.Fresh, out.State = r.ownWall, r.joined && s.community.online && r.wallFresh, r.wallState
	var names []string
	for name, text := range r.wall {
		if name > req.Cursor && strings.Contains(strings.ToLower(name+" "+text), strings.ToLower(req.Query)) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	// The standard row budget leaves room for the separately bounded own text.
	budget := 6 * 1024
	for _, name := range names {
		entry := CommunityRoomWallEntry{Username: name, Text: r.wall[name]}
		encoded, _ := json.Marshal(entry)
		if len(out.Entries) == int(limit) || budget+len(encoded)+1 > communityPageBytes {
			if len(out.Entries) == 0 {
				return out, errors.New("community: wall entry exceeds page budget")
			}
			out.NextCursor = out.Entries[len(out.Entries)-1].Username
			break
		}
		budget += len(encoded) + 1
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}
