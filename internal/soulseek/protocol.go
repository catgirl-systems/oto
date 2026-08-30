package soulseek

import (
	"bytes"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
)

// Soulseek command identifiers. The wire command byte follows the frame length.
const (
	ServerLogin            uint32 = 1
	ServerSetListenPort    uint32 = 2
	ServerGetPeerAddress   uint32 = 3
	ServerAddUser          uint32 = 5
	ServerConnectToPeer    uint32 = 18
	ServerFileSearch       uint32 = 26
	ServerSetStatus        uint32 = 28
	ServerPing             uint32 = 32
	ServerSharedCounts     uint32 = 35
	ServerHaveNoParent     uint32 = 71
	ServerEmbeddedMessage  uint32 = 93
	ServerAcceptChildren   uint32 = 100
	ServerPossibleParents  uint32 = 102
	ServerBranchLevel      uint32 = 126
	ServerBranchRoot       uint32 = 127
	ServerResetDistributed uint32 = 130

	PeerInit                uint32 = 1
	PeerSearch              uint32 = 9
	PeerGetSharedList       uint32 = 4
	PeerSharedList          uint32 = 5
	PeerFolderContents      uint32 = 36
	PeerFolderResponse      uint32 = 37
	PeerTransferRequest     uint32 = 40
	PeerTransferResponse    uint32 = 41
	PeerQueueUpload         uint32 = 43
	PeerPlaceInQueue        uint32 = 44
	PeerUploadFailed        uint32 = 46
	PeerUploadDenied        uint32 = 50
	PeerPlaceInQueueRequest uint32 = 51
	PeerPierceFirewall      byte   = 0

	DistributedSearchCommand      byte = 3
	DistributedBranchLevelCommand byte = 4
	DistributedBranchRootCommand  byte = 5
)

// Message is a command payload that can be encoded on the wire.
type Message interface {
	command() uint32
	encode(*Encoder) error
}

type LoginRequest struct {
	Username, Password    string
	Version, MinorVersion uint32
	Hash                  string
}

func (LoginRequest) command() uint32 { return ServerLogin }
func (m LoginRequest) encode(e *Encoder) error {
	if err := e.String(m.Username); err != nil {
		return err
	}
	if err := e.String(m.Password); err != nil {
		return err
	}
	e.U32(m.Version)
	if err := e.String(m.Hash); err != nil {
		return err
	}
	e.U32(m.MinorVersion)
	return nil
}
func DecodeLoginRequest(b []byte) (LoginRequest, error) {
	d := NewDecoder(b)
	var m LoginRequest
	var e error
	if m.Username, e = d.String(); e != nil {
		return m, e
	}
	if m.Password, e = d.String(); e != nil {
		return m, e
	}
	if m.Version, e = d.U32(); e != nil {
		return m, e
	}
	if m.Hash, e = d.String(); e != nil {
		return m, e
	}
	if m.MinorVersion, e = d.U32(); e != nil {
		return m, e
	}
	return m, d.Done()
}

type LoginResponse struct {
	Success   bool
	Message   string
	Detail    string
	IP        uint32
	Hash      string
	Supporter bool
}

func (LoginResponse) command() uint32 { return ServerLogin }
func (m LoginResponse) encode(e *Encoder) error {
	e.Bool(m.Success)
	if err := e.String(m.Message); err != nil {
		return err
	}
	if !m.Success {
		if m.Detail != "" {
			return e.String(m.Detail)
		}
		return nil
	}
	e.U32(m.IP)
	if err := e.String(m.Hash); err != nil {
		return err
	}
	e.Bool(m.Supporter)
	return nil
}
func DecodeLoginResponse(b []byte) (LoginResponse, error) {
	d := NewDecoder(b)
	var m LoginResponse
	var err error
	if m.Success, err = d.Bool(); err != nil {
		return m, err
	}
	if m.Message, err = d.String(); err != nil {
		return m, err
	}
	if !m.Success {
		if d.Remaining() > 0 {
			m.Detail, err = d.String()
		}
		if err != nil {
			return m, err
		}
		return m, d.Done()
	}
	if m.IP, err = d.U32(); err != nil {
		return m, err
	}
	if m.Hash, err = d.String(); err != nil {
		return m, err
	}
	if m.Supporter, err = d.Bool(); err != nil {
		return m, err
	}
	return m, d.Done()
}

type Ping struct{}

func (Ping) command() uint32       { return ServerPing }
func (Ping) encode(*Encoder) error { return nil }

type ListenPort struct{ Port uint32 }

func (ListenPort) command() uint32           { return ServerSetListenPort }
func (m ListenPort) encode(e *Encoder) error { e.U32(m.Port); return nil }

type Status struct{ Status uint32 }

func (Status) command() uint32           { return ServerSetStatus }
func (m Status) encode(e *Encoder) error { e.U32(m.Status); return nil }

type SharedCounts struct{ Folders, Files uint32 }

func (SharedCounts) command() uint32           { return ServerSharedCounts }
func (m SharedCounts) encode(e *Encoder) error { e.U32(m.Folders); e.U32(m.Files); return nil }

type HaveNoParent struct{ Value bool }

func (HaveNoParent) command() uint32           { return ServerHaveNoParent }
func (m HaveNoParent) encode(e *Encoder) error { e.Bool(m.Value); return nil }

type AcceptChildren struct{ Value bool }

func (AcceptChildren) command() uint32           { return ServerAcceptChildren }
func (m AcceptChildren) encode(e *Encoder) error { e.Bool(m.Value); return nil }

type BranchLevel struct{ Level uint32 }

func (BranchLevel) command() uint32           { return ServerBranchLevel }
func (m BranchLevel) encode(e *Encoder) error { e.U32(m.Level); return nil }

type BranchRoot struct{ Username string }

func (BranchRoot) command() uint32           { return ServerBranchRoot }
func (m BranchRoot) encode(e *Encoder) error { return e.String(m.Username) }

type ParentCandidate struct {
	Username, IP string
	Port         uint32
}
type PossibleParents struct{ Parents []ParentCandidate }

func DecodePossibleParents(b []byte) (PossibleParents, error) {
	d := NewDecoder(b)
	var message PossibleParents
	count, err := d.U32()
	if err != nil {
		return message, err
	}
	if count > 10 {
		return message, ErrTooLarge
	}
	for i := uint32(0); i < count; i++ {
		var parent ParentCandidate
		if parent.Username, err = d.String(); err != nil {
			return message, err
		}
		ip, err := d.U32()
		if err != nil {
			return message, err
		}
		parent.IP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip)).String()
		if parent.Port, err = d.U32(); err != nil {
			return message, err
		}
		message.Parents = append(message.Parents, parent)
	}
	return message, d.Done()
}

type EmbeddedDistributed struct {
	Command byte
	Payload []byte
}

func DecodeEmbeddedDistributed(b []byte) (EmbeddedDistributed, error) {
	d := NewDecoder(b)
	var message EmbeddedDistributed
	var err error
	if message.Command, err = d.U8(); err != nil {
		return message, err
	}
	if message.Payload, err = d.Bytes(); err != nil {
		return message, err
	}
	message.Payload = append([]byte(nil), message.Payload...)
	return message, d.Done()
}

type PeerAddressRequest struct{ Username string }

func (PeerAddressRequest) command() uint32           { return ServerGetPeerAddress }
func (m PeerAddressRequest) encode(e *Encoder) error { return e.String(m.Username) }

type PeerAddress struct {
	Username       string
	IP             string
	Port           uint32
	Obfuscation    uint32
	ObfuscatedPort uint16
}

func (PeerAddress) command() uint32 { return ServerGetPeerAddress }
func (m PeerAddress) encode(e *Encoder) error {
	if err := e.String(m.Username); err != nil {
		return err
	}
	ip := net.ParseIP(m.IP).To4()
	if ip == nil {
		return fmt.Errorf("%w: invalid IPv4 address", ErrMalformed)
	}
	e.U32(uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3]))
	e.U32(m.Port)
	e.U32(m.Obfuscation)
	e.U16(m.ObfuscatedPort)
	return nil
}

func DecodePeerAddress(b []byte) (PeerAddress, error) {
	d := NewDecoder(b)
	var message PeerAddress
	var err error
	if message.Username, err = d.String(); err != nil {
		return message, err
	}
	ip, err := d.U32()
	if err != nil {
		return message, err
	}
	message.IP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip)).String()
	if message.Port, err = d.U32(); err != nil {
		return message, err
	}
	if d.Remaining() > 0 {
		if message.Obfuscation, err = d.U32(); err != nil {
			return message, err
		}
		if message.ObfuscatedPort, err = d.U16(); err != nil {
			return message, err
		}
	}
	return message, d.Done()
}

type ConnectPeer struct {
	Token    uint32
	Username string
	Kind     string
}

func (ConnectPeer) command() uint32 { return ServerConnectToPeer }
func (m ConnectPeer) encode(e *Encoder) error {
	e.U32(m.Token)
	if err := e.String(m.Username); err != nil {
		return err
	}
	return e.String(m.Kind)
}

type ConnectPeerInstruction struct {
	Username       string
	Kind           string
	IP             string
	Port           uint32
	Token          uint32
	Privileged     bool
	Obfuscation    uint32
	ObfuscatedPort uint32
}

func DecodeConnectPeerInstruction(b []byte) (ConnectPeerInstruction, error) {
	d := NewDecoder(b)
	var message ConnectPeerInstruction
	var err error
	if message.Username, err = d.String(); err != nil {
		return message, err
	}
	if message.Kind, err = d.String(); err != nil {
		return message, err
	}
	ip, err := d.U32()
	if err != nil {
		return message, err
	}
	message.IP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip)).String()
	if message.Port, err = d.U32(); err != nil {
		return message, err
	}
	if message.Token, err = d.U32(); err != nil {
		return message, err
	}
	if message.Privileged, err = d.Bool(); err != nil {
		return message, err
	}
	if d.Remaining() > 0 {
		if message.Obfuscation, err = d.U32(); err != nil {
			return message, err
		}
		if message.ObfuscatedPort, err = d.U32(); err != nil {
			return message, err
		}
	}
	return message, d.Done()
}

type SearchRequest struct {
	Token uint32
	Query string
}

func (SearchRequest) command() uint32           { return ServerFileSearch }
func (m SearchRequest) encode(e *Encoder) error { e.U32(m.Token); return e.String(m.Query) }

type IncomingSearch struct {
	Username string
	Token    uint32
	Query    string
}

func DecodeIncomingSearch(b []byte) (IncomingSearch, error) {
	d := NewDecoder(b)
	var message IncomingSearch
	var err error
	if message.Username, err = d.String(); err != nil {
		return message, err
	}
	if message.Token, err = d.U32(); err != nil {
		return message, err
	}
	if message.Query, err = d.String(); err != nil {
		return message, err
	}
	return message, d.Done()
}

type SearchResponse struct {
	Token       uint32
	Username    string
	Results     []SearchResult
	SlotFree    bool
	Speed       uint32
	QueueLength uint32
}

func (SearchResponse) command() uint32 { return PeerSearch }
func (m SearchResponse) encode(e *Encoder) error {
	if len(m.Results) > 500 {
		return ErrTooLarge
	}
	var raw Encoder
	if err := raw.String(m.Username); err != nil {
		return err
	}
	raw.U32(m.Token)
	raw.U32(uint32(len(m.Results)))
	for _, result := range m.Results {
		if err := result.encode(&raw); err != nil {
			return err
		}
	}
	raw.Bool(m.SlotFree)
	raw.U32(m.Speed)
	raw.U32(m.QueueLength)
	raw.U32(0)
	raw.U32(0) // no private results in the MVP
	compressed, err := CompressZlib(raw.Payload())
	if err != nil {
		return err
	}
	e.Raw(compressed)
	return nil
}

type SearchResult struct {
	Username    string `json:"username,omitempty"`
	Path        string `json:"path"`
	Extension   string `json:"extension,omitempty"`
	Size        uint64 `json:"size"`
	IsDirectory bool   `json:"directory,omitempty"`
	SlotFree    bool   `json:"slot_free,omitempty"`
	Speed       uint32 `json:"speed,omitempty"`
	QueueLength uint32 `json:"queue_length,omitempty"`
}

func (r SearchResult) encode(e *Encoder) error {
	e.U8(1)
	if err := e.String(r.Path); err != nil {
		return err
	}
	e.U64(r.Size)
	if err := e.String(r.Extension); err != nil {
		return err
	}
	e.U32(0) // media attributes are intentionally deferred
	return nil
}

func decodeSearchResult(d *Decoder) (SearchResult, error) {
	var result SearchResult
	code, err := d.U8()
	if err != nil {
		return result, err
	}
	if code != 1 {
		return result, fmt.Errorf("%w: file code %d", ErrMalformed, code)
	}
	if result.Path, err = d.String(); err != nil {
		return result, err
	}
	if result.Size, err = d.U64(); err != nil {
		return result, err
	}
	if result.Extension, err = d.String(); err != nil {
		return result, err
	}
	count, err := d.U32()
	if err != nil {
		return result, err
	}
	if count > 64 {
		return result, ErrTooLarge
	}
	for i := uint32(0); i < count; i++ {
		if _, err = d.U32(); err != nil {
			return result, err
		}
		if _, err = d.U32(); err != nil {
			return result, err
		}
	}
	return result, nil
}

func DecodeSearchResponse(b []byte) (SearchResponse, error) {
	var message SearchResponse
	raw, err := DecompressZlib(b)
	if err != nil {
		return message, err
	}
	d := NewDecoder(raw)
	if message.Username, err = d.String(); err != nil {
		return message, err
	}
	if message.Token, err = d.U32(); err != nil {
		return message, err
	}
	count, err := d.U32()
	if err != nil {
		return message, err
	}
	if count > 500 {
		return message, ErrTooLarge
	}
	message.Results = make([]SearchResult, 0, count)
	for i := uint32(0); i < count; i++ {
		result, err := decodeSearchResult(d)
		if err != nil {
			return message, err
		}
		result.Username = message.Username
		message.Results = append(message.Results, result)
	}
	if message.SlotFree, err = d.Bool(); err != nil {
		return message, err
	}
	if message.Speed, err = d.U32(); err != nil {
		return message, err
	}
	if message.QueueLength, err = d.U32(); err != nil {
		return message, err
	}
	for i := range message.Results {
		message.Results[i].SlotFree, message.Results[i].Speed, message.Results[i].QueueLength = message.SlotFree, message.Speed, message.QueueLength
	}
	if _, err = d.U32(); err != nil {
		return message, err
	}
	privateCount, err := d.U32()
	if err != nil {
		return message, err
	}
	if privateCount > 500 {
		return message, ErrTooLarge
	}
	for i := uint32(0); i < privateCount; i++ {
		if _, err = decodeSearchResult(d); err != nil {
			return message, err
		}
	}
	return message, d.Done()
}

type PeerInitMessage struct {
	Username string
	Type     string
	Token    uint32
}

// FileSearchRequest is a peer-side search query carrying the originating token.
type FileSearchRequest struct {
	Token uint32
	Query string
}

func (FileSearchRequest) command() uint32           { return PeerSearch }
func (m FileSearchRequest) encode(e *Encoder) error { e.U32(m.Token); return e.String(m.Query) }
func DecodeFileSearchRequest(b []byte) (FileSearchRequest, error) {
	d := NewDecoder(b)
	var m FileSearchRequest
	var err error
	if m.Token, err = d.U32(); err != nil {
		return m, err
	}
	if m.Query, err = d.String(); err != nil {
		return m, err
	}
	return m, d.Done()
}

func (PeerInitMessage) command() uint32 { return PeerInit }
func (m PeerInitMessage) encode(e *Encoder) error {
	if err := e.String(m.Username); err != nil {
		return err
	}
	if err := e.String(m.Type); err != nil {
		return err
	}
	e.U32(m.Token)
	return nil
}

type SharedListRequest struct{}

func (SharedListRequest) command() uint32       { return PeerGetSharedList }
func (SharedListRequest) encode(*Encoder) error { return nil }

type SharedListResponse struct{ Entries []ShareEntry }

func (SharedListResponse) command() uint32 { return PeerSharedList }
func (m SharedListResponse) encode(e *Encoder) error {
	groups := make(map[string][]ShareEntry)
	for _, entry := range m.Entries {
		if entry.Directory {
			continue
		}
		name := strings.ReplaceAll(entry.Name, "/", "\\")
		dir, file := "", name
		if cut := strings.LastIndexByte(name, '\\'); cut >= 0 {
			dir, file = name[:cut], name[cut+1:]
		}
		groups[dir] = append(groups[dir], ShareEntry{Name: file, Size: entry.Size})
	}
	if len(groups) > 50000 {
		return ErrTooLarge
	}
	dirs := make([]string, 0, len(groups))
	for dir := range groups {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	var raw Encoder
	raw.U32(uint32(len(dirs)))
	for _, dir := range dirs {
		if err := raw.String(dir); err != nil {
			return err
		}
		raw.U32(uint32(len(groups[dir])))
		for _, file := range groups[dir] {
			result := SearchResult{Path: file.Name, Size: file.Size, Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(file.Name)), ".")}
			if err := result.encode(&raw); err != nil {
				return err
			}
		}
	}
	raw.U32(0)
	raw.U32(0)
	compressed, err := CompressZlib(raw.Payload())
	if err != nil {
		return err
	}
	e.Raw(compressed)
	return nil
}

func DecodeSharedListResponse(b []byte) (SharedListResponse, error) {
	var message SharedListResponse
	raw, err := DecompressZlib(b)
	if err != nil {
		return message, err
	}
	d := NewDecoder(raw)
	count, err := d.U32()
	if err != nil {
		return message, err
	}
	if count > 50000 {
		return message, ErrTooLarge
	}
	for i := uint32(0); i < count; i++ {
		dir, err := d.String()
		if err != nil {
			return message, err
		}
		message.Entries = append(message.Entries, ShareEntry{Name: dir, Directory: true})
		files, err := d.U32()
		if err != nil {
			return message, err
		}
		if files > 50000 || len(message.Entries)+int(files) > 50000 {
			return message, ErrTooLarge
		}
		for j := uint32(0); j < files; j++ {
			file, err := decodeSearchResult(d)
			if err != nil {
				return message, err
			}
			message.Entries = append(message.Entries, ShareEntry{Name: strings.TrimPrefix(dir+"\\"+file.Path, "\\"), Size: file.Size})
		}
	}
	if _, err = d.U32(); err != nil {
		return message, err
	}
	private, err := d.U32()
	if err != nil {
		return message, err
	}
	if private > 50000 {
		return message, ErrTooLarge
	}
	for i := uint32(0); i < private; i++ {
		if _, err = d.String(); err != nil {
			return message, err
		}
		count, err := d.U32()
		if err != nil {
			return message, err
		}
		if count > 50000 {
			return message, ErrTooLarge
		}
		for j := uint32(0); j < count; j++ {
			if _, err = decodeSearchResult(d); err != nil {
				return message, err
			}
		}
	}
	return message, d.Done()
}

type ShareEntry struct {
	Name      string
	Size      uint64
	Directory bool
}

type FolderRequest struct {
	Token uint32
	Path  string
}

func (FolderRequest) command() uint32 { return PeerFolderContents }
func (m FolderRequest) encode(e *Encoder) error {
	e.U32(m.Token)
	return e.String(m.Path)
}

type FolderResponse struct {
	Token   uint32
	Path    string
	Entries []ShareEntry
}

func (FolderResponse) command() uint32 { return PeerFolderResponse }
func (m FolderResponse) encode(e *Encoder) error {
	var raw Encoder
	raw.U32(m.Token)
	if err := raw.String(m.Path); err != nil {
		return err
	}
	raw.U32(1)
	if err := raw.String(m.Path); err != nil {
		return err
	}
	files := make([]ShareEntry, 0, len(m.Entries))
	for _, entry := range m.Entries {
		if !entry.Directory {
			files = append(files, entry)
		}
	}
	raw.U32(uint32(len(files)))
	for _, file := range files {
		result := SearchResult{Path: filepath.Base(strings.ReplaceAll(file.Name, "\\", "/")), Size: file.Size, Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(file.Name)), ".")}
		if err := result.encode(&raw); err != nil {
			return err
		}
	}
	compressed, err := CompressZlib(raw.Payload())
	if err != nil {
		return err
	}
	e.Raw(compressed)
	return nil
}

func DecodeFolderResponse(b []byte) (FolderResponse, error) {
	var message FolderResponse
	raw, err := DecompressZlib(b)
	if err != nil {
		return message, err
	}
	d := NewDecoder(raw)
	if message.Token, err = d.U32(); err != nil {
		return message, err
	}
	if message.Path, err = d.String(); err != nil {
		return message, err
	}
	folders, err := d.U32()
	if err != nil {
		return message, err
	}
	if folders > 50000 {
		return message, ErrTooLarge
	}
	for i := uint32(0); i < folders; i++ {
		dir, err := d.String()
		if err != nil {
			return message, err
		}
		message.Entries = append(message.Entries, ShareEntry{Name: dir, Directory: true})
		count, err := d.U32()
		if err != nil {
			return message, err
		}
		if count > 50000 || len(message.Entries)+int(count) > 50000 {
			return message, ErrTooLarge
		}
		for j := uint32(0); j < count; j++ {
			file, err := decodeSearchResult(d)
			if err != nil {
				return message, err
			}
			message.Entries = append(message.Entries, ShareEntry{Name: strings.TrimPrefix(dir+"\\"+file.Path, "\\"), Size: file.Size})
		}
	}
	return message, d.Done()
}

type QueueRequest struct {
	Filename string
	Size     uint64
	Offset   uint64
}

func (QueueRequest) command() uint32           { return PeerQueueUpload }
func (m QueueRequest) encode(e *Encoder) error { return e.String(m.Filename) }

type QueuePlace struct {
	Filename string
	Place    uint32
}

func (QueuePlace) command() uint32 { return PeerPlaceInQueue }
func (m QueuePlace) encode(e *Encoder) error {
	if err := e.String(m.Filename); err != nil {
		return err
	}
	e.U32(m.Place)
	return nil
}

type QueueDenied struct{ Filename, Reason string }

func (QueueDenied) command() uint32 { return PeerUploadDenied }
func (m QueueDenied) encode(e *Encoder) error {
	if err := e.String(m.Filename); err != nil {
		return err
	}
	return e.String(m.Reason)
}

type QueueFailedMessage struct{ Filename, Reason string }

func (QueueFailedMessage) command() uint32           { return PeerUploadFailed }
func (m QueueFailedMessage) encode(e *Encoder) error { return e.String(m.Filename) }

type TransferRequest struct {
	Direction uint32
	Token     uint32
	Filename  string
	Size      uint64
	Offset    uint64 // local resume state; not encoded in message 40
}

func (TransferRequest) command() uint32 { return PeerTransferRequest }
func (m TransferRequest) encode(e *Encoder) error {
	e.U32(m.Direction)
	e.U32(m.Token)
	if err := e.String(m.Filename); err != nil {
		return err
	}
	if m.Direction == 1 {
		e.U64(m.Size)
	}
	return nil
}

type TransferResponse struct {
	Token    uint32
	Accepted bool
	Size     uint64
	Reason   string
}

func (TransferResponse) command() uint32 { return PeerTransferResponse }
func (m TransferResponse) encode(e *Encoder) error {
	e.U32(m.Token)
	e.Bool(m.Accepted)
	if !m.Accepted {
		return e.String(m.Reason)
	}
	if m.Size > 0 {
		e.U64(m.Size)
	}
	return nil
}

// EncodeMessage frames a supported message.
func EncodeMessage(m Message) ([]byte, error) {
	if raw, ok := m.(RawMessage); ok {
		return encodeRaw(raw.Command, raw.Payload)
	}
	var e Encoder
	if err := m.encode(&e); err != nil {
		return nil, err
	}
	return encodeRaw(m.command(), e.Payload())
}
func encodeRaw(command uint32, payload []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := WriteFrame(&b, command, payload); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// DecodeMessage decodes payload into the concrete built-in message. Unknown messages are raw.
func DecodeMessage(command uint32, payload []byte) (any, error) {
	switch command {
	case ServerLogin:
		return DecodeLoginResponse(payload)
	case ServerGetPeerAddress:
		return DecodePeerAddress(payload)
	case ServerConnectToPeer:
		return DecodeConnectPeerInstruction(payload)
	case ServerFileSearch:
		return DecodeIncomingSearch(payload)
	case ServerEmbeddedMessage:
		return DecodeEmbeddedDistributed(payload)
	case ServerPossibleParents:
		return DecodePossibleParents(payload)
	case PeerSearch:
		return DecodeSearchResponse(payload)
	case PeerSharedList:
		return DecodeSharedListResponse(payload)
	case PeerTransferRequest:
		return DecodeTransferRequest(payload)
	case PeerTransferResponse:
		return DecodeTransferResponse(payload)
	default:
		return RawMessage{Command: command, Payload: append([]byte(nil), payload...)}, nil
	}
}

type RawMessage struct {
	Command uint32
	Payload []byte
}

func (m RawMessage) command() uint32       { return m.Command }
func (m RawMessage) encode(*Encoder) error { return nil }

func encodePeerHandshake(w net.Conn, m PeerInitMessage) error {
	var payload Encoder
	if err := m.encode(&payload); err != nil {
		return err
	}
	return WriteInitFrame(w, byte(PeerInit), payload.Payload())
}
func parsePeerInit(b []byte) (PeerInitMessage, error) {
	d := NewDecoder(b)
	var m PeerInitMessage
	var err error
	if m.Username, err = d.String(); err != nil {
		return m, err
	}
	if m.Type, err = d.String(); err != nil {
		return m, err
	}
	if m.Token, err = d.U32(); err != nil {
		return m, err
	}
	return m, d.Done()
}

func DecodeTransferRequest(b []byte) (TransferRequest, error) {
	d := NewDecoder(b)
	var message TransferRequest
	var err error
	if message.Direction, err = d.U32(); err != nil {
		return message, err
	}
	if message.Token, err = d.U32(); err != nil {
		return message, err
	}
	if message.Filename, err = d.String(); err != nil {
		return message, err
	}
	if message.Direction == 1 {
		if message.Size, err = d.U64(); err != nil {
			return message, err
		}
	} else if message.Direction != 0 {
		return message, fmt.Errorf("%w: transfer direction", ErrMalformed)
	}
	return message, d.Done()
}

func DecodeTransferResponse(b []byte) (TransferResponse, error) {
	d := NewDecoder(b)
	var message TransferResponse
	var err error
	if message.Token, err = d.U32(); err != nil {
		return message, err
	}
	if message.Accepted, err = d.Bool(); err != nil {
		return message, err
	}
	if !message.Accepted {
		if message.Reason, err = d.String(); err != nil {
			return message, err
		}
	} else if d.Remaining() == 8 {
		if message.Size, err = d.U64(); err != nil {
			return message, err
		}
	} else if d.Remaining() != 0 {
		return message, fmt.Errorf("%w: transfer response", ErrMalformed)
	}
	return message, d.Done()
}
func parseStringPayload(b []byte) (string, error) {
	d := NewDecoder(b)
	s, e := d.String()
	if e == nil {
		e = d.Done()
	}
	return s, e
}
func protocolError(format string, args ...any) error { return fmt.Errorf("soulseek: "+format, args...) }
