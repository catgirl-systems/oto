package soulseek

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"time"
)

// A direct route dials the address the server reports for a peer. Behind a
// shared VPN exit that address:port can belong to someone else: the dial and
// handshake succeed, the stranger never speaks, then resets. Racing routes
// picks that socket as the winner and cancels the indirect route that would
// have reached the real peer.
//
// Direct sockets therefore tag remote-originated failures that arrive before
// the peer sent a single byte. Callers that were awaiting a reply (transfer
// setup) retry once through the indirect route only. A real peer can also
// hang up silently, so the address is remembered as disproven only when that
// retry succeeds; otherwise a user without port forwarding, for whom only the
// direct route works, would lose the peer. Silence alone proves nothing: a
// peer receiving search results never answers.

var errUnconfirmedDirect = errors.New("soulseek: direct route never reached the peer")

// directAvoidTTL bounds how long a disproven direct address is skipped; VPN
// port assignments change, so the route may become valid again.
const directAvoidTTL = 10 * time.Minute

type unconfirmedDirectError struct {
	addr string
	err  error
}

func (e *unconfirmedDirectError) Error() string   { return e.err.Error() }
func (e *unconfirmedDirectError) Unwrap() []error { return []error{e.err, errUnconfirmedDirect} }

// unconfirmedDirectAddr reports the direct address blamed by err, if any.
func unconfirmedDirectAddr(err error) (string, bool) {
	var direct *unconfirmedDirectError
	if errors.As(err, &direct) {
		return direct.addr, true
	}
	return "", false
}

// directConn wraps the raw socket of a direct route, beneath any diagnostic
// wrapper, and records whether the peer has sent anything yet.
type directConn struct {
	net.Conn
	addr          string
	heard, closed atomic.Bool
}

func (d *directConn) Close() error {
	d.closed.Store(true)
	return d.Conn.Close()
}

// blame tags err as the route's fault when it came from the remote side
// before the peer sent anything.
func (d *directConn) blame(err error) error {
	// Our own close or deadline says nothing about who answered the dial.
	if err == nil || d.heard.Load() || d.closed.Load() || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) {
		return err
	}
	return &unconfirmedDirectError{addr: d.addr, err: err}
}

func (d *directConn) Read(b []byte) (int, error) {
	n, err := d.Conn.Read(b)
	if n > 0 {
		d.heard.Store(true)
	}
	return n, d.blame(err)
}

// Write tags the same way: a reset can surface on either side of the socket.
func (d *directConn) Write(b []byte) (int, error) {
	n, err := d.Conn.Write(b)
	return n, d.blame(err)
}

func (d *directConn) SyscallConn() (syscall.RawConn, error) {
	if raw, ok := d.Conn.(interface {
		SyscallConn() (syscall.RawConn, error)
	}); ok {
		return raw.SyscallConn()
	}
	return nil, errors.New("socket does not expose a raw connection")
}

type indirectOnlyKey struct{}

// withIndirectOnly makes connections opened under ctx skip the direct route to
// addr, for one retry, without remembering anything.
func withIndirectOnly(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, indirectOnlyKey{}, addr)
}

// skipDirect reports whether the direct route to addr must not be dialled.
func (c *Client) skipDirect(ctx context.Context, addr string) bool {
	if only, ok := ctx.Value(indirectOnlyKey{}).(string); ok && only == addr {
		return true
	}
	return c.directAvoided(addr)
}

// retryIndirect runs attempt again through the indirect route when first
// failed because the direct route reached a stranger. The address is avoided
// only after the retry succeeds; a failed retry reports both causes.
func (c *Client) retryIndirect(ctx context.Context, first error, event string, attempt func(context.Context) error) error {
	addr, ok := unconfirmedDirectAddr(first)
	if !ok || ctx.Err() != nil {
		return first
	}
	c.log(ctx, slog.LevelInfo, event, first, slog.String("remote_endpoint", addr))
	if err := attempt(withIndirectOnly(ctx, addr)); err != nil {
		return fmt.Errorf("%w; indirect retry: %w", first, err)
	}
	c.avoidDirect(addr)
	return nil
}

// avoidDirect skips the direct route to addr until directAvoidTTL passes.
func (c *Client) avoidDirect(addr string) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.directAvoid == nil {
		c.directAvoid = make(map[string]time.Time)
	}
	for known, until := range c.directAvoid {
		if now.After(until) {
			delete(c.directAvoid, known)
		}
	}
	c.directAvoid[addr] = now.Add(directAvoidTTL)
}

func (c *Client) directAvoided(addr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.directAvoid[addr]
	return ok && time.Now().Before(until)
}
