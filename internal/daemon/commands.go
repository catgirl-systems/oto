package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

type CommandRequest struct {
	CommunityIdentity
	Name      string   `json:"name"`
	Args      []string `json:"args"`
	RequestID string   `json:"request_id,omitempty"`
	Revision  uint64   `json:"revision,omitempty"`
	Confirm   bool     `json:"confirm"`
}
type CommandSpec struct {
	Name        string `json:"name"`
	Usage       string `json:"usage"`
	Description string `json:"description"`
	MinArgs     int    `json:"min_args"`
	MaxArgs     int    `json:"max_args"`
}
type CommandResult struct {
	Help       []CommandSpec               `json:"help,omitempty"`
	Privileges *AccountPrivileges          `json:"privileges,omitempty"`
	Gift       *AccountPrivilegeGiftResult `json:"gift,omitempty"`
}
type builtinCommand struct {
	spec CommandSpec
	run  func(context.Context, *Service, CommandRequest) (CommandResult, error)
}

var builtinCommands = []builtinCommand{
	{CommandSpec{"privileges", "privileges", "Refresh supporter privilege balance", 0, 0}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
		out, err := s.AccountPrivileges(ctx, AccountPrivilegesRequest{CommunityIdentity: req.CommunityIdentity, Refresh: true})
		return CommandResult{Privileges: &out}, err
	}},
	{CommandSpec{"gift", "gift USER WHOLE_DAYS", "Preview a gift; confirmation requires its request ID and balance revision", 2, 2}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
		days, err := strconv.ParseUint(req.Args[1], 10, 32)
		if err != nil || days == 0 {
			return CommandResult{}, errors.New("gift requires positive whole days")
		}
		if !req.Confirm {
			if _, err := s.AccountPrivileges(ctx, AccountPrivilegesRequest{CommunityIdentity: req.CommunityIdentity}); err != nil {
				return CommandResult{}, err
			}
		}
		out, err := s.GiftAccountPrivileges(ctx, AccountPrivilegeGiftRequest{CommunityIdentity: req.CommunityIdentity, Username: req.Args[0], Days: uint32(days), RequestID: req.RequestID, Revision: req.Revision, Confirm: req.Confirm})
		return CommandResult{Gift: &out}, err
	}},
}

func CommandSpecs() []CommandSpec {
	out := make([]CommandSpec, 0, len(builtinCommands))
	for _, command := range builtinCommands {
		out = append(out, command.spec)
	}
	return out
}
func (s *Service) RunCommand(ctx context.Context, req CommandRequest) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	if len(req.Name) > 64 || len(req.Args) > 128 {
		return CommandResult{}, errors.New("command exceeds argument limits")
	}
	bytes := len(req.Name)
	for _, arg := range req.Args {
		bytes += len(arg)
		if bytes > 128<<10 {
			return CommandResult{}, errors.New("command exceeds text limit")
		}
	}
	if req.Name == "help" && len(req.Args) == 0 {
		return CommandResult{Help: CommandSpecs()}, nil
	}
	for _, command := range builtinCommands {
		if command.spec.Name != req.Name {
			continue
		}
		if len(req.Args) < command.spec.MinArgs || len(req.Args) > command.spec.MaxArgs {
			return CommandResult{}, fmt.Errorf("usage: %s", command.spec.Usage)
		}
		return command.run(ctx, s, req)
	}
	return CommandResult{}, fmt.Errorf("unknown command %q", req.Name)
}
