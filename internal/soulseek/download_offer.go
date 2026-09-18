package soulseek

import (
	"context"
	"io"
	"math"
	"net"
	"net/netip"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// DownloadOffer is a single-use incoming offer, not permission to receive it.
// DownloadOffered must durably admit it before starting ReceiveOfferedFile.
// Queue limits, consent, bans, destinations and persistence belong to the caller.
type DownloadOffer struct {
	client             *Client
	peer               net.Conn
	retained           *peerLease
	username, filename string
	request            TransferRequest
	expires            time.Time
	used               atomic.Bool
}

func (o *DownloadOffer) Username() string    { return o.username }
func (o *DownloadOffer) Filename() string    { return o.filename }
func (o *DownloadOffer) Size() uint64        { return o.request.Size }
func (o *DownloadOffer) Address() netip.Addr { return peerIP(o.peer.RemoteAddr()) }

// Reject withdraws an offer which has not started. It never cancels an accepted file.
func (o *DownloadOffer) Reject() {
	if o == nil || !o.used.CompareAndSwap(false, true) {
		return
	}
	_ = o.peer.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = writeMessage(o.peer, TransferResponse{Token: o.request.Token, Reason: "Cancelled"})
	_ = o.peer.SetWriteDeadline(time.Time{})
	o.releaseLease()
}

// releaseLease drops the independent lease held for a deferred response. The
// pending file itself arrives on its own connection, so the lease is only
// needed until the accept or reject response has been written.
func (o *DownloadOffer) releaseLease() {
	if o.retained == nil {
		return
	}
	lease := o.retained
	o.retained = nil
	_ = lease.Close()
}

func (c *Client) offerDownload(peer net.Conn, username, filename string, request TransferRequest) {
	offer := &DownloadOffer{client: c, peer: peer, username: username, filename: filename, request: request, expires: time.Now().Add(downloadSetupTimeout)}
	// The message handler's lease is closed as soon as it returns, so hold an
	// independent one until the offer is accepted or rejected.
	if lease, ok := peer.(*peerLease); ok {
		if retained := lease.retain(); retained != nil {
			offer.peer, offer.retained = retained, retained
		}
	}
	if c.cfg.DownloadOffered == nil || username == "" || len(username) > 1024 || !utf8.ValidString(username) || len(filename) > 16<<10 || !utf8.ValidString(filename) || request.Size > math.MaxInt64 {
		offer.Reject()
		return
	}
	if err := c.cfg.DownloadOffered(offer); err != nil {
		offer.Reject()
	}
}

// ReceiveOfferedFile uses the normal F-channel receiver, limits and progress
// machinery without requesting the file again. The original offer is accepted
// only here, after the caller has persisted admission and opened its safe writer.
func (c *Client) ReceiveOfferedFile(ctx context.Context, offer *DownloadOffer, offset uint64, dst io.WriterAt, progress ProgressFunc, start func()) error {
	return c.ReceiveOfferedFileWithAuthorization(ctx, offer, offset, dst, progress, start, nil)
}

// ReceiveOfferedFileWithAuthorization rechecks live policy against the actual peer.
func (c *Client) ReceiveOfferedFileWithAuthorization(ctx context.Context, offer *DownloadOffer, offset uint64, dst io.WriterAt, progress ProgressFunc, start func(), authorize func(netip.Addr) error) error {
	if offer == nil || offer.client != c || dst == nil || offset > offer.Size() {
		return ErrMalformed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if time.Now().After(offer.expires) {
		offer.Reject()
		return context.DeadlineExceeded
	}
	if !offer.used.CompareAndSwap(false, true) {
		return ErrMalformed
	}
	return c.downloadWithStart(ctx, offer.username, offer.filename, offer.Size(), offset, dst, progress, start, offer, authorize)
}

// DownloadWithAuthorization resumes an admitted download, rechecking the actual
// peer address before both the queue request and acceptance of the file offer.
func (c *Client) DownloadWithAuthorization(ctx context.Context, username, filename string, size, offset uint64, dst io.WriterAt, progress ProgressFunc, start func(), authorize func(netip.Addr) error) error {
	return c.downloadWithStart(ctx, username, filename, size, offset, dst, progress, start, nil, authorize)
}
