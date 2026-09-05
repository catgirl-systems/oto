package storage

import (
	"encoding/binary"
	"errors"
)

var ErrInvalidUint64 = errors.New("storage: uint64 value must be exactly 8 bytes")

// EncodeUint64 returns the fixed-width big-endian SQLite BLOB representation.
func EncodeUint64(value uint64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, value)
	return out
}

// DecodeUint64 validates and decodes a fixed-width big-endian SQLite BLOB.
func DecodeUint64(value []byte) (uint64, error) {
	if len(value) != 8 {
		return 0, ErrInvalidUint64
	}
	return binary.BigEndian.Uint64(value), nil
}

// Uint64Bytes is a short alias useful in generated query arguments.
func Uint64Bytes(value uint64) []byte { return EncodeUint64(value) }

// Uint64FromBytes is the matching validating alias for DecodeUint64.
func Uint64FromBytes(value []byte) (uint64, error) { return DecodeUint64(value) }
