package soulseek

import (
	"errors"
	"io"
	"sync"
)

// DistributedMessage preserves a D-message payload byte-for-byte for forwarding.
type DistributedMessage struct {
	Command byte
	Payload []byte
}

func ReadDistributed(r io.Reader) (DistributedMessage, error) {
	command, payload, err := ReadInitFrame(r)
	return DistributedMessage{Command: command, Payload: payload}, err
}

func WriteDistributed(w io.Writer, message DistributedMessage) error {
	return WriteInitFrame(w, message.Command, message.Payload)
}

// DistributedNode tracks one parent and direct children. Dispatch does not alter raw searches.
type DistributedNode struct {
	mu       sync.Mutex
	parent   string
	children map[string]chan DistributedMessage
}

func NewDistributedNode() *DistributedNode {
	return &DistributedNode{children: make(map[string]chan DistributedMessage)}
}
func (n *DistributedNode) SetParent(id string) { n.mu.Lock(); n.parent = id; n.mu.Unlock() }
func (n *DistributedNode) Parent() string      { n.mu.Lock(); defer n.mu.Unlock(); return n.parent }
func (n *DistributedNode) AddChild(id string) (<-chan DistributedMessage, error) {
	if id == "" {
		return nil, errors.New("empty child")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.children[id]; exists {
		return nil, errors.New("child already exists")
	}
	ch := make(chan DistributedMessage, 16)
	n.children[id] = ch
	return ch, nil
}
func (n *DistributedNode) RemoveChild(id string) {
	n.mu.Lock()
	if ch := n.children[id]; ch != nil {
		delete(n.children, id)
		close(ch)
	}
	n.mu.Unlock()
}
func (n *DistributedNode) Dispatch(message DistributedMessage) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	sent := 0
	for _, ch := range n.children {
		copyMessage := DistributedMessage{Command: message.Command, Payload: append([]byte(nil), message.Payload...)}
		select {
		case ch <- copyMessage:
			sent++
		default:
		}
	}
	return sent
}

type DistributedSearchQuery struct {
	Username string
	Token    uint32
	Query    string
}

func (m DistributedSearchQuery) MarshalBinary() ([]byte, error) {
	var e Encoder
	e.U32(49)
	e.String(m.Username)
	e.U32(m.Token)
	e.String(m.Query)
	return append([]byte(nil), e.Payload()...), e.Err()
}

func DecodeDistributedSearch(b []byte) (DistributedSearchQuery, error) {
	d := NewDecoder(b)
	var m DistributedSearchQuery
	if identifier := d.U32(); identifier != 49 {
		return m, ErrMalformed
	}
	m.Username = d.String()
	m.Token = d.U32()
	m.Query = d.String()
	return m, d.Done()
}

type DistributedBranchLevel int32

func (level DistributedBranchLevel) MarshalBinary() []byte {
	var e Encoder
	e.U32(uint32(level))
	return append([]byte(nil), e.Payload()...)
}
func DecodeDistributedBranchLevel(b []byte) (DistributedBranchLevel, error) {
	d := NewDecoder(b)
	level := DistributedBranchLevel(int32(d.U32()))
	return level, d.Done()
}

type DistributedBranchRoot string

func (root DistributedBranchRoot) MarshalBinary() ([]byte, error) {
	var e Encoder
	e.String(string(root))
	return append([]byte(nil), e.Payload()...), e.Err()
}
func DecodeDistributedBranchRoot(b []byte) (DistributedBranchRoot, error) {
	d := NewDecoder(b)
	root := DistributedBranchRoot(d.String())
	return root, d.Done()
}
