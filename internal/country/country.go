package country

import (
	_ "embed"
	"net/netip"
	"sort"
)

const recordSize = 6

//go:generate go run generate.go

// Each record is an inclusive IPv4 range end followed by a two-letter code.
//
//go:embed ipv4.bin
var ipv4Data string

// Lookup returns the approximate country code for an IPv4 address.
func Lookup(addr netip.Addr) string {
	return lookup(ipv4Data, addr)
}

func lookup(table string, addr netip.Addr) string {
	addr = addr.Unmap()
	if !addr.Is4() || len(table)%recordSize != 0 {
		return ""
	}

	bytes := addr.As4()
	value := uint32(bytes[0])<<24 | uint32(bytes[1])<<16 | uint32(bytes[2])<<8 | uint32(bytes[3])
	records := len(table) / recordSize
	index := sort.Search(records, func(index int) bool {
		offset := index * recordSize
		end := uint32(table[offset])<<24 | uint32(table[offset+1])<<16 | uint32(table[offset+2])<<8 | uint32(table[offset+3])
		return end >= value
	})
	if index == records {
		return ""
	}

	offset := index*recordSize + 4
	if table[offset] == 0 {
		return ""
	}
	return table[offset : offset+2]
}
