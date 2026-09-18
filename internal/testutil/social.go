// Package testutil contains loopback-only acceptance-test plumbing. It is not
// linked into oto. Scenarios supply the protocol script; this is not a server.
package testutil

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

//go:embed testdata/social.json
var socialFixtures []byte

const NicotineRevision = "0e2a467269255572906f8360e89f70ddc40f7076"

type WireFixture struct {
	Name      string         `json:"name"`
	Class     string         `json:"class"`
	Direction string         `json:"direction"`
	Code      uint32         `json:"code"`
	Hex       string         `json:"payload_hex"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Expected  map[string]any `json:"expected,omitempty"`
}

func SocialFixtures(t testing.TB) []WireFixture {
	t.Helper()
	var corpus struct {
		Revision string        `json:"nicotine_revision"`
		Fixtures []WireFixture `json:"fixtures"`
	}
	if err := json.Unmarshal(socialFixtures, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Revision != NicotineRevision || len(corpus.Fixtures) == 0 {
		t.Fatal("missing or unpinned social wire fixtures")
	}
	return corpus.Fixtures
}

// SocialPayloadMap indexes the frozen wire fixtures as raw payloads, failing the
// test when any of the required fixtures is missing or renamed.
func SocialPayloadMap(t testing.TB, required ...string) map[string][]byte {
	t.Helper()
	payloads := make(map[string][]byte)
	for name, f := range SocialFixtureMap(t, required...) {
		payloads[name] = f.Payload(t)
	}
	return payloads
}

// SocialFixture returns the named frozen wire fixture, failing the test when it
// is missing or renamed.
func SocialFixture(t testing.TB, name string) WireFixture {
	t.Helper()
	for _, f := range SocialFixtures(t) {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("missing social wire fixture %q", name)
	return WireFixture{}
}

// SocialFixtureMap indexes the frozen wire fixtures by name and fails the test
// when any of the required fixtures is missing or renamed.
func SocialFixtureMap(t testing.TB, required ...string) map[string]WireFixture {
	t.Helper()
	fixtures := make(map[string]WireFixture)
	for _, f := range SocialFixtures(t) {
		fixtures[f.Name] = f
	}
	for _, name := range required {
		if _, ok := fixtures[name]; !ok {
			t.Fatalf("missing social wire fixture %q", name)
		}
	}
	return fixtures
}

func (f WireFixture) Payload(t testing.TB) []byte {
	t.Helper()
	b, err := hex.DecodeString(f.Hex)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ScriptSocket owns all accepted connections until cleanup, including connections
// whose scripts failed. Closing listeners/connections interrupts blocked I/O.
type ScriptSocket struct {
	Listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	conns    []net.Conn
	wg       sync.WaitGroup
}

func ListenScript(t testing.TB, script func(context.Context, net.Conn) error) *ScriptSocket {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &ScriptSocket{Listener: ln, ctx: ctx, cancel: cancel}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				if ctx.Err() == nil {
					t.Errorf("script accept: %v", err)
				}
				return
			}
			s.mu.Lock()
			if ctx.Err() != nil {
				s.mu.Unlock()
				_ = c.Close()
				return
			}
			s.conns = append(s.conns, c)
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(time.Minute))
				if err := script(ctx, c); err != nil && ctx.Err() == nil {
					t.Errorf("protocol scenario: %v", err)
				}
			}()
		}
	}()
	t.Cleanup(s.Close)
	return s
}

func (s *ScriptSocket) Close() {
	s.cancel()
	_ = s.Listener.Close()
	s.mu.Lock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// ReadPacket and WritePacket deliberately do not use oto's codec. Fixtures and
// scripted peers must catch production framing regressions, not repeat them.
func ReadPacket(r io.Reader) (uint32, []byte, error) {
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(header[:4])
	if n < 4 || n > 1<<20 {
		return 0, nil, fmt.Errorf("invalid scenario frame length %d", n)
	}
	b := make([]byte, n-4)
	_, err := io.ReadFull(r, b)
	return binary.LittleEndian.Uint32(header[4:]), b, err
}

func WritePacket(w io.Writer, code uint32, payload []byte) error {
	b := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(b, uint32(4+len(payload)))
	binary.LittleEndian.PutUint32(b[4:], code)
	copy(b[8:], payload)
	_, err := io.Copy(w, bytes.NewReader(b))
	return err
}

// WaitForPacket ignores unrelated initialization traffic, not messages inside a
// scenario. Tests requiring strict ordering call ReadPacket directly instead.
func WaitForPacket(r io.Reader, want uint32) ([]byte, error) {
	for {
		code, b, err := ReadPacket(r)
		if err != nil {
			return nil, err
		}
		if code == want {
			return b, nil
		}
	}
}
