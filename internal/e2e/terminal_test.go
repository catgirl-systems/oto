//go:build communitye2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/testutil"
)

var binaryPath string

// This suite is explicitly selected in CI. Missing tmux or build prerequisites
// fail, rather than turning mandatory acceptance workflows into passing skips.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Fprintln(os.Stderr, "Community E2E requires tmux:", err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "oto-e2e-build-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binaryPath = filepath.Join(dir, "oto")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "./cmd/oto")
	cmd.Dir = "../.."
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err = cmd.Run()
	cancel()
	if err != nil {
		_ = os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, "build oto:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type terminal struct {
	t          *testing.T
	root       string
	env        []string
	client     *ipc.Client
	configPath string
	screens    []string
	stopDaemon func()
}

func newTerminal(t *testing.T, server string) *terminal {
	t.Helper()
	// Short private paths also keep Unix socket names below sockaddr_un limits.
	root, err := os.MkdirTemp("", "oto-e2e-")
	must(t, err)
	h := &terminal{t: t, root: root, configPath: filepath.Join(root, "config.json")}
	h.env = []string{
		"PATH=" + os.Getenv("PATH"), "TERM=xterm-256color", "LANG=C.UTF-8", "NO_COLOR=1",
		"HOME=" + root, "XDG_CONFIG_HOME=" + root, "XDG_STATE_HOME=" + root,
		"XDG_RUNTIME_DIR=" + root, "XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
	}
	h.client = ipc.NewClient(filepath.Join(root, "oto", "oto.sock"))
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
	})
	t.Cleanup(func() {
		if t.Failed() {
			h.artifacts()
		}
		// Never talk to the user's tmux server or collect production diagnostics.
		_, _ = h.tmux("kill-server")
	})
	cfg := config.Default()
	cfg.Logging.Level = "DEBUG"
	cfg.Soulseek.Username, cfg.Soulseek.Password = "terminal", "local-test-only"
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	cfg.Soulseek.Server, cfg.Soulseek.ListenAddr = server, reserved.Addr().String()
	_ = reserved.Close()
	cfg.Soulseek.NATPMPPortMapping, cfg.Soulseek.UPnPPortMapping = false, false
	cfg.DownloadDir, cfg.AudioMetadata = filepath.Join(root, "downloads"), false
	must(t, cfg.Save(h.configPath))
	h.startDaemon()
	return h
}

func (h *terminal) startDaemon() {
	t := h.t
	root := h.root
	log, err := os.OpenFile(filepath.Join(root, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	must(t, err)
	cmd := exec.Command(binaryPath, "daemon", "--config", h.configPath)
	cmd.Env, cmd.Stdout, cmd.Stderr = h.env, log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	h.stopDaemon = func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("daemon failed to shut down")
		}
		_ = log.Close()
	}
	t.Cleanup(h.stopDaemon)
	h.wait("daemon ready", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		state, err := h.client.Status(ctx)
		return err == nil && state.Status == daemon.StatusConnected
	})
}

func (h *terminal) tmux(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", append([]string{"-S", filepath.Join(h.root, "tmux.sock"), "-f", "/dev/null"}, args...)...)
	cmd.Env = h.env
	b, err := cmd.CombinedOutput()
	return string(b), err
}

func (h *terminal) command(args ...string) string {
	h.t.Helper()
	out, err := h.tmux(args...)
	if err != nil {
		h.t.Fatalf("tmux %v: %v: %s", args, err, out)
	}
	return out
}

func (h *terminal) attach(name string, width, height int) {
	h.t.Helper()
	h.command("new-session", "-d", "-s", name, "-x", fmt.Sprint(width), "-y", fmt.Sprint(height),
		binaryPath, "--config", h.configPath)
}

func (h *terminal) wait(label string, check func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatalf("timed out: %s", label)
}

func (h *terminal) screen(name, text string) string {
	h.t.Helper()
	var screen string
	defer func() { h.screens = append(h.screens, name+": "+text+"\n"+screen) }()
	h.wait("screen containing "+text, func() bool {
		var err error
		screen, err = h.tmux("capture-pane", "-p", "-t", name)
		return err == nil && strings.Contains(screen, text)
	})
	return screen
}

func (h *terminal) artifacts() {
	h.t.Helper()
	screen, _ := h.tmux("capture-pane", "-p")
	logs, _ := os.ReadFile(filepath.Join(h.root, "daemon.log"))
	// Only this isolated synthetic account exists under root. Scrub even its
	// fixed credential so the artifact policy does not depend on logger behavior.
	scrub := func(s string) string { return strings.ReplaceAll(s, "local-test-only", "[redacted]") }
	data := scrub(strings.Join(h.screens, "\n") + "\nLast screen:\n" + screen + "\nDaemon:\n" + string(logs))
	h.t.Log(data)
	if dir := os.Getenv("OTO_E2E_ARTIFACTS"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			h.t.Error(err)
			return
		}
		name := strings.ReplaceAll(h.t.Name(), "/", "_") + "-" + filepath.Base(h.root) + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			h.t.Error(err)
		}
	}
}

func TestCommunityTerminalStartupNavigation(t *testing.T) {
	login := testutil.SocialFixture(t, "login-ok")
	failIf(t, login.Name == "", "required login fixture missing")
	payload := login.Payload(t)
	server := testutil.ListenScript(t, func(ctx context.Context, conn net.Conn) error {
		if _, err := testutil.WaitForPacket(conn, 1); err != nil {
			return err
		}
		if err := testutil.WritePacket(conn, 1, payload); err != nil {
			return err
		}
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	h := newTerminal(t, server.Listener.Addr().String())
	h.attach("first", 120, 40)
	h.screen("first", "No matching results")
	for _, text := range []string{"No wishlist items", "No saved share lists", "No downloads"} {
		h.command("send-keys", "-t", "first", "Tab")
		h.screen("first", text)
	}
	h.command("send-keys", "-t", "first", "BTab")
	h.screen("first", "No saved share lists")
	h.command("send-keys", "-t", "first", "q")
	h.wait("frontend detach", func() bool {
		_, err := h.tmux("has-session", "-t", "first")
		return err != nil
	})
	// The real daemon, not a fake model/API, must survive frontend detach.
	h.attach("second", 80, 24)
	h.screen("second", "No matching results")
	h.command("send-keys", "-t", "second", "q")
}
