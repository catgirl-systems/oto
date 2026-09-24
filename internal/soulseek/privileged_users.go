package soulseek

const (
	ServerPrivilegedUsers uint32 = 69
	MaxPrivilegedUsers           = 100000 // Local memory budget, not a claimed server limit.
)

// The server can send the privileged-user roster in additive chunks. Presence
// and connection instructions subsequently update individual privilege flags.
type PrivilegedUsers struct{ Users []string }

func (PrivilegedUsers) socialMessage()        {}
func (ConnectPeerInstruction) socialMessage() {}

func DecodePrivilegedUsers(payload []byte) (PrivilegedUsers, error) {
	d := NewDecoder(payload)
	m := PrivilegedUsers{Users: make([]string, decodeRoomCount(d, MaxPrivilegedUsers, 4))}
	for i := range m.Users {
		m.Users[i] = decodeUsername(d)
	}
	return m, d.Done()
}
