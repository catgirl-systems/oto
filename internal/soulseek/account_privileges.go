package soulseek

import (
	"context"
	"fmt"
)

const (
	ServerCheckPrivileges uint32 = 92
	ServerGivePrivileges  uint32 = 123
)

type CheckPrivilegesRequest struct{}

func (CheckPrivilegesRequest) command() uint32       { return ServerCheckPrivileges }
func (CheckPrivilegesRequest) encode(*Encoder) error { return nil }

type PrivilegeBalance struct{ Seconds uint32 }

func (PrivilegeBalance) socialMessage() {}
func DecodePrivilegeBalance(payload []byte) (m PrivilegeBalance, err error) {
	d := NewDecoder(payload)
	if m.Seconds, err = d.U32(); err != nil {
		return m, err
	}
	return m, d.Done()
}

type GivePrivilegesRequest struct {
	Username string
	Days     uint32
}

func (GivePrivilegesRequest) command() uint32 { return ServerGivePrivileges }
func (m GivePrivilegesRequest) encode(e *Encoder) error {
	if m.Days == 0 {
		return fmt.Errorf("%w: gift must contain positive whole days", ErrMalformed)
	}
	if err := encodeUsername(e, m.Username); err != nil {
		return err
	}
	e.U32(m.Days)
	return nil
}

func (c *Client) RequestPrivilegeBalance(ctx context.Context) error {
	return c.sendContext(ctx, CheckPrivilegesRequest{})
}

// The protocol has no gift acknowledgement. A complete socket write is not proof
// that privileges were transferred; after an attempted write, never retry implicitly.
func (c *Client) GivePrivileges(ctx context.Context, username string, days uint32, beforeWrite func() error) (bool, error) {
	return c.sendTracked(ctx, GivePrivilegesRequest{Username: username, Days: days}, beforeWrite)
}
