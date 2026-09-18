package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestConsoleInputErrorsAndEOF(t *testing.T) {
	var out bytes.Buffer
	must(t, runConsole(strings.NewReader("\"unfinished\nquit\n"), &out, nil, false))
	failIf(t, !strings.Contains(out.String(), "unfinished command quote"), out.String())
	if err := runConsole(strings.NewReader(strings.Repeat("x", 129<<10)), &out, nil, false); err == nil {
		t.Fatal("oversized console command accepted")
	}
	must(t, runConsole(strings.NewReader("\nquit\n"), &out, nil, true))
}
