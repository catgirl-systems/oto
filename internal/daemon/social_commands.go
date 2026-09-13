package daemon

import (
	"context"
	"errors"
	"strings"
)

func socialCommands() []builtinCommand {
	return []builtinCommand{
		{CommandSpec{"message", "message USER TEXT", "Send a private message; quote usernames containing spaces", 2, 128}, commandPrivateMessage},
		{CommandSpec{"msg", "msg USER TEXT", "Send a private message", 2, 128}, commandPrivateMessage},
		{CommandSpec{"say", "say ROOM TEXT", "Send to a confirmed joined room", 2, 128}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			out, err := s.SendCommunityRoom(ctx, CommunityRoomSendRequest{CommunityIdentity: req.CommunityIdentity, Room: req.Args[0], Text: strings.Join(req.Args[1:], " "), RequestID: req.RequestID})
			return CommandResult{Send: &out}, err
		}},
		{CommandSpec{"me", "me TEXT", "Send an action to the current private chat or joined room", 1, 128}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			if (req.Username == "") == (req.Room == "") {
				return CommandResult{}, errors.New("me requires exactly one captured chat or room target")
			}
			text := "/me " + strings.Join(req.Args, " ")
			var out CommunitySendResult
			var err error
			if req.Room != "" {
				out, err = s.SendCommunityRoom(ctx, CommunityRoomSendRequest{CommunityIdentity: req.CommunityIdentity, Room: req.Room, Text: text, RequestID: req.RequestID})
			} else {
				out, err = s.SendCommunityPrivate(ctx, CommunitySendRequest{CommunityIdentity: req.CommunityIdentity, Username: req.Username, Text: text, RequestID: req.RequestID})
			}
			return CommandResult{Send: &out}, err
		}},
		{CommandSpec{"join", "join ROOM", "Join a public room; does not remember it automatically", 1, 1}, commandRoomMembership},
		{CommandSpec{"leave", "leave ROOM", "Leave a room without forgetting its saved preference", 1, 1}, commandRoomMembership},
		{CommandSpec{"search", "search global|buddies QUERY; search users|rooms TARGET QUERY", "Search the explicit scope; never falls back to global", 2, 128}, func(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
			search := ScopedSearchRequest{CommunityIdentity: req.CommunityIdentity, Scope: req.Args[0]}
			query := req.Args[1:]
			if search.Scope == "users" || search.Scope == "rooms" {
				if len(req.Args) < 3 {
					return CommandResult{}, errors.New("scoped search requires a target and query")
				}
				if search.Scope == "users" {
					search.Usernames = []string{req.Args[1]}
				} else {
					search.Rooms = []string{req.Args[1]}
				}
				query = req.Args[2:]
			}
			search.Query = strings.Join(query, " ")
			out, err := s.SearchScoped(ctx, search)
			return CommandResult{Search: &out}, err
		}},
	}
}
func commandPrivateMessage(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
	out, err := s.SendCommunityPrivate(ctx, CommunitySendRequest{CommunityIdentity: req.CommunityIdentity, Username: req.Args[0], Text: strings.Join(req.Args[1:], " "), RequestID: req.RequestID})
	return CommandResult{Send: &out}, err
}
func commandRoomMembership(ctx context.Context, s *Service, req CommandRequest) (CommandResult, error) {
	out, err := s.CommunityRoomAction(ctx, CommunityRoomActionRequest{CommunityIdentity: req.CommunityIdentity, Room: req.Args[0], Action: req.Name, RequestID: req.RequestID})
	return CommandResult{RoomAction: &out}, err
}
