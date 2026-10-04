package soulseek

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectConnBlamesRouteOnlyBeforePeerSpeaks(t *testing.T) {
	client, server := net.Pipe()
	direct := &directConn{Conn: client, addr: "203.0.113.7:50300"}
	go func() { _ = server.Close() }()
	_, err := direct.Read(make([]byte, 1))
	addr, ok := unconfirmedDirectAddr(err)
	failIfFmt(t, !ok || addr != "203.0.113.7:50300" || !errors.Is(err, io.EOF) || !errors.Is(err, errUnconfirmedDirect), "silent remote close was not blamed on the route: %v", err)

	client, server = net.Pipe()
	direct = &directConn{Conn: client, addr: "203.0.113.7:50300"}
	go func() {
		_, _ = server.Write([]byte{1})
		_ = server.Close()
	}()
	_, _ = direct.Read(make([]byte, 1))
	_, err = direct.Read(make([]byte, 1))
	_, ok = unconfirmedDirectAddr(err)
	failIfFmt(t, ok || !errors.Is(err, io.EOF), "a peer that spoke was blamed on the route: %v", err)

	client, server = net.Pipe()
	defer server.Close()
	direct = &directConn{Conn: client, addr: "203.0.113.7:50300"}
	_ = direct.Close()
	_, err = direct.Read(make([]byte, 1))
	_, ok = unconfirmedDirectAddr(err)
	failIfFmt(t, ok, "our own close was blamed on the route: %v", err)
}

// strangerListener stands in for another VPN user who owns the peer's
// advertised port: it accepts, reads the handshake and first request, then
// resets without a word.
func strangerListener(t *testing.T) (PeerAddress, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if _, _, err := ReadInitFrame(conn); err == nil {
					_, _, _ = ReadFrame(conn)
				}
				_ = conn.(*net.TCPConn).SetLinger(0)
				_ = conn.Close()
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return PeerAddress{Username: "peer", IP: "127.0.0.1", Port: uint32(ln.Addr().(*net.TCPAddr).Port)}, &accepted
}

// brokerIndirect plays the server: each ConnectToPeer request is answered,
// after delay, by the real peer connecting back with the pierce token.
func brokerIndirect(t *testing.T, c *Client, real PeerAddress, delay time.Duration) {
	t.Helper()
	left, right := net.Pipe()
	c.mu.Lock()
	c.conn = left
	c.mu.Unlock()
	t.Cleanup(func() { _ = right.Close() })
	go func() {
		for {
			code, payload, err := ReadFrame(right)
			if err != nil {
				return
			}
			if code != ServerConnectToPeer {
				continue
			}
			d := NewDecoder(payload)
			token, _, kind := d.U32(), d.String(), d.String()
			go func() {
				time.Sleep(delay)
				c.mu.Lock()
				pierced := c.pierce[token]
				c.mu.Unlock()
				if pierced == nil {
					return // that attempt already chose another route
				}
				conn, err := net.Dial("tcp", net.JoinHostPort(real.IP, itoa(real.Port)))
				if err != nil {
					return
				}
				// The loopback peer expects the init frame its acceptor would read.
				if err := encodePeerHandshake(conn, PeerInitMessage{Username: "uploader", Type: kind}); err != nil {
					_ = conn.Close()
					return
				}
				select {
				case pierced <- conn:
				default:
					_ = conn.Close()
				}
			}()
		}
	}()
}

func itoa(n uint32) string {
	var b [10]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}

func TestUploadRetriesIndirectWhenDirectRouteReachesStranger(t *testing.T) {
	real, _, data := uploadPeer(t, "", 0)
	stranger, accepted := strangerListener(t)
	contents := []byte("the real peer gets the file")
	c, events, _ := uploadClient(t, stranger, contents)
	// The direct dial wins the first race, as it does in the wild.
	brokerIndirect(t, c, real, 300*time.Millisecond)
	_, err := c.QueueUpload("peer", `Music\song`)
	must(t, err)
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for done := false; !done; {
		select {
		case e := <-events:
			failIfFmt(t, e.State == "failed", "upload failed instead of retrying indirectly: %s", e.Error)
			done = e.State == "completed"
		case <-timer.C:
			t.Fatal("upload never completed")
		}
	}
	select {
	case got := <-data:
		failIfFmt(t, !bytes.Equal(got, contents), "real peer received %q", got)
	case <-time.After(3 * time.Second):
		t.Fatal("real peer received nothing")
	}
	failIfFmt(t, accepted.Load() != 1, "stranger dialled %d times; the disproven route must be avoided", accepted.Load())
	failIf(t, !c.directAvoided(net.JoinHostPort(stranger.IP, itoa(stranger.Port))), "disproven direct route was not remembered")
}

func TestAvoidedDirectRouteIsNotDialled(t *testing.T) {
	real, _, _ := uploadPeer(t, "", 0)
	stranger, accepted := strangerListener(t)
	c, _, _ := uploadClient(t, stranger, []byte("x"))
	brokerIndirect(t, c, real, 0)
	c.avoidDirect(net.JoinHostPort(stranger.IP, itoa(stranger.Port)))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := c.connectUserType(ctx, "peer", "P")
	must(t, err)
	_ = conn.Close()
	time.Sleep(50 * time.Millisecond)
	failIfFmt(t, accepted.Load() != 0, "avoided direct route was dialled %d times", accepted.Load())
}

func TestFailedIndirectRetryRemembersNothing(t *testing.T) {
	stranger, accepted := strangerListener(t)
	c, events, _ := uploadClient(t, stranger, []byte("x"))
	// No server connection: the indirect retry cannot succeed, as for a user
	// whose only working route is the direct one.
	_, err := c.QueueUpload("peer", `Music\song`)
	must(t, err)
	failed := uploadEvent(t, events, "failed")
	failIfFmt(t, !strings.Contains(failed.Error, "connection reset") || !strings.Contains(failed.Error, "indirect retry"), "failure hid a cause: %q", failed.Error)
	failIf(t, c.directAvoided(net.JoinHostPort(stranger.IP, itoa(stranger.Port))), "an unproven route was remembered as bad")
	failIfFmt(t, accepted.Load() != 1, "direct route dialled %d times", accepted.Load())
}
