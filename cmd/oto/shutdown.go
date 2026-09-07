package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
)

// A shutdown request is not cancellation of the live session: uploads need its
// connections, callbacks and port mapping until the drain finishes.
func runDaemon(service *daemon.Service, server *ipc.Server, signals <-chan os.Signal, eof <-chan struct{}) error {
	ctx, cancel := context.WithCancel(context.Background())
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(ctx); close(serverErr) }()
	defer func() {
		cancel()
		<-serverErr
		_ = server.Close()
	}()
	started := make(chan error, 1)
	go func() { started <- service.Start(ctx) }()
	select {
	case err := <-started:
		if err != nil {
			return err
		}
	case err := <-serverErr:
		cancel()
		<-started
		return err
	case <-signals:
		cancel()
		<-started
		return nil
	case <-eof:
		cancel()
		<-started
		return nil
	}

	force, forceCancel := context.WithCancel(context.Background())
	defer forceCancel()
	var drained chan struct{}
	begin := func() {
		if drained == nil {
			drained = make(chan struct{})
			go func() { service.WaitForUploads(force); close(drained) }()
		}
	}
	for {
		select {
		case <-signals:
			if drained == nil {
				begin()
			} else {
				forceCancel()
			}
		case <-eof:
			eof = nil // Parent loss requests shutdown once; it never forces it.
			begin()
		case <-drained:
			return nil
		case err := <-serverErr:
			forceCancel()
			if drained != nil {
				<-drained
			}
			return err
		}
	}
}

// The daemon's live status, not a frontend's cached settings, authorizes waiting
// beyond the ordinary child-exit watchdog. Startup failures still use abort().
func waitForChild(child *exec.Cmd, keepAlive io.Closer, client *ipc.Client, output io.Writer) {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	_ = keepAlive.Close()
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	lastResponsive := time.Now()
	lastCount := -1
	forceRequested, forceSent := false, false
	for {
		select {
		case <-done:
			return
		case <-signals:
			forceRequested = true
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			status, err := client.Status(ctx)
			cancel()
			if err == nil && status.Shutdown != nil {
				if status.Shutdown.Draining {
					lastResponsive = time.Now()
					if n := status.Shutdown.ActiveUploads; n > 0 && n != lastCount {
						fmt.Fprintf(output, "Waiting for %d active upload(s); Ctrl+C to force shutdown.\n", n)
						lastCount = n
					}
				}
				if forceRequested && !forceSent {
					_ = child.Process.Signal(syscall.SIGTERM)
					forceSent = true
					fmt.Fprintln(output, "Forcing shutdown; remaining transfers will be interrupted.")
				}
			}
			if time.Since(lastResponsive) >= 3*time.Second {
				_ = child.Process.Kill()
				<-done
				return
			}
		}
	}
}
