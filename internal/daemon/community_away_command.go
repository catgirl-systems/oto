package daemon

import (
	"context"
	"errors"
	"strconv"
)

func autoAwayCommand(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
	current, err := s.CommunityAwaySettings(ctx, req.CommunityIdentity)
	if err != nil {
		return CommandResult{}, err
	}
	if len(req.Args) == 0 {
		return CommandResult{Away: &current}, nil
	}
	if len(req.Args) != 2 {
		return CommandResult{}, errors.New("use auto-away EXPECTED_SECONDS NEW_SECONDS; 0 disables automatic away")
	}
	old, e1 := strconv.Atoi(req.Args[0])
	next, e2 := strconv.Atoi(req.Args[1])
	if e1 != nil || e2 != nil {
		return CommandResult{}, errors.New("auto-away requires whole seconds")
	}
	if old != current.Settings.AutoAwaySeconds {
		return CommandResult{}, ErrCommunityMessageState
	}
	settings := current.Settings
	settings.AutoAwaySeconds = next
	out, err := s.SetCommunityAwaySettings(ctx, CommunityAwaySettingsRequest{CommunityIdentity: req.CommunityIdentity, Expected: current.Settings, Settings: settings})
	return CommandResult{Away: &out}, err
}

func awayReplyCommand(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
	current, err := s.CommunityAwaySettings(ctx, req.CommunityIdentity)
	if err != nil {
		return CommandResult{}, err
	}
	if len(req.Args) == 0 {
		return CommandResult{Away: &current}, nil
	}
	if len(req.Args) != 2 {
		return CommandResult{}, errors.New("use away-reply EXPECTED_TEXT NEW_TEXT; quote empty text to disable")
	}
	if req.Args[0] != current.Settings.AutoReply {
		return CommandResult{}, ErrCommunityMessageState
	}
	settings := current.Settings
	settings.AutoReply = req.Args[1]
	out, err := s.SetCommunityAwaySettings(ctx, CommunityAwaySettingsRequest{CommunityIdentity: req.CommunityIdentity, Expected: current.Settings, Settings: settings})
	return CommandResult{Away: &out}, err
}
