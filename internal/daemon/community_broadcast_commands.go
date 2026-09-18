package daemon

import (
	"context"
	"errors"
	"strconv"
)

func broadcastCommands() []builtinCommand {
	commands := []builtinCommand{
		{CommandSpec{"broadcast", "broadcast buddies|uploaders TEXT [OFFLINE_BUDDY ...]", "Preview exact text and recipients; quote TEXT as one argument; offline buddies require explicit names", 2, 128}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			if req.Confirm {
				return CommandResult{}, errors.New("preview first, then confirm broadcast-send using its request ID and token")
			}
			out, err := s.PreviewCommunityBroadcast(ctx, CommunityBroadcastRequest{CommunityIdentity: req.CommunityIdentity, RequestID: req.RequestID, Audience: req.Args[0], Text: req.Args[1], Offline: req.Args[2:]})
			return CommandResult{Broadcast: &out, Message: "Preview only. Review every recipient page before confirming broadcast-send REQUEST_ID TOKEN. Sent means written to the server, not delivered to the recipient."}, err
		}},
		{CommandSpec{"broadcast-show", "broadcast-show REQUEST_ID [CURSOR]", "Inspect a persisted broadcast and per-recipient outcomes; no sending", 1, 2}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			cursor := 0
			var err error
			if len(req.Args) == 2 {
				cursor, err = strconv.Atoi(req.Args[1])
				if err != nil {
					return CommandResult{}, errors.New("broadcast cursor must be an integer")
				}
			}
			out, err := s.CommunityBroadcast(ctx, req.CommunityIdentity, req.Args[0], cursor)
			return CommandResult{Broadcast: &out}, err
		}},
	}
	for _, action := range []string{"send", "stop"} {
		commands = append(commands, builtinCommand{CommandSpec{"broadcast-" + action, "broadcast-" + action + " REQUEST_ID TOKEN", "Requires explicit confirmation; stopping only stops remaining submissions, not already queued PMs; no automatic rebroadcast", 2, 2}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			if req.Confirm && req.RequestID != req.Args[0] {
				return CommandResult{}, errors.New("confirmation request ID must match the broadcast preview")
			}
			out, err := s.ActCommunityBroadcast(ctx, CommunityBroadcastAction{CommunityIdentity: req.CommunityIdentity, RequestID: req.Args[0], Token: req.Args[1], Action: action, Confirm: req.Confirm})
			return CommandResult{Broadcast: &out, Message: "Already queued PMs retain their individual history and cancellation controls. Unknown outcomes require individual review; do not repeat the whole broadcast."}, err
		}})
	}
	return commands
}
