package daemon

import (
	"reflect"
	"strings"
	"testing"
)

func TestCommandTextQuotingAndBounds(t *testing.T) {
	for _, test := range []struct {
		text, name string
		args       []string
	}{
		{`/gift "Alice Smith" 1`, "gift", []string{"Alice Smith", "1"}},
		{`message Alice ''`, "message", []string{"Alice", ""}},
		{`message '世界' "a\"b"`, "message", []string{"世界", `a"b`}},
		{"message Alice '$HOME; `id` $(id)'", "message", []string{"Alice", "$HOME; `id` $(id)"}},
	} {
		name, args, err := ParseCommand(test.text)
		if err != nil || name != test.name || !reflect.DeepEqual(args, test.args) {
			t.Fatal(test.text, name, args, err)
		}
	}
	for _, text := range []string{"", `message "unfinished`, `message \`, "message Alice\x1b[31m", strings.Repeat("x", 129<<10), "message " + strings.Repeat("x ", 129)} {
		if _, _, err := ParseCommand(text); err == nil {
			t.Fatal("invalid command accepted")
		}
	}
}
