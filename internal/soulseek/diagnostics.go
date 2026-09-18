package soulseek

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/catgirl-systems/oto/internal/diagnostics"
)

type diagnosticConn struct {
	net.Conn
	logger    *slog.Logger
	id        string
	opened    time.Time
	closeOnce sync.Once
}

func (p *diagnosticConn) SyscallConn() (syscall.RawConn, error) {
	if raw, ok := p.Conn.(interface {
		SyscallConn() (syscall.RawConn, error)
	}); ok {
		return raw.SyscallConn()
	}
	return nil, errors.New("socket does not expose a raw connection")
}

type browseIDKey struct{}

func (c *Client) browseContext(ctx context.Context) (context.Context, string) {
	if id, ok := ctx.Value(browseIDKey{}).(string); ok {
		return ctx, id
	}
	id := diagnostics.NextID("browse")
	ctx = diagnostics.WithLogger(ctx, c.logger(ctx).With("operation_id", id, "component", "soulseek"))
	return context.WithValue(ctx, browseIDKey{}, id), id
}
func (c *Client) browsePeer(ctx context.Context, peer net.Conn) (context.Context, net.Conn) {
	if _, ok := peer.(*diagnosticConn); !ok && leasedPeer(peer) == nil {
		peer = c.traceConn(ctx, peer, "", "P", "supplied")
	}
	return diagnostics.WithLogger(ctx, c.peerLogger(ctx, peer)), peer
}

func (p *diagnosticConn) Close() error {
	err := p.Conn.Close()
	p.closeOnce.Do(func() {
		diagnostics.Event(p.logger, slog.LevelDebug, "connection_closed", err, slog.String("close_initiator", "local"), slog.Int64("elapsed_ms", time.Since(p.opened).Milliseconds()))
	})
	return err
}
func (p *diagnosticConn) Read(b []byte) (int, error) {
	n, err := p.Conn.Read(b)
	if err != nil {
		diagnostics.Event(p.logger, slog.LevelDebug, "connection_read_ended", err)
	}
	return n, err
}
func (c *Client) logger(ctx context.Context, attrs ...slog.Attr) *slog.Logger {
	l := diagnostics.FromContext(ctx, c.cfg.Logger)
	if len(attrs) == 0 {
		return l
	}
	args := make([]any, len(attrs))
	for i, a := range attrs {
		args[i] = a
	}
	return l.With(args...)
}
func (c *Client) log(ctx context.Context, level slog.Level, event string, err error, attrs ...slog.Attr) {
	diagnostics.Event(c.logger(ctx), level, event, err, attrs...)
}
func diagnosticEndpoint(address net.Addr) string {
	if tcp, ok := address.(*net.TCPAddr); ok {
		return tcp.String()
	}
	return ""
}

func (c *Client) traceConn(ctx context.Context, peer net.Conn, username, kind, direction string) net.Conn {
	l := c.logger(ctx)
	if !l.Enabled(ctx, slog.LevelError) {
		return peer
	}
	id := diagnostics.NextID("conn")
	l = l.With("component", "soulseek", "connection_id", id, "type", kind, "direction", direction)
	if username != "" {
		l = l.With("peer_username", username)
	}
	if endpoint := diagnosticEndpoint(peer.LocalAddr()); endpoint != "" {
		l = l.With("local_endpoint", endpoint)
	}
	if endpoint := diagnosticEndpoint(peer.RemoteAddr()); endpoint != "" {
		l = l.With("remote_endpoint", endpoint)
	}
	diagnostics.Event(l, slog.LevelDebug, "connection_open", nil)
	return &diagnosticConn{Conn: peer, logger: l, id: id, opened: time.Now()}
}
func (c *Client) peerLogger(ctx context.Context, peer net.Conn) *slog.Logger {
	if lease := leasedPeer(peer); lease != nil {
		lease.mu.Lock()
		peer = lease.Conn
		lease.mu.Unlock()
		logger := c.logger(ctx).With("peer_username", lease.username, "type", "P")
		if p, ok := peer.(*diagnosticConn); ok {
			logger = logger.With("connection_id", p.id)
		}
		return logger.With("local_endpoint", diagnosticEndpoint(peer.LocalAddr()), "remote_endpoint", diagnosticEndpoint(peer.RemoteAddr()))
	}
	if p, ok := peer.(*diagnosticConn); ok {
		return p.logger
	}
	return c.logger(ctx)
}
func (c *Client) linkFileLogger(ctx context.Context, peer net.Conn) *slog.Logger {
	l := c.logger(ctx).With("component", "soulseek", "type", "F")
	if p, ok := peer.(*diagnosticConn); ok {
		l = l.With("connection_id", p.id)
	}
	if endpoint := diagnosticEndpoint(peer.RemoteAddr()); endpoint != "" {
		l = l.With("remote_endpoint", endpoint)
	}
	return l
}

type transferObservation struct {
	mu                                                   sync.Mutex
	id, direction, stage                                 string
	logger                                               *slog.Logger
	offset, size, received, written, committed, previous uint64
	started, lastRead, lastWrite, lastProgress, sampled  time.Time
	stallWarned                                          bool
}

func (c *Client) newObservation(ctx context.Context, direction string, offset, size uint64) *transferObservation {
	l := c.logger(ctx)
	if !l.Enabled(ctx, slog.LevelError) {
		return nil
	}
	id := diagnostics.NextID("stream")
	o := &transferObservation{id: id, direction: direction, offset: offset, committed: offset, size: size, stage: "queued", logger: l.With("operation_id", id, "direction", direction)}
	c.mu.Lock()
	if c.observations == nil {
		c.observations = make(map[string]*transferObservation)
	}
	c.observations[id] = o
	c.mu.Unlock()
	diagnostics.Event(o.logger, slog.LevelInfo, "transfer_queued", nil, slog.Uint64("offset", offset), slog.Uint64("size", size))
	return o
}
func (c *Client) endObservation(o *transferObservation) {
	if o == nil {
		return
	}
	c.mu.Lock()
	delete(c.observations, o.id)
	c.mu.Unlock()
}
func (c *Client) observationStage(ctx context.Context, o *transferObservation, stage string, level slog.Level, err error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.stage = stage
	l := c.logger(ctx)
	o.mu.Unlock()
	diagnostics.Event(l, level, stage, err)
}
func (o *transferObservation) begin(l *slog.Logger) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.started = time.Now()
	o.sampled = o.started
	o.stage = "stream"
	o.logger = l.With("operation_id", o.id, "direction", o.direction)
	o.mu.Unlock()
}
func (o *transferObservation) setStage(stage string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.stage = stage
	o.mu.Unlock()
}
func (o *transferObservation) observe(read bool, n int, err error) {
	if o == nil {
		return
	}
	now := time.Now()
	o.mu.Lock()
	first := false
	if n > 0 {
		if read {
			first = o.received == 0
			o.received += uint64(n)
			o.lastRead = now
		} else {
			first = o.written == 0
			o.written += uint64(n)
			o.lastWrite = now
		}
	}
	l := o.logger
	o.mu.Unlock()
	stage := "write"
	if read {
		stage = "read"
	}
	if first {
		diagnostics.Event(l, slog.LevelDebug, "transfer_first_"+stage, nil, slog.Int("bytes", n))
	}
	if err != nil {
		level := slog.LevelDebug
		if !read {
			level = slog.LevelError
		}
		diagnostics.Event(l, level, "transfer_"+stage+"_ended", err, slog.String("stage", stage))
	}
}

func (o *transferObservation) commit(done uint64) {
	if o == nil {
		return
	}
	o.mu.Lock()
	advanced := done > o.committed
	recovered := advanced && o.stallWarned
	if advanced {
		o.committed = done
		o.lastProgress = time.Now()
		o.stallWarned = false
	}
	logger := o.logger
	o.mu.Unlock()
	if recovered {
		diagnostics.Event(logger, slog.LevelInfo, "transfer_recovered", nil)
	}
}
func observedProgress(o *transferObservation, next ProgressFunc) ProgressFunc {
	if o == nil {
		return next
	}
	return func(p Progress) {
		if next != nil {
			next(p)
		}
		o.commit(p.Done)
	}
}

// DownloadWaitSeconds reports time without file data while a network read is
// pending. Setup, local writes, completed attempts and unavailable observations
// are unknown, not evidence that the peer is at fault. This does not sample logs.
func (c *Client) DownloadWaitSeconds(username, filename string, now time.Time) *uint64 {
	c.mu.Lock()
	pending := c.requested[downloadKey(username, filename)]
	c.mu.Unlock()
	if pending == nil || pending.observation == nil || pending.ctx.Err() != nil {
		return nil
	}
	o := pending.observation
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.started.IsZero() || o.stage != "read" {
		return nil
	}
	last := o.lastRead
	if last.IsZero() {
		last = o.started
	}
	seconds := uint64(max(0, now.Sub(last)/time.Second))
	return &seconds
}

// LogDiagnostics is invoked by the existing five-second daemon telemetry cadence.
func (c *Client) LogDiagnostics(now time.Time) {
	c.mu.Lock()
	items := make([]*transferObservation, 0, len(c.observations))
	for _, o := range c.observations {
		items = append(items, o)
	}
	c.mu.Unlock()
	for _, o := range items {
		o.mu.Lock()
		l := o.logger
		if o.started.IsZero() || !l.Enabled(context.Background(), slog.LevelWarn) {
			o.mu.Unlock()
			continue
		}
		last := o.lastProgress
		if last.IsZero() {
			last = o.started
		}
		stalled := now.Sub(last) >= 30*time.Second
		warn, recovered := stalled && !o.stallWarned, !stalled && o.stallWarned
		o.stallWarned = stalled
		var attrs []slog.Attr
		if l.Enabled(context.Background(), slog.LevelDebug) {
			rate := uint64(0)
			if d := now.Sub(o.sampled).Seconds(); d > 0 {
				rate = uint64(float64(o.written-o.previous) / d)
			}
			attrs = []slog.Attr{slog.Time("sampled_at", now.UTC()), slog.String("stage", o.stage), slog.Uint64("received_bytes", o.received), slog.Uint64("written_bytes", o.written), slog.Uint64("committed_bytes", o.committed), slog.Uint64("size", o.size), slog.Uint64("rate_bps", rate)}
			if !o.lastRead.IsZero() {
				attrs = append(attrs, slog.Int64("last_data_age_ms", now.Sub(o.lastRead).Milliseconds()))
			}
			if !o.lastWrite.IsZero() {
				attrs = append(attrs, slog.Int64("last_write_age_ms", now.Sub(o.lastWrite).Milliseconds()))
			}
			if !o.lastProgress.IsZero() {
				attrs = append(attrs, slog.Int64("last_progress_age_ms", now.Sub(o.lastProgress).Milliseconds()))
			}
		}
		o.sampled, o.previous = now, o.written
		stage := o.stage
		o.mu.Unlock()
		if attrs != nil {
			diagnostics.Event(l, slog.LevelDebug, "transfer_summary", nil, attrs...)
		}
		if warn {
			diagnostics.Event(l, slog.LevelWarn, "transfer_stalled", nil, slog.String("stage", stage))
		}
		if recovered {
			diagnostics.Event(l, slog.LevelInfo, "transfer_recovered", nil)
		}
	}
}

type observedWriterAt struct {
	io.WriterAt
	observation *transferObservation
}
type observedReader struct {
	io.Reader
	observation *transferObservation
}

func (r observedReader) Read(p []byte) (int, error) {
	r.observation.setStage("read")
	n, e := r.Reader.Read(p)
	r.observation.observe(true, n, e)
	return n, e
}
func (w observedWriterAt) WriteAt(p []byte, off int64) (int, error) {
	w.observation.setStage("write")
	n, e := w.WriterAt.WriteAt(p, off)
	w.observation.observe(false, n, e)
	return n, e
}
