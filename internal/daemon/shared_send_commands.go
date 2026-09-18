package daemon

import (
	"context"
	"errors"
	"strconv"
)

func sharedSendCommands() []builtinCommand {
	commands := []builtinCommand{}
	for _, folder := range []bool{false, true} {
		name, usage, maxArgs := "send-files", "send-files USER SHARED_FILE [SHARED_FILE ...]", 128
		if folder {
			name, usage, maxArgs = "send-folder", "send-folder USER SHARED_FOLDER", 2
		}
		commands = append(commands, builtinCommand{CommandSpec{name, usage, "Preview currently indexed shared files, sizes and recipient permission failures; quote paths with spaces", 2, maxArgs}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			if req.Confirm {
				return CommandResult{}, errors.New("preview first, then use send-confirm with its request ID and token")
			}
			selection := SharedSendRequest{CommunityIdentity: req.CommunityIdentity, RequestID: req.RequestID, Username: req.Args[0]}
			if folder {
				selection.Folder = req.Args[1]
			} else {
				selection.Files = req.Args[1:]
			}
			out, err := s.PreviewSharedSend(ctx, selection)
			return CommandResult{SharedSend: &out, Message: "Preview only. Review all files and permission failures before send-confirm REQUEST_ID TOKEN. Only permitted files will be submitted."}, err
		}})
	}
	commands = append(commands, builtinCommand{CommandSpec{"send-show", "send-show REQUEST_ID [CURSOR]", "Inspect captured selection and linked upload history; no sending", 1, 2}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
		cursor := 0
		var err error
		if len(req.Args) == 2 {
			cursor, err = strconv.Atoi(req.Args[1])
			if err != nil {
				return CommandResult{}, errors.New("send cursor must be an integer")
			}
		}
		out, err := s.SharedSend(ctx, req.CommunityIdentity, req.Args[0], cursor)
		return CommandResult{SharedSend: &out}, err
	}})
	for _, action := range []string{"send", "stop"} {
		name := "send-confirm"
		if action == "stop" {
			name = "send-stop"
		}
		commands = append(commands, builtinCommand{CommandSpec{name, name + " REQUEST_ID TOKEN", "Requires explicit confirmation; stop only stops remaining submissions; already admitted files retain normal upload controls", 2, 2}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			if req.Confirm && req.RequestID != req.Args[0] {
				return CommandResult{}, errors.New("confirmation request ID must match the shared-send preview")
			}
			out, err := s.ActSharedSend(ctx, SharedSendAction{CommunityIdentity: req.CommunityIdentity, RequestID: req.Args[0], Token: req.Args[1], Action: action, Confirm: req.Confirm})
			return CommandResult{SharedSend: &out, Message: "Batch completion means submission finished, not file delivery. Inspect per-file upload status; do not automatically repeat uncertain submissions."}, err
		}})
	}
	return commands
}
