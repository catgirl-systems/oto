package country

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestLookup(t *testing.T) {
	table := make([]byte, 18)
	binary.BigEndian.PutUint32(table[0:], 9)
	binary.BigEndian.PutUint32(table[6:], 19)
	copy(table[10:], "US")
	binary.BigEndian.PutUint32(table[12:], ^uint32(0))
	copy(table[16:], "CA")

	for input, want := range map[string]string{
		"0.0.0.9":         "",
		"0.0.0.10":        "US",
		"0.0.0.19":        "US",
		"0.0.0.20":        "CA",
		"::ffff:0.0.0.10": "US",
		"2001:db8::1":     "",
	} {
		if got := lookup(string(table), netip.MustParseAddr(input)); got != want {
			t.Errorf("lookup(%s) = %q, want %q", input, got, want)
		}
	}
	if got := lookup(string(table), netip.Addr{}); got != "" {
		t.Fatalf("invalid address lookup = %q", got)
	}
	if got := lookup("bad", netip.MustParseAddr("0.0.0.1")); got != "" {
		t.Fatalf("malformed table lookup = %q", got)
	}
}

func TestEmbeddedTable(t *testing.T) {
	if len(ipv4Data) == 0 || len(ipv4Data)%recordSize != 0 {
		t.Fatalf("invalid embedded table size %d", len(ipv4Data))
	}
	var previous uint32
	for index := 0; index < len(ipv4Data)/recordSize; index++ {
		offset := index * recordSize
		end := uint32(ipv4Data[offset])<<24 | uint32(ipv4Data[offset+1])<<16 | uint32(ipv4Data[offset+2])<<8 | uint32(ipv4Data[offset+3])
		if index > 0 && end <= previous {
			t.Fatalf("range %d ends at %d after %d", index, end, previous)
		}
		code := ipv4Data[offset+4 : offset+6]
		if code != "\x00\x00" && (code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z') {
			t.Fatalf("range %d has invalid code %q", index, code)
		}
		previous = end
	}
	if previous != ^uint32(0) {
		t.Fatalf("table ends at %d", previous)
	}
	if got := Lookup(netip.MustParseAddr("1.0.0.1")); got != "AU" {
		t.Fatalf("known address country = %q, want AU", got)
	}
	addr := netip.MustParseAddr("1.0.0.1")
	if allocations := testing.AllocsPerRun(100, func() { Lookup(addr) }); allocations != 0 {
		t.Fatalf("lookup allocations = %v", allocations)
	}
	for _, input := range []string{"0.0.0.0", "10.0.0.1", "127.0.0.1", "169.254.1.1", "192.168.1.1", "224.0.0.1", "255.255.255.255"} {
		if got := Lookup(netip.MustParseAddr(input)); got != "" {
			t.Errorf("special address %s country = %q", input, got)
		}
	}
}
