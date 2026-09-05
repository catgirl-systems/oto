package soulseek

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type TransferEvent struct {
	Direction, Username, Filename, State string
	Attempt                              uint64
	Done, Total                          uint64
	Error                                string
}

// UploadTarget identifies an attempt, not a future request for the same file.
type UploadTarget struct {
	Username, Filename string
	Attempt            uint64
}

type uploadAttempt struct {
	target         UploadTarget
	key, localPath string
	job            *UploadJob
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	mu             sync.Mutex
	state          string
	progress       uint64
	manual, notify bool
}

func (c *Client) emitUpload(a *uploadAttempt, state string, done uint64, message string) {
	if callback := c.cfg.UploadUpdate; callback != nil {
		callback(TransferEvent{Direction: "upload", Username: a.target.Username, Filename: a.target.Filename, Attempt: a.target.Attempt, State: state, Done: done, Total: a.job.Request.Size, Error: message})
	}
	// Events are informational; the callback above owns authoritative bookkeeping.
	c.emit(Event{Command: PeerTransferRequest, Message: TransferEvent{Direction: "upload", Username: a.target.Username, Filename: a.target.Filename, Attempt: a.target.Attempt, State: state, Done: done, Total: a.job.Request.Size, Error: message}})
}

func (c *Client) validateUpload(username, filename string) (string, string, uint64, error) {
	if username == "" {
		return "", "", 0, errors.New("soulseek: empty username")
	}
	clean, err := NormalizePath(filename)
	if err != nil {
		return "", "", 0, err
	}
	localPath, err := c.shareIndex().Resolve(clean)
	if err != nil {
		return "", "", 0, err
	}
	stat, err := os.Stat(localPath)
	if err != nil {
		return "", "", 0, err
	}
	if !stat.Mode().IsRegular() {
		return "", "", 0, errors.New("soulseek: shared path is not a file")
	}
	return strings.ReplaceAll(clean, "/", "\\"), localPath, uint64(stat.Size()), nil
}

func (c *Client) registerUpload(username, filename string) (*uploadAttempt, bool, error) {
	for {
		wire, localPath, size, err := c.validateUpload(username, filename)
		if err != nil {
			return nil, false, err
		}
		key := downloadKey(username, wire)
		c.mu.Lock()
		if c.closing {
			c.mu.Unlock()
			return nil, false, errors.New("soulseek: client closed")
		}
		if a := c.uploads[key]; a != nil {
			c.mu.Unlock()
			a.mu.Lock()
			terminal := a.state != "queued" && a.state != "running"
			a.mu.Unlock()
			if !terminal {
				return a, false, nil
			}
			// Both peer requeues and manual retries must join retiring workers.
			<-a.done
			continue
		}
		c.uploadSeq++
		ctx, cancel := context.WithCancel(c.uploadRoot)
		a := &uploadAttempt{target: UploadTarget{Username: username, Filename: wire, Attempt: c.uploadSeq}, key: key, localPath: localPath, ctx: ctx, cancel: cancel, done: make(chan struct{}), state: "queued"}
		a.job = c.cfg.Uploads.Enqueue(username, TransferRequest{Direction: 1, Token: randomToken(), Filename: wire, Size: size})
		c.uploads[key] = a
		c.uploadWG.Add(1)
		c.mu.Unlock()
		c.emitUpload(a, "queued", 0, "")
		go c.executeUpload(a)
		return a, true, nil
	}
}

// QueueUpload registers a manual retry; network work belongs to the client.
func (c *Client) QueueUpload(username, filename string) (bool, error) {
	_, started, err := c.registerUpload(username, filename)
	return started, err
}

func (c *Client) executeUpload(a *uploadAttempt) {
	defer c.uploadWG.Done()
	setupCtx, setupCancel := context.WithTimeout(a.ctx, 30*time.Minute)
	err := c.cfg.Uploads.Wait(setupCtx, a.job)
	if err == nil {
		err = a.ctx.Err()
	}
	if err == nil {
		a.mu.Lock()
		a.state = "running"
		a.mu.Unlock()
		c.emitUpload(a, "running", 0, "")
		err = c.performUpload(a, setupCtx, setupCancel)
	}
	setupCancel()
	a.cancel()
	// StopUploads holds c.mu while marking the entire batch; finish only after it.
	c.mu.Lock()
	a.mu.Lock()
	state, progress, message := "completed", a.progress, ""
	if a.manual {
		state = "cancelled"
	} else if err != nil {
		state, message = "failed", err.Error()
		if c.closing {
			message = "soulseek: upload connection closed"
		}
	} else {
		progress = a.job.Request.Size
	}
	a.state, a.progress = state, progress
	notify := a.notify
	a.mu.Unlock()
	c.mu.Unlock()
	c.emitUpload(a, state, progress, message)
	// Cancellation delivery is bounded and independent of the stopped attempt.
	if notify {
		c.notifyUpload(a, QueueDenied{Filename: a.target.Filename, Reason: "Cancelled"})
	}
	if state == "failed" {
		c.notifyUpload(a, QueueFailedMessage{Filename: a.target.Filename})
	}
	c.cfg.Uploads.Done(a.job)
	c.mu.Lock()
	if c.uploads[a.key] == a {
		delete(c.uploads, a.key)
	}
	close(a.done)
	c.mu.Unlock()
}

func (c *Client) performUpload(a *uploadAttempt, setupCtx context.Context, setupCancel context.CancelFunc) error {
	peer, err := c.connectUser(setupCtx, a.target.Username)
	if err != nil {
		return err
	}
	stopPeer := context.AfterFunc(setupCtx, func() { _ = peer.Close() })
	defer stopPeer()
	defer peer.Close()
	if err := writeMessage(peer, a.job.Request); err != nil {
		return err
	}
	// This dedicated connection has exactly one reader.
	command, payload, err := ReadFrame(peer)
	if err != nil {
		return err
	}
	if command != PeerTransferResponse {
		return fmt.Errorf("%w: expected transfer response", ErrMalformed)
	}
	response, err := DecodeTransferResponse(payload)
	if err != nil {
		return err
	}
	if response.Token != a.job.Request.Token {
		return ErrMalformed
	}
	if !response.Accepted {
		if response.Reason == "" {
			response.Reason = "upload denied"
		}
		return errors.New(response.Reason)
	}
	_ = peer.Close()
	filePeer, err := c.connectUserType(setupCtx, a.target.Username, "F")
	if err != nil {
		return err
	}
	stopFile := context.AfterFunc(a.ctx, func() { _ = filePeer.Close() })
	defer stopFile()
	defer filePeer.Close()
	// Deadlines cover setup without closing a successful stream when setupCancel runs.
	if deadline, ok := setupCtx.Deadline(); ok {
		_ = filePeer.SetDeadline(deadline)
	}
	var token [4]byte
	binary.LittleEndian.PutUint32(token[:], a.job.Request.Token)
	if err := writeAll(filePeer, token[:]); err != nil {
		return err
	}
	var offsetBytes [8]byte
	if _, err := io.ReadFull(filePeer, offsetBytes[:]); err != nil {
		return err
	}
	offset := binary.LittleEndian.Uint64(offsetBytes[:])
	if offset > a.job.Request.Size {
		return ErrMalformed
	}
	setupCancel()
	if err := filePeer.SetDeadline(time.Time{}); err != nil {
		return err
	}
	a.mu.Lock()
	a.progress = offset
	a.mu.Unlock()
	c.emitUpload(a, "running", offset, "")
	writer := c.cfg.Uploads.LimitWriter(a.ctx, a.job, uploadProgressWriter{Writer: filePeer, client: c, attempt: a})
	return SendFile(a.ctx, filepath.Dir(a.localPath), filepath.Base(a.localPath), writer, a.job.Request.Size, offset, nil)
}

// Count bytes accepted by the socket even if a paced write is interrupted.
type uploadProgressWriter struct {
	io.Writer
	client  *Client
	attempt *uploadAttempt
}

func (w uploadProgressWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n > 0 {
		a := w.attempt
		a.mu.Lock()
		a.progress += uint64(n)
		done := a.progress
		a.mu.Unlock()
		w.client.emitUpload(a, "running", done, "")
	}
	return n, err
}

func (c *Client) notifyUpload(a *uploadAttempt, message Message) {
	ctx, cancel := context.WithTimeout(c.uploadRoot, 2*time.Second)
	defer cancel()
	peer, err := c.connectUser(ctx, a.target.Username)
	if err != nil {
		return
	}
	defer peer.Close()
	stop := context.AfterFunc(ctx, func() { _ = peer.Close() })
	defer stop()
	_ = writeMessage(peer, message)
}

// StopUploads marks every target before releasing any scheduler slot, then joins
// all stopped workers. Unknown or retired attempt identities are harmless.
func (c *Client) StopUploads(targets []UploadTarget, notify bool) {
	var stopped []*uploadAttempt
	var jobs []*UploadJob
	c.mu.Lock()
	seen := make(map[*uploadAttempt]bool)
	for _, target := range targets {
		a := c.uploads[downloadKey(target.Username, target.Filename)]
		if a == nil || a.target.Attempt != target.Attempt || seen[a] {
			continue
		}
		seen[a] = true
		stopped = append(stopped, a)
		a.mu.Lock()
		if a.state == "queued" || a.state == "running" {
			a.manual, a.notify = true, notify && a.state == "queued"
			jobs = append(jobs, a.job)
		}
		a.mu.Unlock()
	}
	c.cfg.Uploads.CancelJobs(jobs)
	for _, a := range stopped {
		a.cancel()
	}
	c.mu.Unlock()
	for _, a := range stopped {
		<-a.done
	}
}

func (c *Client) failPendingDownload(username, filename string, err error) {
	if _, cleanErr := NormalizePath(filename); cleanErr != nil {
		return
	}
	c.mu.Lock()
	pending := c.requested[downloadKey(username, filename)]
	c.mu.Unlock()
	if pending != nil {
		pending.finish(err)
	}
}
