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

func DecodePrivilegedUsers(payload []byte) (m PrivilegedUsers, err error) {
	d := NewDecoder(payload)
	n, err := decodeRoomCount(d, MaxPrivilegedUsers, 4)
	if err != nil {
		return m, err
	}
	m.Users = make([]string, n)
	for i := range m.Users {
		if m.Users[i], err = decodeUsername(d); err != nil {
			return m, err
		}
	}
	return m, d.Done()
}
