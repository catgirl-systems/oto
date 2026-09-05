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
	"syscall"
	"time"
)

type TransferEvent struct {
	Restored                             bool
	Direction, Username, Filename, State string
	Attempt                              uint64
	Done, Total                          uint64
	Fingerprint                          string
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
	fingerprint    string
	job            *UploadJob
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	mu             sync.Mutex
	state          string
	progress       uint64
	manual, notify bool
	fileStarted    bool
}

func (c *Client) emitUpload(a *uploadAttempt, state string, done uint64, message string) {
	if callback := c.cfg.UploadUpdate; callback != nil {
		callback(TransferEvent{Direction: "upload", Username: a.target.Username, Filename: a.target.Filename, Attempt: a.target.Attempt, State: state, Done: done, Total: a.job.Request.Size, Fingerprint: a.fingerprint, Error: message})
	}
	// Events are informational; the callback above owns authoritative bookkeeping.
	c.emit(Event{Command: PeerTransferRequest, Message: TransferEvent{Direction: "upload", Username: a.target.Username, Filename: a.target.Filename, Attempt: a.target.Attempt, State: state, Done: done, Total: a.job.Request.Size, Fingerprint: a.fingerprint, Error: message}})
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

func fileFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("soulseek: file fingerprint unavailable")
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d", stat.Dev, stat.Ino, info.Size(), info.ModTime().UnixNano(), stat.Ctim.Sec*1e9+stat.Ctim.Nsec), nil
}

func (c *Client) registerUpload(username, filename string, peerRequeue bool) (*uploadAttempt, bool, error) {
	return c.registerUploadWithOptions(username, filename, peerRequeue, false, "")
}

func (c *Client) registerUploadWithOptions(username, filename string, peerRequeue, restored bool, expectedFingerprint string) (*uploadAttempt, bool, error) {
	if !restored && c.cfg.UploadsReady != nil {
		select {
		case <-c.cfg.UploadsReady:
		case <-c.uploadRoot.Done():
			return nil, false, c.uploadRoot.Err()
		}
	}
	c.uploadAdmissionMu.Lock()
	defer c.uploadAdmissionMu.Unlock()
	for {
		wire, localPath, size, err := c.validateUpload(username, filename)
		if err != nil {
			return nil, false, err
		}
		fingerprint, err := fileFingerprint(localPath)
		if err != nil {
			return nil, false, err
		}
		if expectedFingerprint != "" && fingerprint != expectedFingerprint {
			return nil, false, errors.New("soulseek: shared file changed")
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
			// A peer can retry before we notice its old stream has closed.
			// Replace running attempts, but keep queued requests deduplicated.
			if peerRequeue && a.state == "running" {
				a.manual = true
				a.cancel()
				terminal = true
			}
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
		a.fingerprint = fingerprint
		request := TransferRequest{Direction: 1, Token: randomToken(), Filename: wire, Size: size}
		if restored {
			a.job = c.cfg.Uploads.EnqueueRestored(username, request)
		} else {
			a.job, err = c.cfg.Uploads.TryEnqueue(username, request)
		}
		if err != nil || a.job == nil {
			c.mu.Unlock()
			cancel()
			if err == nil {
				err = ErrTooManyUploadBytes
			}
			if reject := c.cfg.UploadRejected; reject != nil {
				reject(TransferEvent{Direction: "upload", Username: username, Filename: wire, Total: size, Error: err.Error()})
			}
			return nil, false, err
		}
		c.uploads[key] = a
		c.uploadWG.Add(1)
		c.mu.Unlock()
		if accept := c.cfg.UploadAccepted; accept != nil {
			if err := accept(TransferEvent{Restored: restored, Direction: "upload", Username: username, Filename: wire, Attempt: a.target.Attempt, State: "queued", Total: size, Fingerprint: fingerprint}); err != nil {
				cancel()
				c.cfg.Uploads.Done(a.job)
				c.mu.Lock()
				delete(c.uploads, key)
				close(a.done)
				c.mu.Unlock()
				c.uploadWG.Done()
				return nil, false, err
			}
		}
		c.emitUpload(a, "queued", 0, "")
		go c.executeUpload(a)
		return a, true, nil
	}
}

// RestoreUpload re-admits previously accepted work with fresh protocol tokens.
func (c *Client) RestoreUpload(username, filename, fingerprint string) (bool, error) {
	_, started, err := c.registerUploadWithOptions(username, filename, false, true, fingerprint)
	return started, err
}

func uploadDenial(err error) string {
	if errors.Is(err, ErrTooManyUploadFiles) || errors.Is(err, ErrTooManyUploadBytes) {
		return err.Error()
	}
	return "File not shared"
}

// QueueUpload registers a manual retry; network work belongs to the client.
func (c *Client) QueueUpload(username, filename string) (bool, error) {
	_, started, err := c.registerUpload(username, filename, false)
	return started, err
}

func (c *Client) executeUpload(a *uploadAttempt) {
	defer c.uploadWG.Done()
	setupCtx, setupCancel := context.WithTimeout(a.ctx, 30*time.Minute)
	if c.cfg.UploadsReady != nil {
		select {
		case <-c.cfg.UploadsReady:
		case <-setupCtx.Done():
		}
	}
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
	notify, fileStarted := a.notify, a.fileStarted
	a.mu.Unlock()
	c.mu.Unlock()
	c.emitUpload(a, state, progress, message)
	// Cancellation delivery is bounded and independent of the stopped attempt.
	if notify {
		c.notifyUpload(a, QueueDenied{Filename: a.target.Filename, Reason: "Cancelled"})
	}
	// F closure already reports a token-bound failure. A filename-only message
	// on a new P connection could abort a replacement download instead.
	if state == "failed" && !fileStarted {
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
	a.mu.Lock()
	a.fileStarted = true
	a.mu.Unlock()
	var offsetBytes [8]byte
	if _, err := io.ReadFull(filePeer, offsetBytes[:]); err != nil {
		return err
	}
	offset := binary.LittleEndian.Uint64(offsetBytes[:])
	if offset > a.job.Request.Size {
		return ErrMalformed
	}
	// A queued/setup attempt may outlive a share-policy publication.
	_, local, size, err := c.validateUpload(a.target.Username, a.target.Filename)
	if err != nil {
		return err
	}
	fingerprint, fingerprintErr := fileFingerprint(local)
	if fingerprintErr != nil || fingerprint != a.fingerprint || local != a.localPath || size != a.job.Request.Size {
		return errors.New("soulseek: shared file changed before stream start")
	}
	setupCancel()
	if err := filePeer.SetDeadline(time.Time{}); err != nil {
		return err
	}
	a.mu.Lock()
	a.progress = offset
	a.mu.Unlock()
	if callback := c.cfg.UploadStreamStart; callback != nil {
		callback(TransferEvent{Direction: "upload", Username: a.target.Username, Filename: a.target.Filename, Attempt: a.target.Attempt, State: "running", Done: offset, Total: a.job.Request.Size})
	}
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
