//go:build communitye2e

package e2e

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/testutil"
)

func TestCommunityNicotineProfiles(t *testing.T) {
	fixtures := testutil.SocialFixtureMap(t, "login-ok", "peer-address", "watch-online", "profile-picture")
	var mu sync.Mutex
	ports := map[string]uint32{}
	server := testutil.ListenScript(t, func(ctx context.Context, c net.Conn) error {
		login, err := testutil.WaitForPacket(c, 1)
		if err != nil {
			return err
		}
		if len(login) < 4 {
			return fmt.Errorf("short test login")
		}
		n := int(binary.LittleEndian.Uint32(login))
		if n > 32 || n > len(login)-4 {
			return fmt.Errorf("invalid test login")
		}
		username := string(login[4 : 4+n])
		if username != "terminal" && username != "reference" {
			return fmt.Errorf("unexpected local account")
		}
		if err := testutil.WritePacket(c, 1, fixtures["login-ok"].Payload(t)); err != nil {
			return err
		}
		for {
			code, data, err := testutil.ReadPacket(c)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			switch code {
			case 2:
				if len(data) < 4 {
					return fmt.Errorf("short listen port")
				}
				mu.Lock()
				ports[username] = binary.LittleEndian.Uint32(data)
				mu.Unlock()
			case 3, 5, 7:
				if len(data) < 4 || int(binary.LittleEndian.Uint32(data)) != len(data)-4 {
					return fmt.Errorf("invalid user request %d", code)
				}
				target := string(data[4:])
				if target != "terminal" && target != "reference" {
					return fmt.Errorf("unexpected local target")
				}
				response := append([]byte(nil), data...)
				switch code {
				case 3:
					// Reference address frame with only the exact user and ephemeral port varied.
					response = append(response, fixtures["peer-address"].Payload(t)[9:]...)
					mu.Lock()
					port := ports[target]
					mu.Unlock()
					if port == 0 {
						return fmt.Errorf("peer has not announced its port")
					}
					binary.LittleEndian.PutUint32(response[len(data)+4:], port)
				case 5:
					response = append(response, fixtures["watch-online"].Payload(t)[9:]...)
				case 7:
					response = binary.LittleEndian.AppendUint32(response, 2)
					response = append(response, 0)
				}
				if err := testutil.WritePacket(c, code, response); err != nil {
					return err
				}
			}
		}
	})
	h := newTerminal(t, server.Listener.Addr().String())
	dir := t.TempDir()
	picture, err := hex.DecodeString(fixtures["profile-picture"].Arguments["pic"].(map[string]any)["hex"].(string))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, "picture.png"), picture, 0600))
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	root, err := filepath.Abs("../..")
	must(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	logPath := filepath.Join(dir, "reference.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0600)
	must(t, err)
	command := exec.CommandContext(ctx, "python3", "-B", filepath.Join(root, "scripts/nicotine-profile-peer.py"), filepath.Join(root, "nicotine-plus"), dir, server.Listener.Addr().String(), strconv.Itoa(port))
	command.Env = h.env
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		_ = log.Close()
		t.Fatal("mandatory Nicotine peer", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if !finished {
			cancel()
			<-done
		}
		_ = log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("Isolated Nicotine peer:\n%s", data)
		}
	})
	h.wait("reference login", func() bool {
		select {
		case err := <-done:
			finished = true
			data, _ := os.ReadFile(logPath)
			t.Fatalf("reference exited early: %v\n%s", err, data)
		default:
		}
		_, err := os.Stat(filepath.Join(dir, "ready.json"))
		return err == nil
	})
	h.wait("reference listening", func() bool { mu.Lock(); defer mu.Unlock(); return ports["reference"] != 0 })
	summary, err := h.client.CommunitySummary(ctx)
	must(t, err)
	request := daemon.CommunityProfileRequest{CommunityIdentity: summary.CommunityIdentity, Username: "reference", Frontend: "nicotine-interop"}
	if _, err := h.client.StartCommunityProfile(ctx, request); err != nil {
		t.Fatal(err)
	}
	var profile daemon.CommunityProfile
	h.wait("oto reads real Nicotine profile", func() bool {
		profile, err = h.client.CommunityProfile(ctx, request)
		return err == nil && profile.State == "ready"
	})
	failIfFmt(t, profile.Description != "Nicotine reference 世界" || profile.PictureType != "image/png" || profile.PictureWidth != 1 || profile.PictureHeight != 1 || !profile.UploadAllowedKnown || profile.UploadAllowed != 0, "reference profile metadata: %+v", profile)
	image, err := h.client.CommunityProfilePicture(ctx, daemon.CommunityProfilePictureRequest{CommunityIdentity: summary.CommunityIdentity, Username: "reference", Revision: profile.PictureRevision})
	failIf(t, err != nil || !bytes.Equal(image.Data, picture), "reference picture bytes changed", err)
	current, err := h.client.CommunitySelfProfile(ctx, summary.CommunityIdentity)
	must(t, err)
	current.Description = "oto reference 世界"
	if _, err := h.client.SetCommunitySelfProfile(ctx, current); err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(dir, "request"), nil, 0600))
	h.wait("Nicotine reads oto profile", func() bool { _, err := os.Stat(filepath.Join(dir, "response.json")); return err == nil })
	var response struct {
		Username, Description string
		UploadAllowed         uint32 `json:"upload_allowed"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "response.json"))
	must(t, err)
	must(t, json.Unmarshal(data, &response))
	failIfFmt(t, response.Username != "terminal" || response.Description != current.Description || response.UploadAllowed != 0, "Nicotine parsed oto profile: %+v", response)
	verifyNicotineSharePermissions(t, h, ctx, dir)
	verifyNicotineSharedSend(t, h, ctx, dir)
	verifyNicotineReceiving(t, h, ctx, dir)
	must(t, os.WriteFile(filepath.Join(dir, "stop"), nil, 0600))
	select {
	case err := <-done:
		finished = true
		if err != nil {
			t.Fatal("reference shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reference did not stop")
	}
	logs, err := os.ReadFile(logPath)
	must(t, err)
	failIfFmt(t, bytes.Contains(logs, []byte("Traceback")), "reference subprocess failed:\n%s", logs)
}
