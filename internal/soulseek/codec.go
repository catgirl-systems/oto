package soulseek

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/catgirl-systems/oto/internal/diagnostics"
	"io"
	"log/slog"
	"time"
)

const (
	ProtocolVersion     uint32 = 170
	ProtocolMinor       uint32 = 2718
	MaxFrameSize               = 64 << 20
	MaxStringSize              = 4 << 20
	MaxBytesSize               = 16 << 20
	MaxDecompressedSize        = 256 << 20
)

var (
	ErrMalformed = errors.New("soulseek: malformed message")
	ErrTooLarge  = errors.New("soulseek: message too large")
	ErrTruncated = errors.New("soulseek: truncated message")
)

// Encoder writes little-endian Soulseek payload values.
type Encoder struct{ buf bytes.Buffer }

func (e *Encoder) U8(v uint8)   { _ = e.buf.WriteByte(v) }
func (e *Encoder) U16(v uint16) { _ = binary.Write(&e.buf, binary.LittleEndian, v) }
func (e *Encoder) U32(v uint32) { _ = binary.Write(&e.buf, binary.LittleEndian, v) }
func (e *Encoder) U64(v uint64) { _ = binary.Write(&e.buf, binary.LittleEndian, v) }
func (e *Encoder) Bool(v bool) {
	if v {
		e.U8(1)
	} else {
		e.U8(0)
	}
}
func (e *Encoder) Bytes(v []byte) error {
	if len(v) > MaxBytesSize {
		return ErrTooLarge
	}
	e.U32(uint32(len(v)))
	_, err := e.buf.Write(v)
	return err
}
func (e *Encoder) String(v string) error {
	if len(v) > MaxStringSize {
		return ErrTooLarge
	}
	return e.Bytes([]byte(v))
}
func (e *Encoder) Raw(v []byte)    { _, _ = e.buf.Write(v) }
func (e *Encoder) Payload() []byte { return e.buf.Bytes() }

// Decoder reads a bounded Soulseek payload. Every read is checked before advancing.
type Decoder struct {
	b   []byte
	off int
}

func NewDecoder(b []byte) *Decoder { return &Decoder{b: b} }
func (d *Decoder) need(n int) error {
	if n < 0 || n > len(d.b)-d.off {
		return ErrTruncated
	}
	return nil
}
func (d *Decoder) U8() (uint8, error) {
	if err := d.need(1); err != nil {
		return 0, err
	}
	v := d.b[d.off]
	d.off++
	return v, nil
}
func (d *Decoder) U16() (uint16, error) {
	if err := d.need(2); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint16(d.b[d.off:])
	d.off += 2
	return v, nil
}
func (d *Decoder) U32() (uint32, error) {
	if err := d.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(d.b[d.off:])
	d.off += 4
	return v, nil
}
func (d *Decoder) U64() (uint64, error) {
	if err := d.need(8); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(d.b[d.off:])
	d.off += 8
	return v, nil
}
func (d *Decoder) Bool() (bool, error) {
	v, e := d.U8()
	if e != nil {
		return false, e
	}
	if v > 1 {
		return false, fmt.Errorf("%w: invalid bool", ErrMalformed)
	}
	return v == 1, nil
}
func (d *Decoder) Bytes() ([]byte, error) {
	n, e := d.U32()
	if e != nil {
		return nil, e
	}
	if n > MaxBytesSize {
		return nil, fmt.Errorf("%w: byte field has %d bytes (limit %d)", ErrTooLarge, n, MaxBytesSize)
	}
	if e = d.need(int(n)); e != nil {
		return nil, e
	}
	v := d.b[d.off : d.off+int(n)]
	d.off += int(n)
	return v, nil
}
func (d *Decoder) String() (string, error) {
	v, e := d.Bytes()
	if e != nil {
		return "", e
	}
	if len(v) > MaxStringSize {
		return "", fmt.Errorf("%w: string has %d bytes (limit %d)", ErrTooLarge, len(v), MaxStringSize)
	}
	return string(v), nil
}
func (d *Decoder) Remaining() int { return len(d.b) - d.off }
func (d *Decoder) Done() error {
	if d.Remaining() != 0 {
		return fmt.Errorf("%w: %d trailing bytes", ErrMalformed, d.Remaining())
	}
	return nil
}

// ReadFrame reads a server/peer frame: uint32 length, uint32 command, payload.
func ReadFrame(r io.Reader) (uint32, []byte, error) {
	return readFrame(r, nil, MaxFrameSize)
}

// ReadFrameWithProgress reports received body bytes once the frame size is known.
func ReadFrameWithProgress(r io.Reader, progress func(received, total uint64)) (uint32, []byte, error) {
	return readFrame(r, progress, MaxFrameSize)
}

func readFrame(r io.Reader, progress func(received, total uint64), maxFrameSize int) (uint32, []byte, error) {
	body, err := readBody(r, 4, progress, maxFrameSize)
	if err != nil {
		return 0, nil, err
	}
	return binary.LittleEndian.Uint32(body), body[4:], nil
}

func WriteFrame(w io.Writer, command uint32, payload []byte) error {
	var code [4]byte
	binary.LittleEndian.PutUint32(code[:], command)
	return writeBody(w, code[:], payload)
}

// ReadInitFrame reads peer-init/distributed framing, whose command is one byte.
func ReadInitFrame(r io.Reader) (byte, []byte, error) {
	body, err := readBody(r, 1, nil, MaxFrameSize)
	if err != nil {
		return 0, nil, err
	}
	return body[0], body[1:], nil
}

func WriteInitFrame(w io.Writer, command byte, payload []byte) error {
	return writeBody(w, []byte{command}, payload)
}

func readBody(r io.Reader, header int, progress func(received, total uint64), maxFrameSize int) ([]byte, error) {
	var logger *slog.Logger
	if p, ok := r.(*diagnosticConn); ok {
		logger = p.logger
	}
	var received uint64
	lastLog := time.Now()
	commandLogged := false
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		// EOF between frames is a connection close, not an invalid payload.
		diagnostics.Event(logger, slog.LevelDebug, "frame_read_failed", err, slog.String("stage", "frame_length"))
		return nil, fmt.Errorf("soulseek: read frame length: %w", err)
	}
	diagnostics.Event(logger, slog.LevelDebug, "frame_length_received", nil, slog.Uint64("declared_size", uint64(n)))
	if n < uint32(header) {
		return nil, fmt.Errorf("%w: short frame header", ErrMalformed)
	}
	if uint64(n) > uint64(maxFrameSize) {
		diagnostics.Event(logger, slog.LevelWarn, "frame_limit_rejected", nil, slog.String("limit_name", "frame_bytes"), slog.Int("limit", maxFrameSize), slog.Uint64("actual", uint64(n)))
		return nil, fmt.Errorf("%w: frame has %d bytes (limit %d)", ErrTooLarge, n, maxFrameSize)
	}
	body := make([]byte, n)
	reader := r
	if progress != nil || logger != nil {
		total := uint64(n)
		if progress != nil {
			progress(0, total)
		}
		reader = io.TeeReader(r, writerFunc(func(p []byte) (int, error) {
			received += uint64(len(p))
			if progress != nil {
				progress(received, total)
			}
			if !commandLogged && received >= uint64(header) {
				command := uint64(body[0])
				if header == 4 {
					command = uint64(binary.LittleEndian.Uint32(body))
				}
				diagnostics.Event(logger, slog.LevelDebug, "frame_header_received", nil, slog.Uint64("command", command), slog.Uint64("declared_size", total))
				commandLogged = true
			}
			if logger != nil && logger.Enabled(context.Background(), slog.LevelDebug) && (received == total || time.Since(lastLog) >= 5*time.Second) {
				diagnostics.Event(logger, slog.LevelDebug, "frame_receive_progress", nil, slog.Uint64("received", received), slog.Uint64("total", total))
				lastLog = time.Now()
			}
			return len(p), nil
		}))
	}
	if _, err := io.ReadFull(reader, body); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // The frame length was already received.
		}
		diagnostics.Event(logger, slog.LevelDebug, "frame_read_failed", err, slog.String("stage", "frame_body"), slog.Uint64("received", received), slog.Uint64("declared_size", uint64(n)))
		return nil, fmt.Errorf("soulseek: read frame body: %w", err)
	}
	return body, nil
}

type writerFunc func([]byte) (int, error)

func (fn writerFunc) Write(p []byte) (int, error) { return fn(p) }

func writeBody(w io.Writer, header, payload []byte) error {
	if len(header)+len(payload) > MaxFrameSize {
		return ErrTooLarge
	}
	var length [4]byte
	binary.LittleEndian.PutUint32(length[:], uint32(len(header)+len(payload)))
	for _, part := range [][]byte{length[:], header, payload} {
		if _, err := w.Write(part); err != nil {
			return err
		}
	}
	return nil
}

func DecompressZlib(data []byte) ([]byte, error) {
	return decompressZlib(data, MaxFrameSize, MaxDecompressedSize)
}

func decompressZlib(data []byte, maxCompressedSize, maxDecompressedSize int, loggers ...*slog.Logger) ([]byte, error) {
	if len(data) > maxCompressedSize {
		logLimit(firstLogger(loggers), "compressed_bytes", uint64(maxCompressedSize), uint64(len(data)))
		return nil, fmt.Errorf("%w: compressed payload has %d bytes (limit %d)", ErrTooLarge, len(data), maxCompressedSize)
	}
	z, e := zlib.NewReader(bytes.NewReader(data))
	if e != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, e)
	}
	defer z.Close()
	lr := io.LimitReader(z, int64(maxDecompressedSize)+1)
	out, e := io.ReadAll(lr)
	if e != nil {
		return nil, e
	}
	if len(out) > maxDecompressedSize {
		logLimit(firstLogger(loggers), "decompressed_bytes", uint64(maxDecompressedSize), uint64(len(out)))
		return nil, fmt.Errorf("%w: decompressed payload exceeds %d bytes", ErrTooLarge, maxDecompressedSize)
	}
	return out, nil
}

func CompressZlib(data []byte) ([]byte, error) {
	if len(data) > MaxDecompressedSize {
		return nil, ErrTooLarge
	}
	var out bytes.Buffer
	z := zlib.NewWriter(&out)
	if _, err := z.Write(data); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	if out.Len() > MaxFrameSize {
		return nil, ErrTooLarge
	}
	return out.Bytes(), nil
}
