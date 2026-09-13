package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strconv"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
)

func socialCommand(args []string) error {
	fs := flag.NewFlagSet("command", flag.ContinueOnError)
	requestID := fs.String("request-id", "", "request ID from preview; generated for new previews")
	revision := fs.Uint64("revision", 0, "balance revision from preview")
	account := fs.String("account", "", "account identity from preview")
	process := fs.String("daemon", "", "daemon identity from preview")
	session := fs.String("session", "", "session identity from preview")
	confirm := fs.Bool("confirm", false, "confirm the exact preview; never retries automatically")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := "help"
	rest := fs.Args()
	if len(rest) > 0 {
		name, rest = rest[0], rest[1:]
	}
	req := daemon.CommandRequest{Name: name, Args: rest, RequestID: *requestID, Revision: *revision, Confirm: *confirm}
	if *confirm {
		if *requestID == "" || name == "gift" && *revision == 0 || *account == "" || *process == "" || *session == "" {
			return errors.New("confirmation requires --request-id, --account, --daemon and --session from the preview; gifts also require --revision; flags precede the command")
		}
		generation, err := strconv.ParseUint(*session, 10, 64)
		if err != nil {
			return err
		}
		req.CommunityIdentity = daemon.CommunityIdentity{Account: *account, Daemon: *process, Session: generation}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := ipc.NewClient(config.SocketPath())
	if !*confirm && name != "help" {
		summary, err := client.CommunitySummary(ctx)
		if err != nil {
			return err
		}
		req.CommunityIdentity = summary.CommunityIdentity
	}
	if req.RequestID == "" {
		req.RequestID = "command-" + rand.Text()
	}
	out, err := client.RunCommand(ctx, req)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
