package testutil

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestSocialFixtureFraming(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range SocialFixtures(t) {
		if f.Name == "" || seen[f.Name] || f.Code == 0 {
			t.Fatalf("invalid fixture identity: %s", f.Name)
		}
		seen[f.Name] = true
		t.Run(f.Name, func(t *testing.T) {
			payload := f.Payload(t)
			var wire bytes.Buffer
			if err := WritePacket(&wire, f.Code, payload); err != nil {
				t.Fatal(err)
			}
			code, got, err := ReadPacket(&wire)
			if err != nil || code != f.Code || !bytes.Equal(got, payload) || wire.Len() != 0 {
				t.Fatalf("fixture frame: code=%d remaining=%d err=%v", code, wire.Len(), err)
			}
		})
	}
}

func TestScriptSocketCleanup(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	s := ListenScript(t, func(_ context.Context, c net.Conn) error {
		close(entered)
		defer close(exited)
		_, err := io.Copy(io.Discard, c)
		return err
	})
	conn, err := net.Dial("tcp", s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("script did not accept connection")
	}
	s.Close()
	select {
	case <-exited:
	default:
		t.Fatal("script survived cleanup")
	}
}
