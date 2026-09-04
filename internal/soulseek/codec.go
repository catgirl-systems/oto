package soulseek

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
		return nil, ErrTooLarge
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
		return "", ErrTooLarge
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
	return readFrame(r, nil)
}

// ReadFrameWithProgress reports received body bytes once the frame size is known.
func ReadFrameWithProgress(r io.Reader, progress func(received, total uint64)) (uint32, []byte, error) {
	return readFrame(r, progress)
}

func readFrame(r io.Reader, progress func(received, total uint64)) (uint32, []byte, error) {
	body, err := readBody(r, 4, progress)
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
	body, err := readBody(r, 1, nil)
	if err != nil {
		return 0, nil, err
	}
	return body[0], body[1:], nil
}

func WriteInitFrame(w io.Writer, command byte, payload []byte) error {
	return writeBody(w, []byte{command}, payload)
}

func readBody(r io.Reader, header int, progress func(received, total uint64)) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrTruncated
		}
		return nil, err
	}
	if n < uint32(header) {
		return nil, fmt.Errorf("%w: short frame header", ErrMalformed)
	}
	if n > MaxFrameSize {
		return nil, ErrTooLarge
	}
	body := make([]byte, n)
	reader := r
	if progress != nil {
		var received uint64
		total := uint64(n)
		progress(0, total)
		reader = io.TeeReader(r, writerFunc(func(p []byte) (int, error) {
			received += uint64(len(p))
			progress(received, total)
			return len(p), nil
		}))
	}
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, ErrTruncated
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
	if len(data) > MaxFrameSize {
		return nil, ErrTooLarge
	}
	z, e := zlib.NewReader(bytes.NewReader(data))
	if e != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, e)
	}
	defer z.Close()
	lr := io.LimitReader(z, MaxDecompressedSize+1)
	out, e := io.ReadAll(lr)
	if e != nil {
		return nil, e
	}
	if len(out) > MaxDecompressedSize {
		return nil, ErrTooLarge
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
