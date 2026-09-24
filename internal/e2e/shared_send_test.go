//go:build communitye2e

package e2e

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestSharedFolderSendTerminalPartialOutcomes(t *testing.T) {
	var wireMu sync.Mutex
	pmPayload := testutil.SocialFixture(t, "pm-online").Payload(t)
	failIf(t, pmPayload == nil, "missing pm-online fixture")
	pm, err := soulseek.DecodePrivateMessage(pmPayload)
	must(t, err)
	payload := strings.Repeat("payload", 1<<20)
	serverConn := make(chan net.Conn, 1)
	burstDone := make(chan struct{})
	var acknowledged atomic.Int32
	send := func(conn net.Conn, message soulseek.Message) error {
		wireMu.Lock()
		defer wireMu.Unlock()
		data, err := soulseek.EncodeMessage(message)
		if err != nil {
			return err
		}
		_, err = io.Copy(conn, bytes.NewReader(data))
		return err
	}
	var offers atomic.Int32
	received := make(chan []byte, 2)
	peer := testutil.ListenScript(t, func(ctx context.Context, conn net.Conn) error {
		_, body, err := soulseek.ReadInitFrame(conn)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		d := soulseek.NewDecoder(body)
		_ = d.String()
		kind := d.String()
		switch kind {
		case "P":
			for {
				code, body, err := soulseek.ReadFrame(conn)
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
				if code == soulseek.PeerUploadFailed || code == soulseek.PeerUploadDenied {
					continue
				}
				if code != soulseek.PeerTransferRequest {
					return fmt.Errorf("unexpected peer request %d", code)
				}
				req, err := soulseek.DecodeTransferRequest(body)
				if err != nil {
					return err
				}
				if req.Direction != 1 {
					return fmt.Errorf("not an upload offer")
				}
				offers.Add(1)
				response := soulseek.TransferResponse{Token: req.Token, Accepted: true}
				if strings.HasSuffix(req.Filename, `\deny`) {
					response.Accepted = false
					response.Reason = "Cancelled"
				}
				if err := send(conn, response); err != nil {
					return err
				}
			}
		case "F":
			var token [4]byte
			if _, err := io.ReadFull(conn, token[:]); err != nil {
				return err
			}
			var offset [8]byte
			if _, err := conn.Write(offset[:]); err != nil {
				return err
			}
			first := make([]byte, 1)
			if _, err := io.ReadFull(conn, first); err != nil {
				return err
			}
			var server net.Conn
			select {
			case server = <-serverConn:
			case <-ctx.Done():
				return ctx.Err()
			}
			// Keep the large F stream active while the server dispatches a burst.
			for i := uint32(0); i < 256; i++ {
				body := append([]byte(nil), pmPayload...)
				binary.LittleEndian.PutUint32(body, 10000+i)
				wireMu.Lock()
				err := testutil.WritePacket(server, 22, body)
				wireMu.Unlock()
				if err != nil {
					return err
				}
			}
			select {
			case <-burstDone:
			case <-ctx.Done():
				return ctx.Err()
			}
			data, err := io.ReadAll(conn)
			if err == nil {
				received <- append(first, data...)
			}
			return err
		}
		return fmt.Errorf("unexpected peer channel")
	})
	server := testutil.ListenScript(t, func(_ context.Context, conn net.Conn) error {
		for {
			code, body, err := soulseek.ReadFrame(conn)
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			switch code {
			case 23:
				if len(body) == 4 && binary.LittleEndian.Uint32(body) >= 10000 && binary.LittleEndian.Uint32(body) < 10256 && acknowledged.Add(1) == 256 {
					close(burstDone)
				}
			case soulseek.ServerLogin:
				serverConn <- conn
				if err := send(conn, soulseek.LoginResponse{Success: true, IP: 0x7f000001}); err != nil {
					return err
				}
			case soulseek.ServerGetPeerAddress:
				d := soulseek.NewDecoder(body)
				user := d.String()
				if err := send(conn, soulseek.PeerAddress{Username: user, IP: "127.0.0.1", Port: uint32(peer.Listener.Addr().(*net.TCPAddr).Port)}); err != nil {
					return err
				}
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	ctx := context.Background()
	var summary daemon.CommunitySummary
	h.wait("connected", func() bool {
		var err error
		summary, err = h.client.CommunitySummary(ctx)
		return err == nil && summary.Connected
	})
	root := filepath.Join(h.root, "shared")
	must(t, os.Mkdir(root, 0700))
	for _, name := range []string{"accept", "deny"} {
		must(t, os.WriteFile(filepath.Join(root, name), []byte(payload), 0600))
	}
	if _, err := h.client.AddShare(ctx, config.Share{Name: "Music", Path: root}); err != nil {
		t.Fatal(err)
	}
	h.wait("indexed selection", func() bool {
		out, err := h.client.PreviewSharedSend(ctx, daemon.SharedSendRequest{CommunityIdentity: summary.CommunityIdentity, RequestID: "probe", Username: "receiver", Folder: "Music"})
		return err == nil && out.Total == 2
	})
	if _, err := h.client.OpenCommunityConversation(ctx, daemon.CommunityOpenConversationRequest{CommunityIdentity: summary.CommunityIdentity, Username: "receiver"}); err != nil {
		t.Fatal(err)
	}
	h.attach("shared-send", 120, 40)
	h.screen("shared-send", "No matching results")
	h.command("send-keys", "-t", "shared-send", "Tab", "Tab", "Tab", "Tab")
	h.screen("shared-send", "receiver")
	h.command("send-keys", "-t", "shared-send", "U")
	h.screen("shared-send", "Send shared files / folder")
	h.command("send-keys", "-t", "shared-send", "Down", "Down", "Down", "Down", "Down", "Down", "Down", "Enter")
	h.screen("shared-send", "Music")
	h.command("send-keys", "-t", "shared-send", "s")
	h.screen("shared-send", "Recipient:")
	h.screen("shared-send", "receiver")
	h.command("send-keys", "-t", "shared-send", "Enter")
	h.screen("shared-send", "Shared send · preview · 2 files · 2 permitted")
	screen := h.command("capture-pane", "-p", "-t", "shared-send")
	requestID := ""
	for _, line := range strings.Split(screen, "\n") {
		if i := strings.Index(line, "Request: "); i >= 0 {
			if fields := strings.Fields(line[i+len("Request: "):]); len(fields) > 0 {
				requestID = fields[0]
			}
		}
	}
	failIf(t, requestID == "", "missing request ID", screen)
	h.command("send-keys", "-t", "shared-send", "s")
	h.screen("shared-send", "[Cancel]")
	h.command("send-keys", "-t", "shared-send", "Enter")
	h.screen("shared-send", "Shared send · preview")
	failIf(t, offers.Load() != 0, "default confirmation sent files")
	h.command("send-keys", "-t", "shared-send", "s")
	h.screen("shared-send", "[Cancel]")
	h.command("send-keys", "-t", "shared-send", "Right", "Enter")
	h.wait("partial transfer outcomes", func() bool {
		out, err := h.client.SharedSend(ctx, summary.CommunityIdentity, requestID, 0)
		return err == nil && out.State == "completed" && out.Files[0].State == "completed" && out.Files[1].State == "failed"
	})
	h.wait("received expected bytes", func() bool {
		select {
		case data := <-received:
			failIf(t, string(data) != payload, "incorrect file bytes")
			return true
		default:
			return false
		}
	})
	h.command("send-keys", "-t", "shared-send", "r")
	h.screen("shared-send", `[failed] "Music\\deny"`)
	failIf(t, offers.Load() != 2, "duplicate or missing offers", offers.Load())
	conversation, err := h.client.OpenCommunityConversation(ctx, daemon.CommunityOpenConversationRequest{CommunityIdentity: summary.CommunityIdentity, Username: pm.Username})
	must(t, err)
	page, err := h.client.CommunityMessages(ctx, daemon.CommunityMessagesRequest{CommunityIdentity: summary.CommunityIdentity, ConversationID: conversation.ID, Limit: 200})
	failIf(t, err != nil || len(page.Messages) != 200 || page.NextCursor == 0, "burst history lost or unbounded", err, len(page.Messages))
	older, err := h.client.CommunityMessages(ctx, daemon.CommunityMessagesRequest{CommunityIdentity: summary.CommunityIdentity, ConversationID: conversation.ID, Limit: 200, Cursor: page.NextCursor})
	failIf(t, err != nil || len(older.Messages) != 56 || older.NextCursor != 0 || acknowledged.Load() != 256, "burst history pagination or ACK mismatch", err, len(older.Messages))
}
