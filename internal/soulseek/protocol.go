package soulseek

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
)

// Soulseek command identifiers. The wire command byte follows the frame length.
const (
	ServerLogin                 uint32 = 1
	ServerSetListenPort         uint32 = 2
	ServerGetPeerAddress        uint32 = 3
	ServerAddUser               uint32 = 5
	ServerConnectToPeer         uint32 = 18
	ServerFileSearch            uint32 = 26
	ServerSetStatus             uint32 = 28
	ServerPing                  uint32 = 32
	ServerSharedCounts          uint32 = 35
	ServerHaveNoParent          uint32 = 71
	ServerEmbeddedMessage       uint32 = 93
	ServerAcceptChildren        uint32 = 100
	ServerPossibleParents       uint32 = 102
	ServerWishlistSearch        uint32 = 103
	ServerWishlistInterval      uint32 = 104
	ServerBranchLevel           uint32 = 126
	ServerBranchRoot            uint32 = 127
	ServerResetDistributed      uint32 = 130
	ServerChangePassword        uint32 = 142
	ServerExcludedSearchPhrases uint32 = 160

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

const (
	maxShareEntries          = 2_000_000
	maxSearchResults         = 10_000
	maxExcludedSearchPhrases = 10_000
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
	e.String(m.Username)
	e.String(m.Password)
	e.U32(m.Version)
	e.String(m.Hash)
	e.U32(m.MinorVersion)
	return nil
}
func DecodeLoginRequest(b []byte) (LoginRequest, error) {
	d := NewDecoder(b)
	// Composite literal fields evaluate left to right, matching wire order.
	m := LoginRequest{Username: d.String(), Password: d.String(), Version: d.U32(), Hash: d.String(), MinorVersion: d.U32()}
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
	e.String(m.Message)
	if !m.Success {
		if m.Detail != "" {
			e.String(m.Detail)
		}
		return nil
	}
	e.U32(m.IP)
	e.String(m.Hash)
	e.Bool(m.Supporter)
	return nil
}
func DecodeLoginResponse(b []byte) (LoginResponse, error) {
	d := NewDecoder(b)
	var m LoginResponse
	m.Success = d.Bool()
	m.Message = d.String()
	if !m.Success {
		if d.Remaining() > 0 {
			m.Detail = d.String()
		}
		return m, d.Done()
	}
	m.IP = d.U32()
	m.Hash = d.String()
	m.Supporter = d.Bool()
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

type ChangePassword struct{ Password string }

func (ChangePassword) command() uint32           { return ServerChangePassword }
func (m ChangePassword) encode(e *Encoder) error { e.String(m.Password); return nil }

func DecodeChangePassword(b []byte) (ChangePassword, error) {
	d := NewDecoder(b)
	m := ChangePassword{Password: d.String()}
	return m, d.Done()
}

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
func (m BranchRoot) encode(e *Encoder) error { e.String(m.Username); return nil }

type ParentCandidate struct {
	Username, IP string
	Port         uint32
}
type PossibleParents struct{ Parents []ParentCandidate }

func DecodePossibleParents(b []byte) (PossibleParents, error) {
	d := NewDecoder(b)
	var message PossibleParents
	count := d.U32()
	if count > 10 {
		return message, ErrTooLarge
	}
	for i := uint32(0); i < count; i++ {
		var parent ParentCandidate
		parent.Username = d.String()
		ip := d.U32()
		parent.IP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip)).String()
		parent.Port = d.U32()
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
	m := EmbeddedDistributed{Command: d.U8(), Payload: append([]byte(nil), d.Bytes()...)}
	return m, d.Done()
}

type PeerAddressRequest struct{ Username string }

func (PeerAddressRequest) command() uint32           { return ServerGetPeerAddress }
func (m PeerAddressRequest) encode(e *Encoder) error { e.String(m.Username); return nil }

type PeerAddress struct {
	Username       string
	IP             string
	Port           uint32
	Obfuscation    uint32
	ObfuscatedPort uint16
}

func (PeerAddress) command() uint32 { return ServerGetPeerAddress }
func (m PeerAddress) encode(e *Encoder) error {
	e.String(m.Username)
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
	var m PeerAddress
	m.Username = d.String()
	ip := d.U32()
	m.IP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip)).String()
	m.Port = d.U32()
	if d.Remaining() > 0 {
		m.Obfuscation = d.U32()
		m.ObfuscatedPort = d.U16()
	}
	return m, d.Done()
}

type ConnectPeer struct {
	Token    uint32
	Username string
	Kind     string
}

func (ConnectPeer) command() uint32 { return ServerConnectToPeer }
func (m ConnectPeer) encode(e *Encoder) error {
	e.U32(m.Token)
	e.String(m.Username)
	e.String(m.Kind)
	return nil
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
	var m ConnectPeerInstruction
	m.Username = decodeUsername(d)
	m.Kind = d.String()
	ip := d.U32()
	m.IP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip)).String()
	m.Port = d.U32()
	m.Token = d.U32()
	m.Privileged = d.Bool()
	if d.Remaining() > 0 {
		m.Obfuscation = d.U32()
		m.ObfuscatedPort = d.U32()
	}
	return m, d.Done()
}

type SearchRequest struct {
	Token uint32
	Query string
}

func (SearchRequest) command() uint32           { return ServerFileSearch }
func (m SearchRequest) encode(e *Encoder) error { e.U32(m.Token); e.String(m.Query); return nil }

type WishlistSearchRequest SearchRequest

func (WishlistSearchRequest) command() uint32 { return ServerWishlistSearch }
func (m WishlistSearchRequest) encode(e *Encoder) error {
	e.U32(m.Token)
	e.String(m.Query)
	return nil
}

type WishlistInterval struct{ Seconds uint32 }

func DecodeWishlistInterval(b []byte) (WishlistInterval, error) {
	d := NewDecoder(b)
	m := WishlistInterval{Seconds: d.U32()}
	return m, d.Done()
}

type ExcludedSearchPhrases struct{ Phrases []string }

func DecodeExcludedSearchPhrases(b []byte) (ExcludedSearchPhrases, error) {
	d := NewDecoder(b)
	count := d.U32()
	if count > maxExcludedSearchPhrases {
		return ExcludedSearchPhrases{}, ErrTooLarge
	}
	message := ExcludedSearchPhrases{Phrases: make([]string, 0, count)}
	for i := uint32(0); i < count; i++ {
		message.Phrases = append(message.Phrases, d.String())
	}
	return message, d.Done()
}

type IncomingSearch struct {
	Username string
	Token    uint32
	Query    string
}

func DecodeIncomingSearch(b []byte) (IncomingSearch, error) {
	d := NewDecoder(b)
	m := IncomingSearch{Username: d.String(), Token: d.U32(), Query: d.String()}
	return m, d.Done()
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
	public, private := make([]SearchResult, 0, len(m.Results)), make([]SearchResult, 0)
	for _, result := range m.Results {
		if result.Public {
			public = append(public, result)
		} else {
			private = append(private, result)
		}
	}
	if len(public) > maxSearchResults || len(private) > maxSearchResults {
		return ErrTooLarge
	}
	var raw Encoder
	raw.String(m.Username)
	raw.U32(m.Token)
	raw.U32(uint32(len(public)))
	for _, result := range public {
		result.encode(&raw)
	}
	raw.Bool(m.SlotFree)
	raw.U32(m.Speed)
	raw.U32(m.QueueLength)
	raw.U32(0)
	raw.U32(uint32(len(private)))
	for _, result := range private {
		result.encode(&raw)
	}
	if err := raw.Err(); err != nil {
		return err
	}
	compressed, err := CompressZlib(raw.Payload())
	if err != nil {
		return err
	}
	e.Raw(compressed)
	return nil
}

const (
	FileAttributeBitrate    uint32 = 0
	FileAttributeDuration   uint32 = 1
	FileAttributeVBR        uint32 = 2
	FileAttributeSampleRate uint32 = 4
	FileAttributeBitDepth   uint32 = 5
)

type SearchResult struct {
	Username    string `json:"username,omitempty"`
	Path        string `json:"path"`
	Extension   string `json:"extension,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
	Size        uint64 `json:"size"`
	IsDirectory bool   `json:"directory,omitempty"`
	SlotFree    bool   `json:"slot_free,omitempty"`
	Speed       uint32 `json:"speed,omitempty"`
	QueueLength uint32 `json:"queue_length,omitempty"`
	Bitrate     uint32 `json:"bitrate,omitempty"`
	Duration    uint32 `json:"duration,omitempty"`
	VBR         bool   `json:"vbr,omitempty"`
	VBRKnown    bool   `json:"vbr_known,omitempty"`
	SampleRate  uint32 `json:"sample_rate,omitempty"`
	BitDepth    uint32 `json:"bit_depth,omitempty"`
	Public      bool   `json:"public"`
}

func (r SearchResult) encode(e *Encoder) error {
	e.U8(1)
	e.String(r.Path)
	e.U64(r.Size)
	e.String(r.Extension)
	attributes := [][2]uint32{}
	if r.Bitrate != 0 {
		attributes = append(attributes, [2]uint32{FileAttributeBitrate, r.Bitrate})
	}
	if r.Duration != 0 {
		attributes = append(attributes, [2]uint32{FileAttributeDuration, r.Duration})
	}
	if r.VBR || r.VBRKnown {
		value := uint32(0)
		if r.VBR {
			value = 1
		}
		attributes = append(attributes, [2]uint32{FileAttributeVBR, value})
	}
	if r.SampleRate != 0 {
		attributes = append(attributes, [2]uint32{FileAttributeSampleRate, r.SampleRate})
	}
	if r.BitDepth != 0 {
		attributes = append(attributes, [2]uint32{FileAttributeBitDepth, r.BitDepth})
	}
	e.U32(uint32(len(attributes)))
	for _, attribute := range attributes {
		e.U32(attribute[0])
		e.U32(attribute[1])
	}
	return nil
}

type fileDecoder interface {
	U8() uint8
	U32() uint32
	U64() uint64
	String() string
	fail(error)
}

func decodeSearchResult(d fileDecoder) SearchResult {
	var result SearchResult
	if code := d.U8(); code != 1 {
		d.fail(fmt.Errorf("%w: file code %d", ErrMalformed, code))
		return result
	}
	result.Path = d.String()
	result.Size = d.U64()
	result.Extension = d.String()
	count := d.U32()
	if count > 64 {
		d.fail(fmt.Errorf("%w: file has %d attributes (limit 64)", ErrTooLarge, count))
		return result
	}
	for i := uint32(0); i < count; i++ {
		attribute := d.U32()
		value := d.U32()
		switch attribute {
		case FileAttributeBitrate:
			result.Bitrate = value
		case FileAttributeDuration:
			result.Duration = value
		case FileAttributeVBR:
			result.VBR, result.VBRKnown = value != 0, true
		case FileAttributeSampleRate:
			result.SampleRate = value
		case FileAttributeBitDepth:
			result.BitDepth = value
		}
	}
	return result
}

func DecodeSearchResponse(b []byte) (SearchResponse, error) {
	var message SearchResponse
	raw, err := DecompressZlib(b)
	if err != nil {
		return message, err
	}
	d := NewDecoder(raw)
	message.Username = d.String()
	message.Token = d.U32()
	count := d.U32()
	if count > maxSearchResults {
		return message, ErrTooLarge
	}
	message.Results = make([]SearchResult, 0, count)
	for i := uint32(0); i < count; i++ {
		result := decodeSearchResult(d)
		result.Username, result.Public = message.Username, true
		message.Results = append(message.Results, result)
	}
	message.SlotFree = d.Bool()
	message.Speed = d.U32()
	message.QueueLength = d.U32()
	d.U32()
	if d.Remaining() > 0 {
		privateCount := d.U32()
		if privateCount > maxSearchResults {
			return message, ErrTooLarge
		}
		for i := uint32(0); i < privateCount; i++ {
			result := decodeSearchResult(d)
			result.Username = message.Username
			message.Results = append(message.Results, result)
		}
	}
	if d.Err() != nil {
		return message, d.Err()
	}
	for i := range message.Results {
		message.Results[i].SlotFree, message.Results[i].Speed, message.Results[i].QueueLength = message.SlotFree, message.Speed, message.QueueLength
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
func (m FileSearchRequest) encode(e *Encoder) error { e.U32(m.Token); e.String(m.Query); return nil }

func (PeerInitMessage) command() uint32 { return PeerInit }
func (m PeerInitMessage) encode(e *Encoder) error {
	e.String(m.Username)
	e.String(m.Type)
	e.U32(m.Token)
	return nil
}

type SharedListRequest struct{}

func (SharedListRequest) command() uint32       { return PeerGetSharedList }
func (SharedListRequest) encode(*Encoder) error { return nil }

type SharedListResponse struct{ Entries []ShareEntry }

func (SharedListResponse) command() uint32 { return PeerSharedList }
func (m SharedListResponse) encode(e *Encoder) error {
	groups := [2]map[string][]ShareEntry{{}, {}}
	for _, entry := range m.Entries {
		if entry.Directory {
			continue
		}
		name := strings.ReplaceAll(entry.Name, "/", "\\")
		dir, file := "", name
		if cut := strings.LastIndexByte(name, '\\'); cut >= 0 {
			dir, file = name[:cut], name[cut+1:]
		}
		index := 0
		if entry.Private {
			index = 1
		}
		entry.Name = file
		groups[index][dir] = append(groups[index][dir], entry)
	}
	if len(groups[0])+len(groups[1]) > maxShareEntries {
		return ErrTooLarge
	}
	var raw Encoder
	writeGroups := func(group map[string][]ShareEntry) {
		dirs := make([]string, 0, len(group))
		for dir := range group {
			dirs = append(dirs, dir)
		}
		sort.Strings(dirs)
		raw.U32(uint32(len(dirs)))
		for _, dir := range dirs {
			raw.String(dir)
			raw.U32(uint32(len(group[dir])))
			for _, file := range group[dir] {
				searchResultFromShare(file, file.Name).encode(&raw)
			}
		}
	}
	writeGroups(groups[0])
	raw.U32(0)
	writeGroups(groups[1])
	if err := raw.Err(); err != nil {
		return err
	}
	compressed, err := CompressZlib(raw.Payload())
	if err != nil {
		return err
	}
	e.Raw(compressed)
	return nil
}

func DecodeSharedListResponse(b []byte) (SharedListResponse, error) {
	return decodeSharedListResponse(b, BrowseLimits{}.withDefaults())
}

func decodeSharedListResponse(b []byte, limits BrowseLimits, loggers ...*slog.Logger) (SharedListResponse, error) {
	var message SharedListResponse
	raw, err := decompressZlib(b, limits.MaxCompressedSize, limits.MaxDecompressedSize, loggers...)
	if err != nil {
		return message, err
	}
	d := NewDecoder(raw)
	count := d.U32()
	if uint64(count) > uint64(limits.MaxEntries) {
		logLimit(firstLogger(loggers), "entries", uint64(limits.MaxEntries), uint64(count))
		return message, fmt.Errorf("%w: share list has %d directories (limit %d)", ErrTooLarge, count, limits.MaxEntries)
	}
	for i := uint32(0); i < count; i++ {
		if d.Err() != nil {
			return message, d.Err()
		}
		dir := d.String()
		message.Entries = append(message.Entries, ShareEntry{Name: dir, Directory: true})
		files := d.U32()
		if uint64(len(message.Entries))+uint64(files) > uint64(limits.MaxEntries) {
			logLimit(firstLogger(loggers), "entries", uint64(limits.MaxEntries), uint64(len(message.Entries))+uint64(files))
			return message, fmt.Errorf("%w: share list has at least %d files/directories (limit %d)", ErrTooLarge, uint64(len(message.Entries))+uint64(files), limits.MaxEntries)
		}
		for j := uint32(0); j < files; j++ {
			file := decodeSearchResult(d)
			message.Entries = append(message.Entries, shareEntryFromSearch(strings.TrimPrefix(dir+"\\"+file.Path, "\\"), file, false))
		}
	}
	d.U32()
	if d.Remaining() > 0 {
		private := d.U32()
		if uint64(len(message.Entries))+uint64(private) > uint64(limits.MaxEntries) {
			logLimit(firstLogger(loggers), "entries", uint64(limits.MaxEntries), uint64(len(message.Entries))+uint64(private))
			return message, fmt.Errorf("%w: share list including private directories has at least %d files/directories (limit %d)", ErrTooLarge, uint64(len(message.Entries))+uint64(private), limits.MaxEntries)
		}
		for i := uint32(0); i < private; i++ {
			if d.Err() != nil {
				return message, d.Err()
			}
			dir := d.String()
			message.Entries = append(message.Entries, ShareEntry{Name: dir, Directory: true, Private: true})
			count := d.U32()
			if uint64(len(message.Entries))+uint64(count) > uint64(limits.MaxEntries) {
				logLimit(firstLogger(loggers), "entries", uint64(limits.MaxEntries), uint64(len(message.Entries))+uint64(count))
				return message, fmt.Errorf("%w: share list including private entries has at least %d files/directories (limit %d)", ErrTooLarge, uint64(len(message.Entries))+uint64(count), limits.MaxEntries)
			}
			for j := uint32(0); j < count; j++ {
				file := decodeSearchResult(d)
				message.Entries = append(message.Entries, shareEntryFromSearch(strings.TrimPrefix(dir+"\\"+file.Path, "\\"), file, true))
			}
		}
	}
	return message, d.Done()
}

type ShareEntry struct {
	Name                    string
	Size                    uint64
	Directory, Private, VBR bool
	VBRKnown                bool `json:"vbr_known,omitempty"`
	Extension               string
	Bitrate, Duration       uint32
	SampleRate, BitDepth    uint32
}

func shareEntryFromSearch(path string, file SearchResult, private bool) ShareEntry {
	return ShareEntry{Name: path, Size: file.Size, Private: private, Extension: file.Extension, Bitrate: file.Bitrate, Duration: file.Duration, VBR: file.VBR, VBRKnown: file.VBRKnown, SampleRate: file.SampleRate, BitDepth: file.BitDepth}
}

func searchResultFromShare(file ShareEntry, path string) SearchResult {
	return SearchResult{Path: path, Size: file.Size, Extension: file.Extension, Bitrate: file.Bitrate, Duration: file.Duration, VBR: file.VBR, VBRKnown: file.VBRKnown, SampleRate: file.SampleRate, BitDepth: file.BitDepth}
}

type FolderRequest struct {
	Token uint32
	Path  string
}

func (FolderRequest) command() uint32 { return PeerFolderContents }
func (m FolderRequest) encode(e *Encoder) error {
	e.U32(m.Token)
	e.String(m.Path)
	return nil
}

type FolderResponse struct {
	Token   uint32
	Path    string
	Entries []ShareEntry
}

func (FolderResponse) command() uint32 { return PeerFolderResponse }
func (m FolderResponse) encode(e *Encoder) error {
	root := strings.ReplaceAll(m.Path, "/", "\\")
	folders := map[string][]ShareEntry{root: nil}
	total := 0
	for _, entry := range m.Entries {
		name := strings.ReplaceAll(entry.Name, "/", "\\")
		if entry.Directory {
			folders[name] = folders[name]
			continue
		}
		dir, file := root, name
		if cut := strings.LastIndexByte(name, '\\'); cut >= 0 {
			dir, file = name[:cut], name[cut+1:]
		}
		entry.Name = file
		folders[dir] = append(folders[dir], entry)
		total++
	}
	if len(folders) > maxShareEntries || total+len(folders) > maxShareEntries {
		return ErrTooLarge
	}
	dirs := make([]string, 0, len(folders))
	for dir := range folders {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i] == root || dirs[j] == root {
			return dirs[i] == root && dirs[j] != root
		}
		return dirs[i] < dirs[j]
	})

	var raw Encoder
	raw.U32(m.Token)
	raw.String(m.Path)
	raw.U32(uint32(len(dirs)))
	for _, dir := range dirs {
		raw.String(dir)
		raw.U32(uint32(len(folders[dir])))
		for _, file := range folders[dir] {
			searchResultFromShare(file, file.Name).encode(&raw)
		}
	}
	if err := raw.Err(); err != nil {
		return err
	}
	compressed, err := CompressZlib(raw.Payload())
	if err != nil {
		return err
	}
	e.Raw(compressed)
	return nil
}

func DecodeFolderResponse(b []byte) (FolderResponse, error) {
	return decodeFolderResponse(b, BrowseLimits{}.withDefaults())
}

func decodeFolderResponse(b []byte, limits BrowseLimits, loggers ...*slog.Logger) (FolderResponse, error) {
	var message FolderResponse
	raw, err := decompressZlib(b, limits.MaxCompressedSize, limits.MaxDecompressedSize, loggers...)
	if err != nil {
		return message, err
	}
	d := NewDecoder(raw)
	message.Token = d.U32()
	message.Path = d.String()
	folders := d.U32()
	if uint64(folders) > uint64(limits.MaxEntries) {
		logLimit(firstLogger(loggers), "entries", uint64(limits.MaxEntries), uint64(folders))
		return message, fmt.Errorf("%w: folder response has %d directories (limit %d)", ErrTooLarge, folders, limits.MaxEntries)
	}
	for i := uint32(0); i < folders; i++ {
		if d.Err() != nil {
			return message, d.Err()
		}
		dir := d.String()
		message.Entries = append(message.Entries, ShareEntry{Name: dir, Directory: true})
		count := d.U32()
		if uint64(len(message.Entries))+uint64(count) > uint64(limits.MaxEntries) {
			logLimit(firstLogger(loggers), "entries", uint64(limits.MaxEntries), uint64(len(message.Entries))+uint64(count))
			return message, fmt.Errorf("%w: folder response has at least %d files/directories (limit %d)", ErrTooLarge, uint64(len(message.Entries))+uint64(count), limits.MaxEntries)
		}
		for j := uint32(0); j < count; j++ {
			file := decodeSearchResult(d)
			message.Entries = append(message.Entries, shareEntryFromSearch(strings.TrimPrefix(dir+"\\"+file.Path, "\\"), file, false))
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
func (m QueueRequest) encode(e *Encoder) error { e.String(m.Filename); return nil }

type QueuePlace struct {
	Filename string
	Place    uint32
}

func (QueuePlace) command() uint32 { return PeerPlaceInQueue }
func (m QueuePlace) encode(e *Encoder) error {
	e.String(m.Filename)
	e.U32(m.Place)
	return nil
}

type QueueDenied struct{ Filename, Reason string }

func (QueueDenied) command() uint32 { return PeerUploadDenied }
func (m QueueDenied) encode(e *Encoder) error {
	e.String(m.Filename)
	e.String(m.Reason)
	return nil
}

type QueueFailedMessage struct{ Filename, Reason string }

func (QueueFailedMessage) command() uint32           { return PeerUploadFailed }
func (m QueueFailedMessage) encode(e *Encoder) error { e.String(m.Filename); return nil }

func DecodeQueueDenied(b []byte) (QueueDenied, error) {
	d := NewDecoder(b)
	m := QueueDenied{Filename: d.String(), Reason: d.String()}
	return m, d.Done()
}

func DecodeQueueFailed(b []byte) (QueueFailedMessage, error) {
	d := NewDecoder(b)
	m := QueueFailedMessage{Filename: d.String()}
	return m, d.Done()
}

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
	e.String(m.Filename)
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
		e.String(m.Reason)
		return nil
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
	if err := e.Err(); err != nil {
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
	case ServerChangePassword:
		return DecodeChangePassword(payload)
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
	case ServerWishlistInterval:
		return DecodeWishlistInterval(payload)
	case ServerExcludedSearchPhrases:
		return DecodeExcludedSearchPhrases(payload)
	case PeerSearch:
		return DecodeSearchResponse(payload)
	case PeerSharedList:
		return DecodeSharedListResponse(payload)
	case PeerTransferRequest:
		return DecodeTransferRequest(payload)
	case PeerTransferResponse:
		return DecodeTransferResponse(payload)
	case PeerUploadDenied:
		return DecodeQueueDenied(payload)
	case PeerUploadFailed:
		return DecodeQueueFailed(payload)
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
	m.encode(&payload)
	if payload.Err() != nil {
		return payload.Err()
	}
	return WriteInitFrame(w, byte(PeerInit), payload.Payload())
}
func parsePeerInit(b []byte) (PeerInitMessage, error) {
	d := NewDecoder(b)
	m := PeerInitMessage{Username: d.String(), Type: d.String(), Token: d.U32()}
	return m, d.Done()
}

func DecodeTransferRequest(b []byte) (TransferRequest, error) {
	d := NewDecoder(b)
	var m TransferRequest
	m.Direction = d.U32()
	m.Token = d.U32()
	m.Filename = d.String()
	if m.Direction > 1 {
		return m, fmt.Errorf("%w: transfer direction", ErrMalformed)
	}
	if m.Direction == 1 || d.Remaining() > 0 {
		m.Size = d.U64()
	}
	return m, d.Done()
}

func DecodeTransferResponse(b []byte) (TransferResponse, error) {
	d := NewDecoder(b)
	var m TransferResponse
	m.Token = d.U32()
	m.Accepted = d.Bool()
	if !m.Accepted {
		m.Reason = d.String()
	} else if d.Err() != nil {
		return m, d.Err()
	} else if d.Remaining() == 8 {
		m.Size = d.U64()
	} else if d.Remaining() != 0 {
		return m, fmt.Errorf("%w: transfer response", ErrMalformed)
	}
	return m, d.Done()
}
func parseStringPayload(b []byte) (string, error) {
	d := NewDecoder(b)
	s := d.String()
	return s, d.Done()
}
