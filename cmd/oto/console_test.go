package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestConsoleInputErrorsAndEOF(t *testing.T) {
	var out bytes.Buffer
	if err := runConsole(strings.NewReader("\"unfinished\nquit\n"), &out, nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unfinished command quote") {
		t.Fatal(out.String())
	}
	if err := runConsole(strings.NewReader(strings.Repeat("x", 129<<10)), &out, nil, false); err == nil {
		t.Fatal("oversized console command accepted")
	}
	if err := runConsole(strings.NewReader("\nquit\n"), &out, nil, true); err != nil {
		t.Fatal(err)
	}
}
