package daemon

import (
	"context"
	"strings"
	"testing"
)

func TestPrivilegeCommandRegistryValidation(t *testing.T) {
	s := downloadService(t)
	for _, req := range []CommandRequest{
		{Name: "unknown"}, {Name: "gift", Args: []string{"Alice"}},
		{Name: "gift", Args: []string{"Alice", "1.5"}},
		{Name: "privileges", Args: []string{"extra"}},
		{Name: strings.Repeat("x", 65)},
		{Name: "gift", Args: []string{strings.Repeat("x", 129<<10), "1"}},
	} {
		if _, err := s.RunCommand(context.Background(), req); err == nil {
			t.Fatal("invalid command accepted", req.Name)
		}
	}
	out, err := s.RunCommand(context.Background(), CommandRequest{Name: "help"})
	if err != nil || len(out.Help) < 2 {
		t.Fatal(out, err)
	}
}
