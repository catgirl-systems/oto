package config

import (
	"strings"
	"testing"
)

func TestReceivingValidationAndConsent(t *testing.T) {
	for _, policy := range []Receiving{{Mode: "everyone"}, {Mode: "users", Users: []string{"Alice", "Alice"}}, {Users: []string{"bad\nname"}}, {Users: []string{"bad\u202ename"}}, {Users: []string{strings.Repeat("x", 1025)}}, {Directory: "relative/path"}} {
		if err := policy.Validate(); err == nil {
			t.Fatal("invalid policy accepted", policy)
		}
	}
	for _, mode := range []string{"", "off", "users", "buddies", "trusted"} {
		policy := Receiving{Mode: mode, Users: []string{"Alice"}}
		if err := policy.Validate(); err != nil {
			t.Fatal(err)
		}
		for _, user := range []string{"Alice", "alice"} {
			for _, buddy := range []bool{false, true} {
				for _, trusted := range []bool{false, true} {
					want := (mode == "users" && user == "Alice") || (mode == "buddies" && buddy) || (mode == "trusted" && buddy && trusted)
					if policy.Allows(user, buddy, trusted) != want {
						t.Fatal(mode, user, buddy, trusted)
					}
				}
			}
		}
	}
}
