package main

import "testing"

func TestPrivilegeCommandRequiresCompletePreviewIdentity(t *testing.T) {
	for _, args := range [][]string{
		{"--confirm", "gift", "Alice", "1"},
		{"--confirm", "--request-id", "one", "--revision", "2", "--account", "test", "--daemon", "old", "--session", "invalid", "gift", "Alice", "1"},
		{"--confirm", "--request-id", "one", "--revision", "2", "gift", "Alice", "1"},
	} {
		if err := socialCommand(args); err == nil {
			t.Fatal("unsafe confirmation accepted", args)
		}
	}
}
