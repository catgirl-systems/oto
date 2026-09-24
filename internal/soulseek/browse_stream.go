package soulseek

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"
)

// ShareDirectory is one directory in a complete shared-list response.
type ShareDirectory struct {
	Name    string
	Private bool
	Files   []ShareEntry
}

// decodeSharedListDirectories parses the compressed payload without retaining
// the decompressed wire representation.
func decodeSharedListDirectories(data []byte, limits BrowseLimits, loggers ...*slog.Logger) ([]ShareDirectory, error) {
	limits = limits.withDefaults()
	if len(data) > limits.MaxCompressedSize {
		logLimit(firstLogger(loggers), "compressed_bytes", uint64(limits.MaxCompressedSize), uint64(len(data)))
		return nil, fmt.Errorf("%w: compressed payload has %d bytes (limit %d)", ErrTooLarge, len(data), limits.MaxCompressedSize)
	}
	source := bytes.NewReader(data)
	z, err := zlib.NewReader(source)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	defer z.Close()

	counted := &decompressedLimitReader{r: z, max: uint64(limits.MaxDecompressedSize), logger: firstLogger(loggers)}
	d := &sharedListStreamDecoder{r: bufio.NewReaderSize(counted, 32<<10), counted: counted}
	result, err := d.parse(limits.MaxEntries)
	if err != nil {
		return nil, err
	}

	// A final read both checks the zlib checksum and rejects decompressed
	// trailing bytes. The compressed reader must also end exactly at the zlib
	// stream boundary.
	if _, err := d.r.ReadByte(); err == nil {
		if counted.n > counted.max {
			return nil, fmt.Errorf("%w: decompressed payload exceeds %d bytes", ErrTooLarge, counted.max)
		}
		return nil, fmt.Errorf("%w: trailing decompressed bytes", ErrMalformed)
	} else if errors.Is(err, ErrTooLarge) {
		return nil, err
	} else if err != io.EOF {
		return nil, streamDecodeError(err)
	}
	if source.Len() != 0 {
		return nil, fmt.Errorf("%w: trailing compressed bytes", ErrMalformed)
	}
	if err := z.Close(); err != nil {
		return nil, streamDecodeError(err)
	}
	return result, nil
}

type decompressedLimitReader struct {
	r      io.Reader
	n      uint64
	max    uint64
	logger *slog.Logger
}

func (r *decompressedLimitReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.n > r.max {
		return 0, ErrTooLarge
	}
	remaining := r.max + 1 - r.n
	if uint64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.r.Read(p)
	r.n += uint64(n)
	if r.n > r.max {
		logLimit(r.logger, "decompressed_bytes", r.max, r.n)
		return n, ErrTooLarge
	}
	return n, err
}

type sharedListStreamDecoder struct {
	r       *bufio.Reader
	counted *decompressedLimitReader
	err     error
}

func streamDecodeError(err error) error {
	if errors.Is(err, ErrTooLarge) {
		return err
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrTruncated
	}
	return fmt.Errorf("%w: %v", ErrMalformed, err)
}

func (d *sharedListStreamDecoder) fail(err error) {
	if d.err == nil {
		d.err = err
	}
}

func (d *sharedListStreamDecoder) readFull(p []byte) error {
	_, err := io.ReadFull(d.r, p)
	if d.counted.n > d.counted.max {
		return fmt.Errorf("%w: decompressed payload exceeds %d bytes", ErrTooLarge, d.counted.max)
	}
	if err != nil {
		return streamDecodeError(err)
	}
	return nil
}

func (d *sharedListStreamDecoder) take(n int) []byte {
	p, err := d.r.Peek(n)
	if d.counted.n > d.counted.max {
		d.fail(fmt.Errorf("%w: decompressed payload exceeds %d bytes", ErrTooLarge, d.counted.max))
		return nil
	}
	if err != nil {
		d.fail(streamDecodeError(err))
		return nil
	}
	_, _ = d.r.Discard(n)
	return p
}

func (d *sharedListStreamDecoder) U8() uint8 {
	if p := d.take(1); d.err == nil {
		return p[0]
	}
	return 0
}

func (d *sharedListStreamDecoder) U32() uint32 {
	if p := d.take(4); d.err == nil {
		return binary.LittleEndian.Uint32(p)
	}
	return 0
}

func (d *sharedListStreamDecoder) U64() uint64 {
	if p := d.take(8); d.err == nil {
		return binary.LittleEndian.Uint64(p)
	}
	return 0
}

func (d *sharedListStreamDecoder) String() string {
	n := d.U32()
	if d.err != nil {
		return ""
	}
	if n > MaxStringSize {
		d.fail(fmt.Errorf("%w: string has %d bytes (limit %d)", ErrTooLarge, n, MaxStringSize))
		return ""
	}
	if n <= uint32(d.r.Size()) {
		return string(d.take(int(n)))
	}
	p := make([]byte, n)
	if err := d.readFull(p); err != nil {
		d.fail(err)
		return ""
	}
	return string(p)
}

func (d *sharedListStreamDecoder) optionalU32() (uint32, bool) {
	_, err := d.r.Peek(1)
	if err == nil {
		return d.U32(), true
	}
	if errors.Is(err, io.EOF) {
		return 0, false
	}
	d.fail(streamDecodeError(err))
	return 0, false
}

func (d *sharedListStreamDecoder) file() ShareEntry {
	file := decodeSearchResult(d)
	return ShareEntry{Name: strings.ReplaceAll(file.Path, "/", "\\"), Size: file.Size, Extension: file.Extension, Bitrate: file.Bitrate, Duration: file.Duration, VBR: file.VBR, VBRKnown: file.VBRKnown, SampleRate: file.SampleRate, BitDepth: file.BitDepth}
}

func (d *sharedListStreamDecoder) parse(maxEntries int) ([]ShareDirectory, error) {
	max := uint64(maxEntries)
	total := uint64(0)
	readGroups := func(count uint32, private bool) []ShareDirectory {
		if uint64(count) > max-total {
			logLimit(d.counted.logger, "entries", max, total+uint64(count))
			d.fail(fmt.Errorf("%w: share list has at least %d directories (limit %d)", ErrTooLarge, total+uint64(count), max))
			return nil
		}
		groups := make([]ShareDirectory, 0, int(count))
		for i := uint32(0); i < count; i++ {
			name := d.String()
			files := d.U32()
			if d.err != nil {
				return nil
			}
			if total >= max || uint64(files) > max-total-1 {
				logLimit(d.counted.logger, "entries", max, total+1+uint64(files))
				d.fail(fmt.Errorf("%w: share list has at least %d files/directories (limit %d)", ErrTooLarge, total+1+uint64(files), max))
				return nil
			}
			total++
			groups = append(groups, ShareDirectory{Name: name, Private: private, Files: make([]ShareEntry, 0, int(files))})
			for j := uint32(0); j < files; j++ {
				file := d.file()
				file.Private = private
				groups[len(groups)-1].Files = append(groups[len(groups)-1].Files, file)
				total++
			}
		}
		return groups
	}

	publicCount := d.U32()
	public := readGroups(publicCount, false)
	if _, present := d.optionalU32(); !present {
		return public, d.err
	}
	privateCount, present := d.optionalU32()
	if !present {
		return public, d.err
	}
	private := readGroups(privateCount, true)
	if d.err != nil {
		return nil, d.err
	}
	return append(public, private...), nil
}

// browseSharedDirectories fetches and parses one complete shared list.
func (c *Client) browseSharedDirectories(ctx context.Context, peer net.Conn, progress func(uint64, uint64), limits BrowseLimits) ([]ShareDirectory, error) {
	ctx, operationID := c.browseContext(ctx)
	ctx, peer = c.browsePeer(ctx, peer)
	configurePeerRead(peer, progress, limits.MaxCompressedSize)
	c.log(ctx, slog.LevelInfo, "browse_operation_start", nil, slog.String("operation_id", operationID), slog.String("browse_type", "group"))
	select {
	case c.browseSlot <- struct{}{}:
		defer func() { <-c.browseSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := writeMessage(peer, SharedListRequest{}); err != nil {
		c.log(ctx, slog.LevelError, "browse_request_failed", err, slog.String("operation_id", operationID), slog.String("stage", "request"))
		return nil, err
	}
	c.log(ctx, slog.LevelDebug, "browse_request_sent", nil, slog.String("operation_id", operationID), slog.Uint64("command", uint64(PeerGetSharedList)))
	command, payload, err := readFrameContextProgress(ctx, peer, progress, limits.MaxCompressedSize)
	if err != nil {
		c.log(ctx, slog.LevelError, "browse_read_failed", err, slog.String("operation_id", operationID), slog.String("stage", "response"))
		return nil, err
	}
	c.log(ctx, slog.LevelDebug, "browse_response_received", nil, slog.String("operation_id", operationID), slog.Uint64("command", uint64(command)), slog.Uint64("declared_size", uint64(len(payload))))
	if command != PeerSharedList {
		return nil, fmt.Errorf("%w: expected shared list", ErrMalformed)
	}
	started := time.Now()
	result, err := decodeSharedListDirectories(payload, limits, c.logger(ctx))
	if err != nil {
		c.log(ctx, slog.LevelError, "browse_decode_failed", err, slog.String("operation_id", operationID), slog.String("stage", "body"))
	} else if c.logger(ctx).Enabled(ctx, slog.LevelInfo) {
		elapsed := time.Since(started)
		entries := len(result)
		for _, directory := range result {
			entries += len(directory.Files)
		}
		c.log(ctx, slog.LevelInfo, "browse_decode_complete", nil, slog.String("operation_id", operationID), slog.Duration("decode_elapsed", elapsed), slog.Int("directories", len(result)), slog.Int("entries", entries), slog.Int("compressed_bytes", len(payload)))
	}
	return result, err
}
