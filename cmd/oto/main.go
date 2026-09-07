package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/diagnostics"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/tui"
)

const sourceURL = "https://github.com/catgirl-systems/oto"

var (
	version    = "dev"
	executable = os.Executable
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if len(os.Args) > 1 && os.Args[1] == "daemon" {
			diagnostics.StartupError(os.Stderr, err)
		} else {
			fmt.Fprintln(os.Stderr, "oto:", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "daemon":
			return daemonCommand(args[1:])
		case "status":
			return statusCommand(args[1:])
		case "transfers":
			return transfersCommand(args[1:])
		case "pause":
			return transferControlCommand("pause", args[1:])
		case "resume":
			return transferControlCommand("resume", args[1:])
		case "rescan":
			return rescanCommand(args[1:])
		case "help", "--help", "-h":
			usage()
			return nil
		case "version", "--version":
			fmt.Println("oto", version, sourceURL, "AGPL-3.0-only; no warranty")
			return nil
		default:
			if args[0] == "" || args[0][0] != '-' {
				return fmt.Errorf("unknown command %q (try 'oto help')", args[0])
			}
		}
	}
	return tuiCommand(args)
}

func usage() {
	fmt.Printf("oto — Soulseek search, browse, shares, and transfers\n\nUsage:\n  oto [--config PATH]\n  oto daemon [--config PATH] [--share-rescan-delay DURATION] [--listen-port-file PATH] [--listen-port-reconcile-interval DURATION]\n  oto status [--json]\n  oto transfers [--json]\n  oto pause DOWNLOAD_ID\n  oto resume DOWNLOAD_ID\n  oto rescan [--cancel]\n\nSource: %s\nLicense: AGPL-3.0-only; no warranty.\n", sourceURL)
}

func configFlag(fs *flag.FlagSet) *string {
	return fs.String("config", config.ConfigPath(), "config file")
}

func tuiCommand(args []string) error {
	fs := flag.NewFlagSet("oto", flag.ContinueOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("oto: unexpected arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if cfg.Soulseek.Username == "" || cfg.Soulseek.Password == "" {
		if err := tui.RunSetup(ctx, *path); err != nil {
			return err
		}
	}

	client := ipc.NewClient(config.SocketPath())
	probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	_, err = client.Status(probe)
	cancel()
	transient := false
	var child *exec.Cmd
	var keepAlive io.Closer
	if err != nil {
		child, keepAlive, err = startChild(ctx, *path)
		if err != nil {
			return err
		}
		transient = true
		defer func() {
			waitForChild(child, keepAlive, client, os.Stderr)
		}()
	}
	return tui.RunWithTransient(ctx, client, *path, transient)
}

type tailWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf == nil {
		w.buf = make([]byte, 0, 32<<10)
	}
	n := len(p)
	if len(p) >= 32<<10 {
		w.buf = append(w.buf[:0], p[len(p)-(32<<10):]...)
	} else {
		if excess := len(w.buf) + len(p) - (32 << 10); excess > 0 {
			copy(w.buf, w.buf[excess:])
			w.buf = w.buf[:len(w.buf)-excess]
		}
		w.buf = append(w.buf, p...)
	}
	return n, nil
}

func startChild(ctx context.Context, path string) (*exec.Cmd, io.Closer, error) {
	exe, err := executable()
	if err != nil {
		return nil, nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(exe, "daemon", "--child", "--config", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = reader
	cmd.Stdout = io.Discard
	tail := new(tailWriter)
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, nil, errors.New("daemon failed to start")
	}
	_ = reader.Close()
	abort := func(err error) (*exec.Cmd, io.Closer, error) {
		_ = writer.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		tail.mu.Lock()
		detail := string(tail.buf)
		tail.mu.Unlock()
		if detail != "" {
			err = fmt.Errorf("%w: %s", err, detail)
		}
		return nil, nil, err
	}

	client := ipc.NewClient(config.SocketPath())
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return abort(err)
		}
		probe, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		_, probeErr := client.Status(probe)
		cancel()
		if probeErr == nil {
			return cmd, writer, nil
		}
		if cmd.ProcessState != nil {
			return abort(errors.New("daemon startup failed"))
		}
		select {
		case <-ctx.Done():
			return abort(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	return abort(errors.New("daemon startup timed out"))
}

type daemonOptions struct {
	configPath                  string
	child                       bool
	shareScanDelay              time.Duration
	listenPortFile              string
	listenPortReconcileInterval time.Duration
}

func parseDaemonOptions(args []string) (daemonOptions, error) {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Invalid values can contain private paths or credentials.
	path := configFlag(fs)
	child := fs.Bool("child", false, "exit when stdin closes")
	delay := fs.Duration("share-rescan-delay", daemon.DefaultShareRescanDelay, "quiet period before automatically rescanning shares (0 disables)")
	listenPortFile := fs.String("listen-port-file", "", "file containing the current incoming listening port")
	listenPortInterval := fs.Duration("listen-port-reconcile-interval", daemon.DefaultListenPortReconcileInterval, "fallback interval for rereading the listening port file (0 disables)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(os.Stderr)
			fs.Usage()
		}
		return daemonOptions{}, err
	}
	if fs.NArg() != 0 {
		return daemonOptions{}, errors.New("daemon: unexpected arguments")
	}
	if *delay < 0 {
		return daemonOptions{}, errors.New("share rescan delay cannot be negative")
	}
	if *listenPortInterval < 0 {
		return daemonOptions{}, errors.New("listen port reconcile interval cannot be negative")
	}
	return daemonOptions{configPath: *path, child: *child, shareScanDelay: *delay, listenPortFile: *listenPortFile, listenPortReconcileInterval: *listenPortInterval}, nil
}

func daemonCommand(args []string) error {
	options, err := parseDaemonOptions(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(options.configPath)
	if err != nil {
		return err
	}
	service, err := daemon.New(cfg, filepath.Join(config.DataDir(), "state.sqlite3"))
	if err != nil {
		return err
	}
	defer service.Close()
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.Logging.Level))
	var mirror io.Writer
	if !options.child {
		mirror = os.Stderr
	}
	logs := diagnostics.New(filepath.Join(config.DataDir(), "logs"), level, mirror)
	if err := service.SetDiagnostics(logs); err != nil {
		logs.Close()
		return err
	}
	logger := logs.Logger()
	diagnostics.Event(logger, slog.LevelInfo, "daemon_started", nil, slog.String("version", version))
	if err := service.SetShareRescanDelay(options.shareScanDelay); err != nil {
		return err
	}
	if err := service.SetListenPortFile(options.listenPortFile, options.listenPortReconcileInterval); err != nil {
		return err
	}
	service.SetConfigPath(options.configPath)
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	var eof <-chan struct{}
	if options.child {
		parent, stop := daemon.ContextWithEOF(context.Background(), os.Stdin)
		defer stop()
		eof = parent.Done()
	}
	return runDaemon(service, ipc.NewServer(service, config.SocketPath()), signals, eof)
}

func transfersCommand(args []string) error {
	fs := flag.NewFlagSet("transfers", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("transfers: unexpected arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	transfers, err := ipc.NewClient(config.SocketPath()).Transfers(ctx)
	if err != nil {
		return fmt.Errorf("daemon unavailable: %w", err)
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(transfers)
	}
	for _, transfer := range transfers {
		fmt.Printf("%q direction=%q user=%q state=%q progress=%d/%d queue=%d error=%q filename=%q speed_bps=%d elapsed_ms=%s eta_seconds=%s\n", transfer.ID, transfer.Direction, transfer.Username, transfer.State, transfer.Done, transfer.Total, transfer.Queue, transfer.Error, transfer.Filename, transfer.SpeedBPS, optionalUint(transfer.ElapsedMS), optionalUint(transfer.ETASeconds))
	}
	return nil
}

func optionalUint(value *uint64) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprint(*value)
}

func transferControlCommand(action string, args []string) error {
	if len(args) != 1 || args[0] == "" || args[0][0] == '-' {
		return fmt.Errorf("%s requires exactly one download ID", action)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := ipc.NewClient(config.SocketPath()).TransferAction(ctx, args[0], action); err != nil {
		return fmt.Errorf("%s %q: %w", action, args[0], err)
	}
	return nil
}

func rescanCommand(args []string) error {
	if len(args) == 1 && args[0] == "--cancel" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client := ipc.NewClient(config.SocketPath())
		state, err := client.Status(ctx)
		if err != nil {
			return err
		}
		scan := state.ShareScan
		if scan == nil || (scan.State != "scanning" && scan.State != "cancelling" && scan.State != "publishing") {
			fmt.Println("No share scan is running.")
			return nil
		}
		if err := client.CancelShareScan(ctx, scan.ID); err != nil {
			return err
		}
		fmt.Printf("Cancellation requested for share scan %d.\n", scan.ID)
		return nil
	}
	if len(args) != 0 {
		return errors.New("rescan: unexpected arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := ipc.NewClient(config.SocketPath()).Rescan(ctx); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("stopped waiting; the daemon's share scan may still be running: %w", ctx.Err())
		}
		return fmt.Errorf("rescan: %w", err)
	}
	return nil
}

func statusCommand(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("status: unexpected arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := ipc.NewClient(config.SocketPath()).Status(ctx)
	if err != nil {
		return fmt.Errorf("daemon unavailable: %w", err)
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(s)
	}
	fmt.Printf("%s presence=%s user=%s shares=%d transfers=%d", s.Status, s.Presence, s.Config.Soulseek.Username, len(s.Shares), len(s.Transfers))
	if s.Shutdown != nil {
		fmt.Printf(" draining=%t active_uploads=%d", s.Shutdown.Draining, s.Shutdown.ActiveUploads)
	}
	if s.ShareScan != nil && (s.ShareScan.State == "scanning" || s.ShareScan.State == "publishing") {
		fmt.Printf(" scan=%s root=%q files=%d dirs=%d elapsed=%dms", s.ShareScan.State, s.ShareScan.Root, s.ShareScan.Files, s.ShareScan.Directories, s.ShareScan.ElapsedMS)
	}
	if s.Error != "" {
		fmt.Printf(" error=%q", s.Error)
	}
	fmt.Println()
	return nil
}
