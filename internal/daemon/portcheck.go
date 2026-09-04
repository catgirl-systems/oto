package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

const (
	portCheckerURL   = "https://www.slsknet.org/porttest.php?port=%d"
	portCheckTimeout = 5 * time.Second
	maxPortCheckBody = 64 << 10
)

var ErrPortCheckFailed = errors.New("daemon: listening port check failed")

type ListeningPortCheck struct {
	Port uint16 `json:"port"`
	Open bool   `json:"open"`
}

type listeningPortChecker func(context.Context, *soulseek.Client, uint16) (bool, error)

func defaultListeningPortCheck(ctx context.Context, client *soulseek.Client, port uint16) (bool, error) {
	return checkListeningPort(ctx, client, portCheckerURL, port)
}

func checkListeningPort(ctx context.Context, client *soulseek.Client, checkerURL string, port uint16) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, portCheckTimeout)
	defer cancel()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
		return client.DialContext(ctx, "tcp4", address)
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(checkerURL, port), nil)
	if err != nil {
		return false, err
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPortCheckBody+1))
	if err != nil {
		return false, err
	}
	if len(body) > maxPortCheckBody {
		return false, errors.New("response too large")
	}
	body = bytes.ToLower(body)
	switch {
	case bytes.Contains(body, []byte(fmt.Sprintf("%d/tcp open", port))):
		return true, nil
	case bytes.Contains(body, []byte(fmt.Sprintf("%d/tcp closed", port))):
		return false, nil
	default:
		return false, errors.New("unrecognized response")
	}
}

func (s *Service) CheckListeningPort(ctx context.Context) (ListeningPortCheck, error) {
	s.mu.RLock()
	if s.status != StatusConnected || s.client == nil {
		s.mu.RUnlock()
		return ListeningPortCheck{}, ErrNotStarted
	}
	client, check := s.client, s.portCheck
	port := client.PublicPort()
	s.mu.RUnlock()
	if port == 0 {
		return ListeningPortCheck{}, ErrListenPortUnavailable
	}
	open, err := check(ctx, client, port)
	if err != nil {
		return ListeningPortCheck{}, fmt.Errorf("%w: %v", ErrPortCheckFailed, err)
	}
	return ListeningPortCheck{Port: port, Open: open}, nil
}
