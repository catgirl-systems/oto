//go:build ignore

// Generates ipv4.bin from a pinned sapics/ip-location-db user-country snapshot.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"time"
)

const (
	sourceURL     = "https://raw.githubusercontent.com/sapics/ip-location-db/de15cb8747b74aacd3202f45f24fab7b16147fac/user-country/user-country-ipv4.csv"
	sourceSHA256  = "2c502804cffec41d87b71541b61fd87bb8805ea5850753f32bdb6800a1e4cda9"
	maxSourceSize = 32 << 20
)

type record struct {
	end  uint32
	code string
}

func main() {
	client := http.Client{Timeout: 2 * time.Minute}
	response, err := client.Get(sourceURL)
	if err != nil {
		fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fatal(fmt.Errorf("download: %s", response.Status))
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxSourceSize+1))
	if err != nil {
		fatal(err)
	}
	if len(data) > maxSourceSize {
		fatal(fmt.Errorf("download exceeds %d bytes", maxSourceSize))
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != sourceSHA256 {
		fatal(fmt.Errorf("source checksum %s, want %s", got, sourceSHA256))
	}

	records, err := parse(bytes.NewReader(data))
	if err != nil {
		fatal(err)
	}
	output := make([]byte, len(records)*6)
	for index, record := range records {
		offset := index * 6
		binary.BigEndian.PutUint32(output[offset:], record.end)
		copy(output[offset+4:], record.code)
	}
	if err := os.WriteFile("ipv4.bin.tmp", output, 0644); err != nil {
		fatal(err)
	}
	if err := os.Rename("ipv4.bin.tmp", "ipv4.bin"); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %d ranges (%d bytes)\n", len(records), len(output))
}

func parse(input io.Reader) ([]record, error) {
	reader := csv.NewReader(input)
	var records []record
	var next uint64
	line := 0
	appendRecord := func(end uint32, code string) {
		if len(records) > 0 && records[len(records)-1].code == code {
			records[len(records)-1].end = end
			return
		}
		records = append(records, record{end: end, code: code})
	}

	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if len(row) != 3 {
			return nil, fmt.Errorf("line %d: expected 3 fields", line)
		}
		start, err := ipv4(row[0])
		if err != nil {
			return nil, fmt.Errorf("line %d start: %w", line, err)
		}
		end, err := ipv4(row[1])
		if err != nil {
			return nil, fmt.Errorf("line %d end: %w", line, err)
		}
		if start > end || uint64(start) < next {
			return nil, fmt.Errorf("line %d: unordered or overlapping range", line)
		}
		code := row[2]
		if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
			return nil, fmt.Errorf("line %d: invalid country code %q", line, code)
		}
		if uint64(start) > next {
			appendRecord(start-1, "")
		}
		if code == "ZZ" {
			code = ""
		}
		appendRecord(end, code)
		next = uint64(end) + 1
	}
	if next <= uint64(^uint32(0)) {
		appendRecord(^uint32(0), "")
	}
	return records, nil
}

func ipv4(value string) (uint32, error) {
	addr, err := netip.ParseAddr(value)
	if err != nil || !addr.Is4() {
		return 0, fmt.Errorf("invalid IPv4 address %q", value)
	}
	bytes := addr.As4()
	return binary.BigEndian.Uint32(bytes[:]), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
