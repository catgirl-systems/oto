package soulseek

import (
	"bytes"
	"testing"
)

func TestAccountPrivilegeWireFixtures(t *testing.T) {
	for name, request := range map[string]Message{"check-privileges-request": CheckPrivilegesRequest{}, "give-privileges-request": GivePrivilegesRequest{Username: "Alice", Days: 1}} {
		fixture := communityFixture(t, name)
		encoded, err := EncodeMessage(request)
		must(t, err)
		code, payload, err := ReadFrame(bytes.NewReader(encoded))
		failIf(t, err != nil || code != fixture.Code || !bytes.Equal(payload, fixture.Payload(t)), name, code, payload, err)
	}
	for name, want := range map[string]uint32{"privilege-balance": 259260, "privilege-balance-empty": 0} {
		fixture := communityFixture(t, name)
		payload := fixture.Payload(t)
		decoded, err := DecodeServerMessage(fixture.Code, payload)
		if err != nil || decoded != (PrivilegeBalance{Seconds: want}) {
			t.Fatal(name, decoded, err)
		}
		for n := range len(payload) {
			if _, err := DecodePrivilegeBalance(payload[:n]); err == nil {
				t.Fatal("accepted incomplete balance", n)
			}
		}
		if _, err := DecodePrivilegeBalance(append(payload, 0)); err == nil {
			t.Fatal("accepted trailing balance data")
		}
	}
	for _, request := range []GivePrivilegesRequest{{Username: "Alice"}, {Days: 1}} {
		if _, err := EncodeMessage(request); err == nil {
			t.Fatal("accepted invalid gift")
		}
	}
}
