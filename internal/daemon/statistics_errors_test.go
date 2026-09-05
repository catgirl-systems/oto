package daemon

import "testing"

func TestStatisticsErrorPrivacy(t *testing.T) {
	for input, want := range map[string]string{
		"dial tcp 203.0.113.1:50300: refused":               "dial tcp [address]: refused",
		"read tcp 127.0.0.1:20->[2001:db8::1]:50300: reset": "read tcp [address]->[address]: reset",
		"lookup 192.0.2.1: timeout":                         "lookup [address]: timeout",
		"[fe80::1%eth0]:50300: timeout":                     "[address]: timeout",
		"2001:db8::1 unreachable":                           "[address] unreachable",
		"timed out at 12:30; file 1.2.3 is missing":         "timed out at 12:30; file 1.2.3 is missing",
	} {
		if got := statsError(input); got != want {
			t.Errorf("%q => %q, want %q", input, got, want)
		}
	}
}
