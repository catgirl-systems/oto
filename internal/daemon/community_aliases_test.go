package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCommunityAliasesPersistenceAndExpansion(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "aliases.sqlite3")
	s, err := New(cfg, path)
	must(t, err)
	t.Cleanup(func() { s.Close() })
	id := s.community.identity
	create := func(name, expansion string) CommunityAlias {
		t.Helper()
		out, err := s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: id, Name: name, Expansion: expansion})
		must(t, err)
		return out
	}
	alias := create("donate", `gift "$1" "$2"`)
	req := CommandRequest{CommunityIdentity: id, Name: "donate", Args: []string{"Alice Smith", "1"}, RequestID: "one"}
	expanded, err := s.expandCommandAliases(ctx, req)
	failIf(t, err != nil || expanded.Name != "gift" || !reflect.DeepEqual(expanded.Args, req.Args) || expanded.RequestID != "one", expanded, err)
	req.Confirm = true
	if _, err := s.expandCommandAliases(ctx, req); err == nil {
		t.Fatal("mutable alias accepted as confirmation")
	}
	req.Confirm = false
	create("guide", "help")
	if out, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "guide"}); err != nil || len(out.Help) == 0 {
		t.Fatal(out, err)
	}
	create("loop-a", "loop-b")
	create("loop-b", "loop-a")
	if _, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "loop-a"}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatal("cycle", err)
	}
	for i := 8; i >= 0; i-- {
		target := "help"
		if i < 8 {
			target = strings.Repeat("a", i+2)
		}
		create(strings.Repeat("a", i+1), target)
	}
	if _, err := s.RunCommand(ctx, CommandRequest{CommunityIdentity: id, Name: "a"}); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatal("depth", err)
	}
	if _, err := s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: id, Name: "invalid", Expansion: "gift $HOME 1"}); err == nil {
		t.Fatal("invalid parameter persisted")
	}
	for _, name := range []string{"help", "gift", "../bad", "Bad"} {
		if _, err := s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: id, Name: name, Expansion: "help"}); err == nil {
			t.Fatal(name)
		}
	}
	if _, err := s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: id, Name: "donate", Expansion: "help"}); !errors.Is(err, ErrCommunityMessageState) {
		t.Fatal("unfenced update", err)
	}
	if _, err := s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: id, Name: "donate", Revision: alias.Revision, Remove: true}); err == nil {
		t.Fatal("unconfirmed removal")
	}
	page, err := s.CommunityAliases(ctx, CommunityAliasesRequest{CommunityIdentity: id, Limit: 1})
	failIf(t, err != nil || len(page.Aliases) != 1 || page.NextCursor == "", page, err)
	must(t, s.Close())
	s, err = New(cfg, path)
	must(t, err)
	if _, err := s.expandCommandAliases(ctx, req); !errors.Is(err, ErrCommunitySession) {
		t.Fatal("old daemon accepted", err)
	}
	id = s.community.identity
	req.CommunityIdentity = id
	if _, err := s.expandCommandAliases(ctx, req); err != nil {
		t.Fatal("alias lost on restart", err)
	}
	if _, err := s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: id, Name: "donate", Revision: alias.Revision, Remove: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.expandCommandAliases(ctx, req); err == nil {
		t.Fatal("deleted alias resolved")
	}
}
func TestAliasArgumentsStayDataAndBounded(t *testing.T) {
	args := []string{`Alice" ; gift Mallory 1`, "世界"}
	out, err := expandAliasArgument("$1 / $* / $$", args)
	failIf(t, err != nil || out != args[0]+" / "+strings.Join(args, " ")+" / $", out, err)
	for _, template := range []string{"$", "$0", "$3", "$HOME"} {
		if _, err := expandAliasArgument(template, args); err == nil {
			t.Fatal(template)
		}
	}
	if _, err := expandAliasArgument("$*$*", []string{strings.Repeat("x", 128<<10)}); err == nil {
		t.Fatal("unbounded expansion")
	}
}
