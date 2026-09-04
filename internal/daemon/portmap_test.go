package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

type mappingServerObservation struct {
	port uint16
	err  error
}

func startMappingServer(t *testing.T, closeAfterLogin bool, events chan<- string) (string, <-chan mappingServerObservation) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	observations := make(chan mappingServerObservation, 8)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			command, _, err := soulseek.ReadFrame(conn)
			if err == nil && command != soulseek.ServerLogin {
				err = fmt.Errorf("first command=%d", command)
			}
			if err == nil && events != nil {
				events <- "login"
			}
			if err == nil {
				var frame []byte
				frame, err = soulseek.EncodeMessage(soulseek.LoginResponse{Success: true, Message: "ok"})
				if err == nil {
					_, err = conn.Write(frame)
				}
			}
			var port uint16
			for i := 0; err == nil && i < 5; i++ {
				var payload []byte
				command, payload, err = soulseek.ReadFrame(conn)
				if i == 0 && err == nil {
					if command != soulseek.ServerSetListenPort || len(payload) != 4 {
						err = fmt.Errorf("listen command=%d payload=%x", command, payload)
					} else {
						port = uint16(uint32(payload[0]) | uint32(payload[1])<<8)
					}
				}
			}
			observations <- mappingServerObservation{port, err}
			if err != nil {
				_ = conn.Close()
				continue
			}
			if closeAfterLogin {
				_, _, _ = soulseek.ReadFrame(conn) // SetShareIndex after connectOnce publishes the client.
				_ = conn.Close()
				continue
			}
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}
	}()
	return listener.Addr().String(), observations
}

type fakePortMapping struct {
	once   sync.Once
	closed *atomic.Int32
	events chan<- string
	name   string
}

func (f *fakePortMapping) Close() error {
	f.once.Do(func() {
		f.closed.Add(1)
		if f.events != nil {
			f.events <- f.name
		}
	})
	return nil
}

func prepareConnectOnce(t *testing.T, server string, natPMP, upnp bool) (*Service, context.Context) {
	t.Helper()
	cfg := testConfig(t)
	cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = server, closedAddress(t)
	cfg.Soulseek.NATPMPPortMapping, cfg.Soulseek.UPnPPortMapping = natPMP, upnp
	service, err := New(cfg, t.TempDir()+"/state.json")
	if err != nil {
		t.Fatal(err)
	}
	service.runCtx, service.runCancel = context.WithCancel(context.Background())
	service.ctx, service.cancel = context.WithCancel(service.runCtx)
	service.presence = PresenceOnline
	t.Cleanup(func() { _ = service.Close() })
	return service, service.ctx
}

func awaitMappingObservation(t *testing.T, observations <-chan mappingServerObservation) mappingServerObservation {
	t.Helper()
	select {
	case observation := <-observations:
		if observation.err != nil {
			t.Fatal(observation.err)
		}
		return observation
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for login")
		return mappingServerObservation{}
	}
}

func TestPortMappingRunsBeforeLoginAndClosesOnShutdown(t *testing.T) {
	events := make(chan string, 4)
	server, observations := startMappingServer(t, false, events)
	service, ctx := prepareConnectOnce(t, server, true, true)
	var closed atomic.Int32
	var internalPort uint16
	service.portMapOpen = func(_ context.Context, port uint16, natPMP, upnp bool, changed func(uint16)) (portMapping, error) {
		internalPort = port
		if !natPMP || !upnp {
			t.Errorf("mapping switches: NAT-PMP=%v UPnP=%v", natPMP, upnp)
		}
		events <- "map"
		changed(62000)
		return &fakePortMapping{closed: &closed}, nil
	}
	if err := service.connectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if first, second := <-events, <-events; first != "map" || second != "login" {
		t.Fatalf("startup order=%q, %q", first, second)
	}
	if observation := awaitMappingObservation(t, observations); observation.port != 62000 {
		t.Fatalf("advertised port=%d", observation.port)
	}
	if internalPort == 0 || internalPort == 62000 {
		t.Fatalf("internal port=%d", internalPort)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 {
		t.Fatalf("mapping closes=%d", closed.Load())
	}
}

func TestPortMappingFailureDisabledAndListenPortFilePrecedence(t *testing.T) {
	t.Run("failure is nonfatal", func(t *testing.T) {
		server, observations := startMappingServer(t, false, nil)
		service, ctx := prepareConnectOnce(t, server, true, true)
		var calls atomic.Int32
		var internalPort uint16
		var failed *fakePortMapping
		service.portMapOpen = func(_ context.Context, port uint16, _, _ bool, _ func(uint16)) (portMapping, error) {
			calls.Add(1)
			internalPort = port
			return failed, errors.New("router unavailable")
		}
		if err := service.connectOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if observation := awaitMappingObservation(t, observations); calls.Load() != 1 || observation.port != internalPort {
			t.Fatalf("calls=%d advertised=%d internal=%d", calls.Load(), observation.port, internalPort)
		}
		if err := service.SetPresence(PresenceOffline); err != nil {
			t.Fatal(err)
		}
		if err := service.SetPresence(PresenceOnline); err != nil {
			t.Fatal(err)
		}
		if observation := awaitMappingObservation(t, observations); calls.Load() != 2 || observation.port != internalPort {
			t.Fatalf("reconnect: calls=%d advertised=%d internal=%d", calls.Load(), observation.port, internalPort)
		}
	})

	t.Run("both disabled", func(t *testing.T) {
		server, observations := startMappingServer(t, false, nil)
		service, ctx := prepareConnectOnce(t, server, false, false)
		var calls atomic.Int32
		service.portMapOpen = func(context.Context, uint16, bool, bool, func(uint16)) (portMapping, error) {
			calls.Add(1)
			return nil, nil
		}
		if err := service.connectOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if observation := awaitMappingObservation(t, observations); calls.Load() != 0 || observation.port != service.client.ListenPort() {
			t.Fatalf("calls=%d advertised=%d listener=%d", calls.Load(), observation.port, service.client.ListenPort())
		}
	})

	t.Run("network interface", func(t *testing.T) {
		server, observations := startMappingServer(t, false, nil)
		service, ctx := prepareConnectOnce(t, server, true, true)
		service.cfg.Soulseek.NetworkInterface = "lo"
		var calls atomic.Int32
		service.portMapOpen = func(context.Context, uint16, bool, bool, func(uint16)) (portMapping, error) {
			calls.Add(1)
			return nil, nil
		}
		if err := service.connectOnce(ctx); err != nil {
			t.Fatal(err)
		}
		cfg := service.Config()
		if observation := awaitMappingObservation(t, observations); calls.Load() != 0 || observation.port != service.client.ListenPort() || !cfg.Soulseek.NATPMPPortMapping || !cfg.Soulseek.UPnPPortMapping {
			t.Fatalf("calls=%d advertised=%d listener=%d config=%+v", calls.Load(), observation.port, service.client.ListenPort(), cfg.Soulseek)
		}
	})

	t.Run("listen port file", func(t *testing.T) {
		server, observations := startMappingServer(t, false, nil)
		service, ctx := prepareConnectOnce(t, server, true, true)
		address := closedAddress(t)
		_, portText, _ := net.SplitHostPort(address)
		port, _ := strconv.Atoi(portText)
		service.listenPortFile, service.listenPort = "/run/oto/forwarded-port", uint16(port)
		var calls atomic.Int32
		service.portMapOpen = func(context.Context, uint16, bool, bool, func(uint16)) (portMapping, error) {
			calls.Add(1)
			return nil, nil
		}
		if err := service.connectOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if observation := awaitMappingObservation(t, observations); calls.Load() != 0 || observation.port != uint16(port) {
			t.Fatalf("calls=%d advertised=%d file port=%d", calls.Load(), observation.port, port)
		}
	})
}

func TestPortMappingClosesBeforeReconnect(t *testing.T) {
	events := make(chan string, 16)
	server, _ := startMappingServer(t, true, nil)
	cfg := testConfig(t)
	cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = server, closedAddress(t)
	cfg.Soulseek.NATPMPPortMapping = true
	service, err := New(cfg, t.TempDir()+"/state.json")
	if err != nil {
		t.Fatal(err)
	}
	var opened, closed atomic.Int32
	service.portMapOpen = func(_ context.Context, _ uint16, _, _ bool, changed func(uint16)) (portMapping, error) {
		id := opened.Add(1)
		events <- "open-" + strconv.Itoa(int(id))
		changed(uint16(62000 + id))
		return &fakePortMapping{closed: &closed, events: events, name: "close-" + strconv.Itoa(int(id))}, nil
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	want := []string{"open-1", "close-1", "open-2"}
	for i, expected := range want {
		select {
		case got := <-events:
			if got != expected {
				t.Fatalf("event %d=%q, want %q", i, got, expected)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %q", expected)
		}
	}
	if opened.Load() < 2 || closed.Load() < 1 {
		t.Fatalf("opened=%d closed=%d", opened.Load(), closed.Load())
	}
}
