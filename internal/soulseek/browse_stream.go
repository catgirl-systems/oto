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
	"net"
	"strings"
)

// ShareDirectory is one directory in a complete shared-list response.
type ShareDirectory struct {
	Name    string
	Private bool
	Files   []ShareEntry
}

// decodeSharedListDirectories parses the compressed payload without retaining
// the decompressed wire representation.
func decodeSharedListDirectories(data []byte, limits BrowseLimits) ([]ShareDirectory, error) {
	limits = limits.withDefaults()
	if len(data) > limits.MaxCompressedSize {
		return nil, fmt.Errorf("%w: compressed payload has %d bytes (limit %d)", ErrTooLarge, len(data), limits.MaxCompressedSize)
	}
	source := bytes.NewReader(data)
	z, err := zlib.NewReader(source)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	defer z.Close()

	counted := &decompressedLimitReader{r: z, max: uint64(limits.MaxDecompressedSize)}
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
	r   io.Reader
	n   uint64
	max uint64
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
		return n, ErrTooLarge
	}
	return n, err
}

type sharedListStreamDecoder struct {
	r       *bufio.Reader
	counted *decompressedLimitReader
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

func (d *sharedListStreamDecoder) take(n int) ([]byte, error) {
	p, err := d.r.Peek(n)
	if d.counted.n > d.counted.max {
		return nil, fmt.Errorf("%w: decompressed payload exceeds %d bytes", ErrTooLarge, d.counted.max)
	}
	if err != nil {
		return nil, streamDecodeError(err)
	}
	_, _ = d.r.Discard(n)
	return p, nil
}

func (d *sharedListStreamDecoder) U8() (uint8, error) {
	p, err := d.take(1)
	if err != nil {
		return 0, err
	}
	return p[0], nil
}

func (d *sharedListStreamDecoder) U32() (uint32, error) {
	p, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(p), nil
}

func (d *sharedListStreamDecoder) U64() (uint64, error) {
	p, err := d.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(p), nil
}

func (d *sharedListStreamDecoder) String() (string, error) {
	n, err := d.U32()
	if err != nil {
		return "", err
	}
	if n > MaxStringSize {
		return "", fmt.Errorf("%w: string has %d bytes (limit %d)", ErrTooLarge, n, MaxStringSize)
	}
	if n <= uint32(d.r.Size()) {
		p, err := d.take(int(n))
		if err != nil {
			return "", err
		}
		return string(p), nil
	}
	p := make([]byte, n)
	if err := d.readFull(p); err != nil {
		return "", err
	}
	return string(p), nil
}

func (d *sharedListStreamDecoder) optionalU32() (uint32, bool, error) {
	_, err := d.r.Peek(1)
	if err == io.EOF {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, streamDecodeError(err)
	}
	value, err := d.U32()
	return value, true, err
}

func (d *sharedListStreamDecoder) file() (ShareEntry, error) {
	file, err := decodeSearchResult(d)
	return ShareEntry{Name: strings.ReplaceAll(file.Path, "/", "\\"), Size: file.Size, Extension: file.Extension, Bitrate: file.Bitrate, Duration: file.Duration, VBR: file.VBR, VBRKnown: file.VBRKnown, SampleRate: file.SampleRate, BitDepth: file.BitDepth}, err
}

func (d *sharedListStreamDecoder) parse(maxEntries int) ([]ShareDirectory, error) {
	max := uint64(maxEntries)
	total := uint64(0)
	readGroups := func(count uint32, private bool) ([]ShareDirectory, error) {
		if uint64(count) > max-total {
			return nil, fmt.Errorf("%w: share list has at least %d directories (limit %d)", ErrTooLarge, total+uint64(count), max)
		}
		groups := make([]ShareDirectory, 0, int(count))
		for i := uint32(0); i < count; i++ {
			name, err := d.String()
			if err != nil {
				return nil, err
			}
			files, err := d.U32()
			if err != nil {
				return nil, err
			}
			if total >= max || uint64(files) > max-total-1 {
				return nil, fmt.Errorf("%w: share list has at least %d files/directories (limit %d)", ErrTooLarge, total+1+uint64(files), max)
			}
			total++
			groups = append(groups, ShareDirectory{Name: name, Private: private, Files: make([]ShareEntry, 0, int(files))})
			for j := uint32(0); j < files; j++ {
				file, err := d.file()
				if err != nil {
					return nil, err
				}
				file.Private = private
				groups[len(groups)-1].Files = append(groups[len(groups)-1].Files, file)
				total++
			}
		}
		return groups, nil
	}

	publicCount, err := d.U32()
	if err != nil {
		return nil, err
	}
	public, err := readGroups(publicCount, false)
	if err != nil {
		return nil, err
	}
	unknown, present, err := d.optionalU32()
	_ = unknown
	if err != nil {
		return nil, err
	}
	if !present {
		return public, nil
	}
	privateCount, present, err := d.optionalU32()
	if err != nil {
		return nil, err
	}
	if !present {
		return public, nil
	}
	private, err := readGroups(privateCount, true)
	if err != nil {
		return nil, err
	}
	return append(public, private...), nil
}

// browseSharedDirectories fetches and parses one complete shared list.
func (c *Client) browseSharedDirectories(ctx context.Context, peer net.Conn, progress func(uint64, uint64), limits BrowseLimits) ([]ShareDirectory, error) {
	select {
	case c.browseSlot <- struct{}{}:
		defer func() { <-c.browseSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := writeMessage(peer, SharedListRequest{}); err != nil {
		return nil, err
	}
	command, payload, err := readFrameContextProgress(ctx, peer, progress, limits.MaxCompressedSize)
	if err != nil {
		return nil, err
	}
	if command != PeerSharedList {
		return nil, fmt.Errorf("%w: expected shared list", ErrMalformed)
	}
	return decodeSharedListDirectories(payload, limits)
}
