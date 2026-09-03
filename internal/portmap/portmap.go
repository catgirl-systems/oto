package portmap

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/huin/goupnp/soap"
	nat "github.com/libp2p/go-nat"
)

const (
	leaseDuration    = 12 * time.Hour
	renewalInterval  = 2 * time.Hour
	discoveryTimeout = 5 * time.Second
	operationTimeout = 5 * time.Second
)

// Mapping owns one router mapping and its renewal loop.
type Mapping struct {
	gateway       nat.NAT
	internalPort  int
	lease         time.Duration
	renewal       time.Duration
	operationWait time.Duration
	onChange      func(uint16)
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	cleanupErr    error
}

// Open discovers and maps internalPort. A nil mapping means both protocols are disabled.
func Open(ctx context.Context, internalPort uint16, natPMP, upnp bool, onChange func(uint16)) (*Mapping, error) {
	return open(ctx, internalPort, natPMP, upnp, onChange, renewalInterval, nat.DiscoverNATs)
}

func open(ctx context.Context, internalPort uint16, natPMP, upnp bool, onChange func(uint16), renewal time.Duration, discover func(context.Context) <-chan nat.NAT) (*Mapping, error) {
	if !natPMP && !upnp {
		return nil, nil
	}
	if internalPort == 0 {
		return nil, errors.New("port mapping: invalid internal port")
	}

	discoveryCtx, cancelDiscovery := context.WithTimeout(ctx, discoveryTimeout)
	var pmpCandidates, upnpCandidates []nat.NAT
	for gateway := range discover(discoveryCtx) {
		kind := strings.ToUpper(gateway.Type())
		if natPMP && kind == "NAT-PMP" {
			pmpCandidates = append(pmpCandidates, gateway)
		} else if upnp && strings.HasPrefix(kind, "UPNP") {
			upnpCandidates = append(upnpCandidates, gateway)
		}
	}
	cancelDiscovery()
	candidates := append(pmpCandidates, upnpCandidates...)
	if len(candidates) == 0 {
		return nil, nat.ErrNoNATFound
	}

	var failures []error
	for _, gateway := range candidates {
		opCtx, cancel := context.WithTimeout(ctx, operationTimeout)
		externalPort, actualLease, err := add(opCtx, gateway, int(internalPort), leaseDuration)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", gateway.Type(), err))
			continue
		}
		if externalPort < 1 || externalPort > 65535 {
			failures = append(failures, fmt.Errorf("%s: invalid external port %d", gateway.Type(), externalPort))
			continue
		}

		mappingCtx, cancelMapping := context.WithCancel(ctx)
		mapping := &Mapping{
			gateway: gateway, internalPort: int(internalPort), lease: actualLease,
			renewal: renewal, operationWait: operationTimeout, onChange: onChange,
			ctx: mappingCtx, cancel: cancelMapping, done: make(chan struct{}),
		}
		log.Printf("port mapping: mapped TCP port %d to external port %d using %s", internalPort, externalPort, gateway.Type())
		if onChange != nil {
			onChange(uint16(externalPort))
		}
		go mapping.run(uint16(externalPort))
		return mapping, nil
	}
	return nil, fmt.Errorf("port mapping: %w", errors.Join(failures...))
}

func add(ctx context.Context, gateway nat.NAT, internalPort int, lease time.Duration) (int, time.Duration, error) {
	externalPort, err := gateway.AddPortMapping(ctx, "tcp", internalPort, "oto", lease)
	if err == nil || !isUPnP(gateway.Type()) || !isPermanentLeaseFault(err) {
		return externalPort, lease, err
	}
	externalPort, err = gateway.AddPortMapping(ctx, "tcp", internalPort, "oto", 0)
	return externalPort, 0, err
}

func isUPnP(kind string) bool { return strings.HasPrefix(strings.ToUpper(kind), "UPNP") }

func isPermanentLeaseFault(err error) bool {
	var fault *soap.SOAPFaultError
	return errors.As(err, &fault) && fault.Detail.UPnPError.Errorcode == 725
}

func (m *Mapping) run(externalPort uint16) {
	defer close(m.done)
	defer func() { m.cleanupErr = m.remove() }()
	ticker := time.NewTicker(m.renewal)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			opCtx, cancel := context.WithTimeout(m.ctx, m.operationWait)
			port, lease, err := add(opCtx, m.gateway, m.internalPort, m.lease)
			cancel()
			if err != nil {
				log.Printf("port mapping: renew %s TCP port %d: %v", m.gateway.Type(), m.internalPort, err)
				continue
			}
			if port < 1 || port > 65535 {
				log.Printf("port mapping: renew %s TCP port %d returned invalid external port %d", m.gateway.Type(), m.internalPort, port)
				continue
			}
			m.lease = lease
			if uint16(port) != externalPort {
				externalPort = uint16(port)
				if m.onChange != nil {
					m.onChange(externalPort)
				}
			}
		}
	}
}

func (m *Mapping) remove() error {
	ctx, cancel := context.WithTimeout(context.Background(), m.operationWait)
	defer cancel()
	var expireErr error
	if strings.EqualFold(m.gateway.Type(), "NAT-PMP") {
		// go-nat tracks NAT-PMP deletion locally; a zero lease expires it on the router.
		_, expireErr = m.gateway.AddPortMapping(ctx, "tcp", m.internalPort, "oto", 0)
	}
	return errors.Join(expireErr, m.gateway.DeletePortMapping(ctx, "tcp", m.internalPort))
}

// Close stops renewal and removes the mapping. It is safe to call repeatedly.
func (m *Mapping) Close() error {
	m.cancel()
	<-m.done
	return m.cleanupErr
}
