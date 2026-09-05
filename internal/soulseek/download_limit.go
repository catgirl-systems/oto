package soulseek

import (
	"context"
	"io"
	"math"
	"sync"
	"time"
)

// downloadLimiter shapes aggregate payload reads, not TCP/kernel buffering.
// There is no per-peer fairness guarantee; idle peers reserve no future credit.
type downloadLimiter struct {
	mu      sync.Mutex
	rate    int64
	credit  float64
	last    time.Time
	changed chan struct{}
}

func (l *downloadLimiter) configure(rate int64) {
	rate = max(0, rate)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.changed != nil && l.rate == rate {
		return
	}
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
	l.rate, l.credit, l.last = rate, float64(transferChunkSize(rate)), time.Now()
}

func (l *downloadLimiter) limit() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rate
}

// take debits only immediately available credit. Waiting readers hold no reservation.
func (l *downloadLimiter) take(size int, now time.Time) (int, time.Duration, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := min(size, transferChunkSize(l.rate))
	if l.rate == 0 {
		return n, 0, l.changed
	}
	if now.After(l.last) {
		l.credit = min(float64(transferChunkSize(l.rate)), l.credit+now.Sub(l.last).Seconds()*float64(l.rate))
		l.last = now
	}
	if l.credit >= float64(n) {
		l.credit -= float64(n)
		return n, 0, l.changed
	}
	delay := time.Duration(math.Ceil((float64(n) - l.credit) * float64(time.Second) / float64(l.rate)))
	return 0, max(time.Nanosecond, delay), l.changed
}

func (l *downloadLimiter) refund(n int, generation <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > 0 && l.rate != 0 && l.changed == generation {
		l.credit = min(float64(transferChunkSize(l.rate)), l.credit+float64(n))
	}
}

func (l *downloadLimiter) acquire(ctx context.Context, size int) (int, <-chan struct{}, error) {
	for {
		if ctx.Err() != nil {
			return 0, nil, ErrTransferCancelled
		}
		n, delay, changed := l.take(size, time.Now())
		if delay == 0 {
			if ctx.Err() != nil {
				l.refund(n, changed)
				return 0, nil, ErrTransferCancelled
			}
			return n, changed, nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-changed:
		case <-ctx.Done():
		}
		timer.Stop()
	}
}

type downloadReader struct {
	ctx     context.Context
	limiter *downloadLimiter
	src     io.Reader
}

func (r downloadReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, generation, err := r.limiter.acquire(r.ctx, len(p))
	if err != nil {
		return 0, err
	}
	read, err := r.src.Read(p[:n])
	r.limiter.refund(n-read, generation)
	return read, err
}
