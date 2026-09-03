package portmap

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huin/goupnp/soap"
	nat "github.com/libp2p/go-nat"
)

type addResult struct {
	port int
	err  error
}

type addCall struct {
	protocol, description string
	internal              int
	lease                 time.Duration
}

type fakeNAT struct {
	kind        string
	defaultPort int
	mu          sync.Mutex
	results     []addResult
	calls       []addCall
	deletes     int
	deleted     chan struct{}
}

func (f *fakeNAT) Type() string                        { return f.kind }
func (f *fakeNAT) GetDeviceAddress() (net.IP, error)   { return net.IPv4(192, 0, 2, 1), nil }
func (f *fakeNAT) GetExternalAddress() (net.IP, error) { return net.IPv4(198, 51, 100, 1), nil }
func (f *fakeNAT) GetInternalAddress() (net.IP, error) { return net.IPv4(192, 0, 2, 2), nil }
func (f *fakeNAT) AddPortMapping(_ context.Context, protocol string, internal int, description string, lease time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, addCall{protocol, description, internal, lease})
	if len(f.results) == 0 {
		return f.defaultPort, nil
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result.port, result.err
}
func (f *fakeNAT) DeletePortMapping(context.Context, string, int) error {
	f.mu.Lock()
	f.deletes++
	f.mu.Unlock()
	if f.deleted != nil {
		select {
		case f.deleted <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *fakeNAT) snapshot() ([]addCall, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]addCall(nil), f.calls...), f.deletes
}

func discovery(count *atomic.Int32, candidates ...nat.NAT) func(context.Context) <-chan nat.NAT {
	return func(context.Context) <-chan nat.NAT {
		count.Add(1)
		out := make(chan nat.NAT, len(candidates))
		for _, candidate := range candidates {
			out <- candidate
		}
		close(out)
		return out
	}
}

func TestOpenFiltersAndPrefersNATPMP(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		natPMP, upnp         bool
		wantKind             string
		wantPMP, wantUPnPAdd bool
	}{
		{"both", true, true, "NAT-PMP", true, false},
		{"nat-pmp only", true, false, "NAT-PMP", true, false},
		{"upnp only", false, true, "UPNP (IG2-IP1)", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pmp := &fakeNAT{kind: "NAT-PMP", defaultPort: 5100}
			upnp := &fakeNAT{kind: "UPNP (IG2-IP1)", defaultPort: 5200}
			var discoveries atomic.Int32
			var changed uint16
			mapping, err := open(context.Background(), 50300, tc.natPMP, tc.upnp, func(port uint16) { changed = port }, time.Hour, discovery(&discoveries, upnp, pmp))
			if err != nil {
				t.Fatal(err)
			}
			if mapping.gateway.Type() != tc.wantKind {
				t.Fatalf("selected %q, want %q", mapping.gateway.Type(), tc.wantKind)
			}
			pmpCalls, _ := pmp.snapshot()
			upnpCalls, _ := upnp.snapshot()
			if (len(pmpCalls) > 0) != tc.wantPMP || (len(upnpCalls) > 0) != tc.wantUPnPAdd {
				t.Fatalf("mapping calls: NAT-PMP=%d UPnP=%d", len(pmpCalls), len(upnpCalls))
			}
			selectedCalls := pmpCalls
			wantPort := uint16(5100)
			if tc.wantKind != "NAT-PMP" {
				selectedCalls, wantPort = upnpCalls, 5200
			}
			if changed != wantPort || len(selectedCalls) != 1 {
				t.Fatalf("callback=%d calls=%+v", changed, selectedCalls)
			}
			call := selectedCalls[0]
			if call.protocol != "tcp" || call.internal != 50300 || call.description != "oto" || call.lease != 12*time.Hour {
				t.Fatalf("mapping request: %+v", call)
			}
			if err := mapping.Close(); err != nil {
				t.Fatal(err)
			}
			selectedCalls, deletes := mapping.gateway.(*fakeNAT).snapshot()
			wantCalls := 1
			if tc.wantKind == "NAT-PMP" {
				wantCalls = 2
				if selectedCalls[1].lease != 0 {
					t.Fatalf("NAT-PMP cleanup lease=%v", selectedCalls[1].lease)
				}
			}
			if len(selectedCalls) != wantCalls || deletes != 1 {
				t.Fatalf("cleanup calls=%d deletes=%d", len(selectedCalls), deletes)
			}
			if discoveries.Load() != 1 {
				t.Fatalf("discoveries=%d", discoveries.Load())
			}
		})
	}
}

func TestOpenFallsBackToUPnPAndRetriesPermanentLease(t *testing.T) {
	var fault soap.SOAPFaultError
	fault.Detail.UPnPError.Errorcode = 725
	pmp := &fakeNAT{kind: "NAT-PMP", results: []addResult{{err: errors.New("refused")}}}
	upnp := &fakeNAT{kind: "UPNP (IG1-IP1)", defaultPort: 5300, results: []addResult{{err: &fault}, {port: 5300}}}
	var discoveries atomic.Int32
	mapping, err := open(context.Background(), 50300, true, true, nil, time.Hour, discovery(&discoveries, upnp, pmp))
	if err != nil {
		t.Fatal(err)
	}
	defer mapping.Close()
	if mapping.gateway != upnp || mapping.lease != 0 {
		t.Fatalf("selected mapping: %+v", mapping)
	}
	calls, _ := upnp.snapshot()
	if len(calls) != 2 || calls[0].lease != 12*time.Hour || calls[1].lease != 0 {
		t.Fatalf("UPnP lease attempts: %+v", calls)
	}
}

func TestRenewalReportsChangedPortAndCancellationDeletes(t *testing.T) {
	gateway := &fakeNAT{
		kind: "UPNP (IG2-IP1)", defaultPort: 5401, deleted: make(chan struct{}, 1),
		results: []addResult{{port: 5400}, {err: errors.New("temporary")}, {port: 5401}},
	}
	var discoveries atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	changed := make(chan uint16, 3)
	mapping, err := open(ctx, 50300, false, true, func(port uint16) { changed <- port }, 5*time.Millisecond, discovery(&discoveries, gateway))
	if err != nil {
		t.Fatal(err)
	}
	if port := <-changed; port != 5400 {
		t.Fatalf("initial port=%d", port)
	}
	select {
	case port := <-changed:
		if port != 5401 {
			t.Fatalf("renewed port=%d", port)
		}
	case <-time.After(time.Second):
		t.Fatal("changed renewal port was not reported")
	}
	cancel()
	select {
	case <-gateway.deleted:
	case <-time.After(time.Second):
		t.Fatal("mapping was not deleted on cancellation")
	}
	if err := mapping.Close(); err != nil {
		t.Fatal(err)
	}
	calls, deletes := gateway.snapshot()
	if len(calls) < 3 || deletes != 1 {
		t.Fatalf("calls=%d deletes=%d", len(calls), deletes)
	}
}

func TestDisabledSkipsDiscovery(t *testing.T) {
	var discoveries atomic.Int32
	mapping, err := open(context.Background(), 50300, false, false, nil, time.Hour, discovery(&discoveries))
	if err != nil || mapping != nil || discoveries.Load() != 0 {
		t.Fatalf("mapping=%v err=%v discoveries=%d", mapping, err, discoveries.Load())
	}
}
