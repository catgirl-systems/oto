package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

func textToolsCommand(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
	if len(req.Args) == 0 {
		out, err := s.CommunityTextSettings(ctx, req.CommunityIdentity)
		return CommandResult{TextTools: &out}, err
	}
	if len(req.Args) != 2 {
		return CommandResult{}, errors.New("use text-tools or text-tools REVISION JSON; changes affect future messages only")
	}
	var settings CommunityTextTools
	decoder := json.NewDecoder(strings.NewReader(req.Args[1]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return CommandResult{}, errors.New("invalid text-tools JSON; use keywords, substitutions, censorship and ctcp_version")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return CommandResult{}, errors.New("text-tools requires exactly one JSON object")
	}
	if strings.TrimSpace(req.Args[1]) == "null" {
		return CommandResult{}, errors.New("text-tools requires a JSON object")
	}
	out, err := s.SetCommunityTextSettings(ctx, CommunityTextSettings{CommunityIdentity: req.CommunityIdentity, Revision: req.Args[0], Settings: settings})
	return CommandResult{TextTools: &out}, err
}
