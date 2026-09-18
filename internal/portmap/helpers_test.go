package portmap

import "testing"

// must fails the test immediately when err is non-nil.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// failIfFmt fails the test when cond is true, formatting like t.Fatalf.
func failIfFmt(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if cond {
		t.Fatalf(format, args...)
	}
}
