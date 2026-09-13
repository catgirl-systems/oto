package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
)

func consoleCommand(args []string) error {
	fs := flag.NewFlagSet("console", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("console: unexpected arguments")
	}
	stat, err := os.Stdin.Stat()
	if err != nil {
		return err
	}
	return runConsole(os.Stdin, os.Stdout, ipc.NewClient(config.SocketPath()), stat.Mode()&os.ModeCharDevice != 0)
}

func runConsole(input io.Reader, output io.Writer, client *ipc.Client, prompt bool) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 128<<10)
	encoder := json.NewEncoder(output)
	for {
		if prompt {
			if _, err := fmt.Fprint(output, "oto> "); err != nil {
				return err
			}
		}
		if !scanner.Scan() {
			return scanner.Err()
		}
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		name, args, err := daemon.ParseCommand(scanner.Text())
		if err == nil && (name == "exit" || name == "quit") && len(args) == 0 {
			return nil
		}
		var result daemon.CommandResult
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			req := daemon.CommandRequest{Name: name, Args: args, RequestID: "console-" + rand.Text()}
			if name != "help" {
				var summary daemon.CommunitySummary
				summary, err = client.CommunitySummary(ctx)
				req.CommunityIdentity = summary.CommunityIdentity
			}
			if err == nil {
				result, err = client.RunCommand(ctx, req)
			}
			cancel()
		}
		// Confirmation is never inferred from another line or from a prior preview.
		// Gift confirmation uses oto command's explicit identity/revision flags.
		if err != nil {
			if writeErr := encoder.Encode(map[string]string{"error": err.Error()}); writeErr != nil {
				return writeErr
			}
		} else {
			if err := encoder.Encode(result); err != nil {
				return err
			}
		}
	}
}
