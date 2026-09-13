package soulseek

import (
	"context"
	"maps"
	"net"
	"time"
)

// Preferred users share one class; exemption only bypasses configured queue caps.
type UploadUserPolicy struct{ Preferred, ExemptLimits bool }

func (m *UploadManager) SetUserPolicies(users map[string]UploadUserPolicy) {
	users = maps.Clone(users)
	m.mu.Lock()
	m.users = users
	m.mu.Unlock()
}

func (c *Client) SetUploadUserPolicies(users map[string]UploadUserPolicy) {
	if c.cfg.Uploads != nil {
		c.cfg.Uploads.SetUserPolicies(users)
	}
}

// Restore the whole accepted queue before choosing slots, not the first restored file.
func (c *Client) beginUploadRecovery() {
	if c.cfg.UploadsReady == nil {
		return
	}
	m := c.cfg.Uploads
	root, ready := c.uploadRoot, c.cfg.UploadsReady
	m.mu.Lock()
	m.recovering = true
	m.mu.Unlock()
	c.uploadWG.Add(1)
	go func() {
		defer c.uploadWG.Done()
		select {
		case <-root.Done():
			return
		case <-ready:
			m.mu.Lock()
			m.recovering = false
			m.promote()
			m.mu.Unlock()
		}
	}()
}

// Position projects the current queue with the actual scheduling policy, without
// consuming its random draws. Active transfers have position zero. New arrivals,
// policy changes and completion of currently busy users can change the projection.
// When slots fill or all queued users are busy, projection assumes the current
// active batch completes together; the peer protocol cannot predict completion order.
func (m *UploadManager) Position(ctx context.Context, job *UploadJob) (uint32, error) {
	select {
	case m.positionSlot <- struct{}{}:
		defer func() { <-m.positionSlot }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	m.mu.Lock()
	if uint64(len(m.q)) > uint64(^uint32(0)) {
		m.mu.Unlock()
		return 0, ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return 0, err
	}
	if job == nil || job.cancelled || job.released {
		m.mu.Unlock()
		return 0, ErrTransferCancelled
	}
	if job.active {
		m.mu.Unlock()
		return 0, nil
	}
	random := *m.random
	simulation := UploadManager{max: m.max, active: m.active, policy: m.policy, users: m.users, byUser: maps.Clone(m.byUser), served: make(map[string]uint64), sequence: m.sequence, random: &random}
	var target *UploadJob
	for _, queued := range m.q {
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return 0, err
		}
		if queued.cancelled {
			continue
		}
		copy := *queued
		simulation.q = append(simulation.q, &copy)
		simulation.served[queued.User] = m.served[queued.User]
		if queued == job {
			target = &copy
		}
	}
	m.mu.Unlock()
	if target == nil {
		return 0, ErrTransferCancelled
	}
	// ponytail: O(n²) projection reuses scheduling; use a heap if large queues make it costly.
	for position := uint32(1); len(simulation.q) > 0; position++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		i := -1
		if simulation.active < simulation.max {
			i = simulation.nextIndex()
		}
		if i < 0 {
			clear(simulation.byUser)
			simulation.active = 0
			i = simulation.nextIndex()
		}
		next := simulation.q[i]
		if next == target {
			return position, nil
		}
		simulation.q = append(simulation.q[:i], simulation.q[i+1:]...)
		simulation.sequence++
		simulation.served[next.User] = simulation.sequence
		simulation.active++
		simulation.byUser[next.User]++
	}
	return 0, ErrTransferCancelled
}

func (c *Client) writeUploadPosition(peer net.Conn, username, filename string) error {
	filename, err := NormalizePath(filename)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.shareResponseContext(), time.Second)
	defer cancel()
	if err := c.checkUploadPermission(username, filename, peerIP(peer.RemoteAddr())); err != nil {
		return writeShareMessage(ctx, peer, QueueDenied{Filename: filename, Reason: uploadDenial(err)})
	}
	c.mu.Lock()
	a := c.uploads[downloadKey(username, filename)]
	c.mu.Unlock()
	if a == nil {
		return nil
	}
	position, err := c.cfg.Uploads.Position(ctx, a.job)
	if err != nil {
		return err
	}
	return writeShareMessage(ctx, peer, QueuePlace{Filename: a.target.Filename, Place: position})
}
