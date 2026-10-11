package daemon

// Roster/role identities and wall text are remote data, not durable authority.
const communityRoomCacheEntries = 100_000
const communityRoomWallsBytes = 16 << 20

func (c *communityState) roomCacheFits(remove, add int) bool {
	// ponytail: bounded room-map scan on cache growth; keep a running total if profiling makes this hot.
	count := add - remove
	for _, r := range c.rooms {
		count += len(r.members) + len(r.privateMembers) + len(r.operators)
	}
	return count <= communityRoomCacheEntries
}
func (c *communityState) clearRoomWall(r *communityRoomState) {
	c.wallBytes -= r.wallBytes
	r.wallBytes = 0
	r.wall, r.wallFresh = nil, false
}
func (c *communityState) clearRoomCaches(r *communityRoomState) {
	c.clearRoomWall(r)
	r.members, r.privateMembers, r.operators = nil, nil, nil
	r.privateMembersFresh, r.operatorsFresh = false, false
}
func (c *communityState) pruneDirectoryRooms() {
	for name, r := range c.rooms {
		if !c.directory[name].private && !r.joined && !r.wanted && !r.creating && !r.rejectJoin && r.pending == "" && !r.autojoin && r.conversationID == 0 && r.ownWall == "" && r.roleMutation == nil {
			c.clearRoomCaches(r)
			delete(c.rooms, name)
		}
	}
}
