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

	"github.com/catgirl-systems/slsk-tui/internal/config"
	"github.com/catgirl-systems/slsk-tui/internal/daemon"
	"github.com/catgirl-systems/slsk-tui/internal/ipc"
	"github.com/catgirl-systems/slsk-tui/internal/tui"
)

const sourceURL = "https://github.com/catgirl-systems/slsk-tui"

var executable = os.Executable

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "slsk-tui:", err)
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
		case "help", "--help", "-h":
			usage()
			return nil
		case "version", "--version":
			fmt.Println("slsk-tui dev", sourceURL, "AGPL-3.0-only; no warranty")
			return nil
		}
	}
	return tuiCommand(args)
}

func usage() {
	fmt.Printf("slsk-tui — Soulseek search, browse, shares, and transfers\n\nUsage:\n  slsk-tui [--config PATH]\n  slsk-tui daemon [--config PATH]\n  slsk-tui status\n\nSource: %s\nLicense: AGPL-3.0-only; no warranty.\n", sourceURL)
}

func configFlag(fs *flag.FlagSet) *string {
	return fs.String("config", config.ConfigPath(), "config file")
}

func tuiCommand(args []string) error {
	fs := flag.NewFlagSet("slsk-tui", flag.ContinueOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
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

func daemonCommand(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	path := configFlag(fs)
	child := fs.Bool("child", false, "exit when stdin closes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	service, err := daemon.NewWithJournal(cfg, config.StatePath())
	if err != nil {
		return err
	}
	service.SetConfigPath(*path)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *child {
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

func statusCommand(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
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
	fmt.Printf("%s user=%s shares=%d transfers=%d", s.Status, s.Config.Soulseek.Username, len(s.Shares), len(s.Transfers))
	if s.Error != "" {
		fmt.Printf(" error=%q", s.Error)
	}
	fmt.Println()
	return nil
}
