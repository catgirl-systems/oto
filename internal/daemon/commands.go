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
	Username  string   `json:"username,omitempty"`
	Room      string   `json:"room,omitempty"`
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
	Aliases    *CommunityAliasesPage       `json:"aliases,omitempty"`
	Alias      *CommunityAlias             `json:"alias,omitempty"`
	Message    string                      `json:"message,omitempty"`
	Send       *CommunitySendResult        `json:"send,omitempty"`
	RoomAction *CommunityRoomActionResult  `json:"room_action,omitempty"`
	Search     *SearchPage                 `json:"search,omitempty"`
	TextTools  *CommunityTextSettings      `json:"text_tools,omitempty"`
	Away       *CommunityAwaySettings      `json:"away,omitempty"`
	Broadcast  *CommunityBroadcastPage     `json:"broadcast,omitempty"`
	SharedSend *SharedSendPage             `json:"shared_send,omitempty"`
}
type builtinCommand struct {
	spec CommandSpec
	run  func(context.Context, *Service, CommandRequest) (CommandResult, error)
}

func commandBuiltins() []builtinCommand {
	commands := append(socialCommands(), broadcastCommands()...)
	commands = append(commands, sharedSendCommands()...)
	return append(commands, []builtinCommand{
		{CommandSpec{"auto-away", "auto-away [EXPECTED_SECONDS NEW_SECONDS]", "View/change idle timeout (0 off); manual Away remains until explicit Online", 0, 2}, autoAwayCommand},
		{CommandSpec{"away-reply", "away-reply [EXPECTED_TEXT NEW_TEXT]", "View/change away reply (empty disables); at most once per sender per away period", 0, 2}, awayReplyCommand},
		{CommandSpec{"ctcp", "ctcp USER", "Explicit VERSION query; automatic VERSION replies are opt-in via text-tools ctcp_version", 1, 1}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			out, err := s.RequestCommunityVersion(ctx, CommunitySendRequest{CommunityIdentity: req.CommunityIdentity, Username: req.Args[0], RequestID: req.RequestID})
			return CommandResult{Send: &out}, err
		}},
		{CommandSpec{"text-tools", "text-tools [REVISION JSON]", "View/replace future-message rules; 32 per kind; substitutions ordered/literal; censorship whole whitespace tokens, case-insensitive * and ? wildcards, matched tokens become ***; quote JSON as one argument", 0, 2}, textToolsCommand},
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
		{CommandSpec{"aliases", "aliases [CURSOR]", "List account aliases; $1..$128 positional, $* rest text, $$ literal dollar; depth limit 8", 0, 1}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			cursor := ""
			if len(req.Args) > 0 {
				cursor = req.Args[0]
			}
			out, err := s.CommunityAliases(ctx, CommunityAliasesRequest{CommunityIdentity: req.CommunityIdentity, Cursor: cursor})
			return CommandResult{Aliases: &out}, err
		}},
		{CommandSpec{"alias", "alias NAME EXPANSION [REVISION]", "Create an alias or edit using its loaded revision; quote the expansion as one argument", 2, 3}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			revision := ""
			if len(req.Args) > 2 {
				revision = req.Args[2]
			}
			out, err := s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: req.CommunityIdentity, Name: req.Args[0], Expansion: req.Args[1], Revision: revision})
			return CommandResult{Alias: &out}, err
		}},
		{CommandSpec{"unalias", "unalias NAME REVISION", "Remove an alias only with explicit confirmation and its loaded revision", 2, 2}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			_, err := s.SetCommunityAlias(ctx, CommunityAliasRequest{CommunityIdentity: req.CommunityIdentity, Name: req.Args[0], Revision: req.Args[1], Remove: true, Confirm: req.Confirm})
			if err != nil {
				return CommandResult{}, err
			}
			return CommandResult{Message: "Alias removed"}, nil
		}},
	}...)
}

func CommandSpecs() []CommandSpec {
	commands := commandBuiltins()
	out := []CommandSpec{{Name: "help", Usage: "help", Description: "Show built-in commands"}}
	for _, command := range commands {
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
	_, _ = s.CommunityActivity(ctx, req.CommunityIdentity)
	var err error
	req, err = s.expandCommandAliases(ctx, req)
	if err != nil {
		return CommandResult{}, err
	}
	if req.Name == "help" && len(req.Args) == 0 {
		return CommandResult{Help: CommandSpecs()}, nil
	}
	for _, command := range commandBuiltins() {
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
