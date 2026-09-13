package soulseek

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	PeerUserInfoRequest        uint32 = 15
	PeerUserInfoResponse       uint32 = 16
	MaxProfileDescriptionBytes        = 64 << 10
	MaxProfilePictureBytes            = 2 << 20
	MaxProfileFrameBytes              = MaxProfileDescriptionBytes + MaxProfilePictureBytes + 32
)

type UserInfoRequest struct{}

func (UserInfoRequest) command() uint32       { return PeerUserInfoRequest }
func (UserInfoRequest) encode(*Encoder) error { return nil }

// Description retains legacy wire bytes; decoding/sanitization is daemon-owned.
// UploadAllowed describes unsolicited receiving, not shared-file authorization.
// Legacy peers can omit it entirely, so absence is not interpreted as permission.
type PeerProfile struct {
	Description              string
	Picture                  []byte
	UploadSlots, QueueLength uint32
	SlotsAvailable           bool
	UploadAllowed            uint32
	UploadAllowedKnown       bool
}

func (PeerProfile) command() uint32 { return PeerUserInfoResponse }
func (m PeerProfile) encode(e *Encoder) error {
	if len(m.Description) > MaxProfileDescriptionBytes || len(m.Picture) > MaxProfilePictureBytes {
		return ErrTooLarge
	}
	if err := e.String(m.Description); err != nil {
		return err
	}
	e.Bool(len(m.Picture) > 0)
	if len(m.Picture) > 0 {
		if err := e.Bytes(m.Picture); err != nil {
			return err
		}
	}
	e.U32(m.UploadSlots)
	e.U32(m.QueueLength)
	e.Bool(m.SlotsAvailable)
	if m.UploadAllowedKnown {
		e.U32(m.UploadAllowed)
	}
	return nil
}
func DecodePeerProfile(payload []byte) (m PeerProfile, err error) {
	if len(payload) > MaxProfileFrameBytes {
		return m, ErrTooLarge
	}
	d := NewDecoder(payload)
	if m.Description, err = d.String(); err != nil {
		return m, err
	}
	if len(m.Description) > MaxProfileDescriptionBytes {
		return m, ErrTooLarge
	}
	hasPicture, err := d.Bool()
	if err != nil {
		return m, err
	}
	if hasPicture {
		if m.Picture, err = d.Bytes(); err != nil {
			return m, err
		}
		if len(m.Picture) > MaxProfilePictureBytes {
			return m, ErrTooLarge
		}
	}
	if m.UploadSlots, err = d.U32(); err != nil {
		return m, err
	}
	if m.QueueLength, err = d.U32(); err != nil {
		return m, err
	}
	if m.SlotsAvailable, err = d.Bool(); err != nil {
		return m, err
	}
	// Nicotine+ accepts old responses without uploadallowed and Museek+'s three
	// extra zero bytes from its incorrectly encoded uint32 slotsavail field.
	if d.Remaining() == 3 {
		if payload[len(payload)-3] != 0 || payload[len(payload)-2] != 0 || payload[len(payload)-1] != 0 {
			return m, ErrMalformed
		}
		return m, nil
	}
	if d.Remaining() > 0 {
		if m.UploadAllowed, err = d.U32(); err != nil {
			return m, err
		}
		m.UploadAllowedKnown = true
	}
	return m, d.Done()
}
func ValidateSelfDescription(description string) error {
	if !utf8.ValidString(description) || len(description) > MaxProfileDescriptionBytes || strings.IndexFunc(description, func(r rune) bool {
		return unicode.Is(unicode.Bidi_Control, r) || unicode.IsControl(r) && r != '\n' && r != '\t'
	}) >= 0 {
		return fmt.Errorf("%w: invalid profile description", ErrMalformed)
	}
	return nil
}
func (c *Client) SetSelfDescription(description string) error {
	if err := ValidateSelfDescription(description); err != nil {
		return err
	}
	c.mu.Lock()
	c.selfDescription = description
	c.mu.Unlock()
	return nil
}
func (c *Client) SelfProfile() PeerProfile {
	c.mu.Lock()
	description := c.selfDescription
	c.mu.Unlock()
	uploads := c.cfg.Uploads
	uploads.mu.Lock()
	defer uploads.mu.Unlock()
	return PeerProfile{Description: description, UploadSlots: uint32(min(uint64(max(0, uploads.max)), uint64(^uint32(0)))), QueueLength: uint32(min(uint64(len(uploads.q)), uint64(^uint32(0)))), SlotsAvailable: uploads.drainDone == nil && uploads.active < uploads.max, UploadAllowedKnown: true}
}
func requestPeerProfile(ctx context.Context, peer net.Conn) (PeerProfile, error) {
	stop := context.AfterFunc(ctx, func() { _ = peer.Close() })
	defer stop()
	if err := ctx.Err(); err != nil {
		return PeerProfile{}, err
	}
	if err := writeMessage(peer, UserInfoRequest{}); err != nil {
		if ctx.Err() != nil {
			return PeerProfile{}, ctx.Err()
		}
		return PeerProfile{}, err
	}
	code, payload, err := readFrame(peer, nil, MaxProfileFrameBytes)
	if ctx.Err() != nil {
		return PeerProfile{}, ctx.Err()
	}
	if err != nil {
		return PeerProfile{}, err
	}
	if code != PeerUserInfoResponse {
		return PeerProfile{}, fmt.Errorf("%w: expected peer profile", ErrMalformed)
	}
	return DecodePeerProfile(payload)
}
func (c *Client) ProfileUser(ctx context.Context, username string) (PeerProfile, error) {
	if err := ValidateUsername(username); err != nil {
		return PeerProfile{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return PeerProfile{}, err
	}
	if username == c.cfg.Username {
		return c.SelfProfile(), nil
	}
	peer, err := c.connectUser(ctx, username)
	if err != nil {
		return PeerProfile{}, err
	}
	defer peer.Close()
	return requestPeerProfile(ctx, peer)
}
