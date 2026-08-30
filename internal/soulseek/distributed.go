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
	if err := e.String(m.Username); err != nil {
		return nil, err
	}
	e.U32(m.Token)
	if err := e.String(m.Query); err != nil {
		return nil, err
	}
	return append([]byte(nil), e.Payload()...), nil
}

func DecodeDistributedSearch(b []byte) (DistributedSearchQuery, error) {
	d := NewDecoder(b)
	var message DistributedSearchQuery
	identifier, err := d.U32()
	if err != nil {
		return message, err
	}
	if identifier != 49 {
		return message, ErrMalformed
	}
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

type DistributedBranchLevel int32

func (level DistributedBranchLevel) MarshalBinary() []byte {
	var e Encoder
	e.U32(uint32(level))
	return append([]byte(nil), e.Payload()...)
}
func DecodeDistributedBranchLevel(b []byte) (DistributedBranchLevel, error) {
	d := NewDecoder(b)
	value, err := d.U32()
	if err != nil {
		return 0, err
	}
	return DistributedBranchLevel(int32(value)), d.Done()
}

type DistributedBranchRoot string

func (root DistributedBranchRoot) MarshalBinary() ([]byte, error) {
	var e Encoder
	if err := e.String(string(root)); err != nil {
		return nil, err
	}
	return append([]byte(nil), e.Payload()...), nil
}
func DecodeDistributedBranchRoot(b []byte) (DistributedBranchRoot, error) {
	d := NewDecoder(b)
	root, err := d.String()
	if err != nil {
		return "", err
	}
	return DistributedBranchRoot(root), d.Done()
}
