package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
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
		fmt.Fprintln(os.Stderr, "oto:", err)
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
			_ = keepAlive.Close()
			done := make(chan struct{})
			go func() { _ = child.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = child.Process.Kill()
			}
		}()
	}
	return tui.RunWithTransient(ctx, client, *path, transient)
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
	cmd := exec.CommandContext(ctx, exe, "daemon", "--child", "--config", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = reader
	cmd.Stdout = io.Discard
	if err := os.MkdirAll(config.DataDir(), 0700); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(config.DataDir(), "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, nil, err
	}
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = reader.Close()
		_ = writer.Close()
		return nil, nil, err
	}
	_ = reader.Close()
	_ = logFile.Close()
	abort := func(err error) (*exec.Cmd, io.Closer, error) {
		_ = writer.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
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
			return abort(errors.New("daemon exited before opening its socket"))
		}
		select {
		case <-ctx.Done():
			return abort(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	return abort(errors.New("timed out waiting for daemon"))
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
	path := configFlag(fs)
	child := fs.Bool("child", false, "exit when stdin closes")
	delay := fs.Duration("share-rescan-delay", daemon.DefaultShareRescanDelay, "quiet period before automatically rescanning shares (0 disables)")
	listenPortFile := fs.String("listen-port-file", "", "file containing the current incoming listening port")
	listenPortInterval := fs.Duration("listen-port-reconcile-interval", daemon.DefaultListenPortReconcileInterval, "fallback interval for rereading the listening port file (0 disables)")
	if err := fs.Parse(args); err != nil {
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
	service, err := daemon.New(cfg, config.StatePath())
	if err != nil {
		return err
	}
	if err := service.SetShareRescanDelay(options.shareScanDelay); err != nil {
		return err
	}
	if err := service.SetListenPortFile(options.listenPortFile, options.listenPortReconcileInterval); err != nil {
		return err
	}
	service.SetConfigPath(options.configPath)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if options.child {
		ctx, stop = daemon.ContextWithEOF(ctx, os.Stdin)
		defer stop()
	}
	server := ipc.NewServer(service, config.SocketPath())
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(ctx) }()

	if err := service.Start(ctx); err != nil {
		log.Printf("connection: %v (retrying when appropriate)", err)
	}
	log.Printf("daemon foreground; socket=%s source=%s", config.SocketPath(), sourceURL)
	select {
	case <-ctx.Done():
	case err := <-serverErr:
		if err != nil {
			_ = service.Close()
			return err
		}
	}
	_ = service.Close()
	_ = server.Close()
	return nil
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
	if s.ShareScan != nil && (s.ShareScan.State == "scanning" || s.ShareScan.State == "publishing") {
		fmt.Printf(" scan=%s root=%q files=%d dirs=%d elapsed=%dms", s.ShareScan.State, s.ShareScan.Root, s.ShareScan.Files, s.ShareScan.Directories, s.ShareScan.ElapsedMS)
	}
	if s.Error != "" {
		fmt.Printf(" error=%q", s.Error)
	}
	fmt.Println()
	return nil
}
