package storage

import "testing"

// must fails the test immediately when err is non-nil.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// failIf fails the test when cond is true.
func failIf(t *testing.T, cond bool, args ...any) {
	t.Helper()
	if cond {
		t.Fatal(args...)
	}
}

// failIfFmt is failIf with t.Fatalf formatting.
func failIfFmt(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if cond {
		t.Fatalf(format, args...)
	}
}
