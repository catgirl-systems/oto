package soulseek

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	ServerPrivateMessage uint32 = 22
	ServerPrivateAck     uint32 = 23
	// MaxChatBytes is a local resource budget, not a claimed server limit.
	MaxChatBytes = 64 << 10
)

type PrivateMessageRequest struct{ Username, Text string }

func (PrivateMessageRequest) command() uint32 { return ServerPrivateMessage }
func (m PrivateMessageRequest) encode(e *Encoder) error {
	if err := ValidateChatText(m.Text); err != nil {
		return err
	}
	if err := encodeUsername(e, m.Username); err != nil {
		return err
	}
	return e.String(m.Text)
}

func ValidateChatText(text string) error {
	if len(text) > MaxChatBytes {
		return fmt.Errorf("%w: chat text exceeds %d bytes", ErrTooLarge, MaxChatBytes)
	}
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: chat text must be nonempty UTF-8", ErrMalformed)
	}
	return nil
}

type PrivateMessageAck struct{ ID uint32 }

func (PrivateMessageAck) command() uint32 { return ServerPrivateAck }
func (m PrivateMessageAck) encode(e *Encoder) error {
	e.U32(m.ID)
	return nil
}

// Text retains the original wire bytes for replay fingerprints. Legacy display
// decoding and terminal sanitization belong to the daemon, never to identities.
type PrivateMessage struct {
	ID, Timestamp  uint32
	Username, Text string
	New            bool
}

func (PrivateMessage) socialMessage() {}

func DecodePrivateMessage(payload []byte) (m PrivateMessage, err error) {
	if len(payload) > MaxUsernameBytes+MaxChatBytes+17 {
		return m, ErrTooLarge
	}
	d := NewDecoder(payload)
	if m.ID, err = d.U32(); err != nil {
		return m, err
	}
	if m.Timestamp, err = d.U32(); err != nil {
		return m, err
	}
	if m.Username, err = decodeUsername(d); err != nil {
		return m, err
	}
	if m.Text, err = d.String(); err != nil {
		return m, err
	}
	if len(m.Text) > MaxChatBytes {
		return m, ErrTooLarge
	}
	if m.New, err = d.Bool(); err != nil {
		return m, err
	}
	return m, d.Done()
}

// SendPrivateMessage reserves the writer before beforeWrite. The daemon commits
// its sending state there. attempted means a write was invoked: any subsequent
// error is ambiguous, even if the transport could not report a byte count.
// Success is a completed socket write, NOT recipient delivery or a read receipt.
func (c *Client) SendPrivateMessage(ctx context.Context, username, text string, beforeWrite func() error) (attempted bool, err error) {
	return c.sendTracked(ctx, PrivateMessageRequest{Username: username, Text: text}, beforeWrite)
}
