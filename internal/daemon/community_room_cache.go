package daemon

// Roster/role identities and wall text are remote data, not durable authority.
const communityRoomCacheEntries = 100_000
const communityRoomWallsBytes = 16 << 20

func (s *Service) roomCacheFitsLocked(remove, add int) bool {
	// ponytail: bounded room-map scan on cache growth; keep a running total if profiling makes this hot.
	count := add - remove
	for _, r := range s.community.rooms {
		count += len(r.members) + len(r.privateMembers) + len(r.operators)
	}
	return count <= communityRoomCacheEntries
}
func (s *Service) clearRoomWallLocked(r *communityRoomState) {
	s.community.wallBytes -= r.wallBytes
	r.wallBytes = 0
	r.wall, r.wallFresh = nil, false
}
func (s *Service) clearRoomCachesLocked(r *communityRoomState) {
	s.clearRoomWallLocked(r)
	r.members, r.privateMembers, r.operators = nil, nil, nil
	r.privateMembersFresh, r.operatorsFresh = false, false
}
func (s *Service) pruneDirectoryRoomsLocked() {
	for name, r := range s.community.rooms {
		if !s.community.directory[name].private && !r.joined && !r.wanted && !r.creating && !r.rejectJoin && r.pending == "" && !r.autojoin && r.conversationID == 0 && r.ownWall == "" && r.roleMutation == nil {
			s.clearRoomCachesLocked(r)
			delete(s.community.rooms, name)
		}
	}
}
