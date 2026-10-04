package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func commPreview() model {
	m := communityViewModel()
	m.status = snapshot{status: daemon.StatusConnected, presence: daemon.PresenceOnline, user: "molly"}
	c := &m.community
	c.summary.Capabilities = []string{"users", "watches", "private-chat", "public-rooms", "buddies", "discovery", "profiles", "private-rooms"}
	c.summary.Unread = 3
	ch := &c.chats
	ch.listReady, ch.historyReady = true, true
	ch.conversations = []daemon.CommunityConversation{{Kind: "private", Target: "alice", Unread: 2, Mentions: 1}, {Kind: "private", Target: "bob_the_long_named_sharer"}, {Kind: "private", Target: "carol", Closed: true}}
	ch.conversation = ch.conversations[0]
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.Local)
	ch.messages = []daemon.CommunityMessage{
		{ID: 4, Sender: "alice", Direction: "incoming", Text: "hey molly, do you still have that Boards of Canada rip?", CreatedAt: at.Add(3 * time.Minute), Mention: true},
		{ID: 3, Sender: "molly", Direction: "outgoing", Text: "sure, queued it for you", CreatedAt: at.Add(2 * time.Minute), State: "sent"},
		{ID: 2, Sender: "alice", Direction: "incoming", Text: "thanks!", CreatedAt: at.Add(time.Minute)},
		{ID: 1, Sender: "server", Direction: "system", Text: "alice is online", CreatedAt: at},
	}
	ch.unreadThrough = 3
	ch.position.follow, ch.position.selected = true, 4
	r := &c.rooms
	r.listReady = true
	r.rooms = []daemon.CommunityRoom{{Name: "electronic", Population: 412, PopulationKnown: true, Joined: true, Remembered: true}, {Name: "ambient", Population: 88, PopulationKnown: true}, {Name: "secret club", Private: true, Joined: true, Population: 5, PopulationKnown: true}}
	r.membersReady = true
	r.members = []daemon.CommunityRoomMember{{CommunityUser: daemon.CommunityUser{Username: "alice"}}, {CommunityUser: daemon.CommunityUser{Username: "bob"}}}
	b := &c.buddies
	b.listReady, b.total = true, 2
	b.buddies = []daemon.CommunityBuddy{{CommunityUser: daemon.CommunityUser{Username: "alice", Exists: true, Status: soulseek.UserStatusOnline, StatusFresh: true}, Trusted: true}, {CommunityUser: daemon.CommunityUser{Username: "bob", Exists: true, Status: soulseek.UserStatusAway, StatusFresh: true}, Note: "great jazz"}}
	b.active = &b.buddies[1]
	c.discover.interests = []daemon.CommunityInterest{{Item: "ambient", Opinion: "like", State: "saved"}, {Item: "dubstep", Opinion: "dislike", State: "saved"}}
	c.target = "alice"
	c.user = daemon.CommunityUser{Username: "alice", Exists: true, Status: soulseek.UserStatusOnline, StatusFresh: true, StatusUpdatedAt: at, Country: "FR", StatsFresh: true, StatsUpdatedAt: at}
	c.user.Stats.Files, c.user.Stats.Directories, c.user.Stats.AverageSpeed = 12000, 800, 2<<20
	return m
}

func TestCommunityViewsFitWithRealisticData(t *testing.T) {
	for _, color := range []string{"", "1"} {
		t.Setenv("NO_COLOR", color)
		for view := range 4 {
			for pane := range 3 {
				for _, size := range [][2]int{{150, 40}, {110, 24}, {90, 20}, {60, 16}, {40, 12}} {
					for _, composing := range []bool{false, true} {
						m := commPreview()
						m.width, m.height = size[0], size[1]
						m.community.view, m.community.pane = view, pane
						m.community.chats.composing = composing && view == 0
						lines := strings.Split(m.View().Content, "\n")
						failIfFmt(t, len(lines) > m.height, "view %d pane %d %v: %d lines", view, pane, size, len(lines))
						for _, line := range lines {
							failIfFmt(t, lipgloss.Width(line) > m.width, "view %d pane %d %v: %q", view, pane, size, line)
						}
					}
				}
			}
		}
	}
	t.Setenv("NO_COLOR", "1")
	m := commPreview()
	m.width, m.height, m.community.pane = 130, 24, 1
	view := m.View().Content
	for _, want := range []string{"[alice]", "unread", "hey molly", "#4", "Status", "● online", "2 @1"} {
		failIfFmt(t, !strings.Contains(view, want), "chat view missing %q:\n%s", want, view)
	}
}

func TestProfileEditOpensDuringBackgroundRefresh(t *testing.T) {
	m := commPreview()
	m.community.summary.Capabilities = append(m.community.summary.Capabilities, "self-profile")
	d := &m.community.discover
	m.community.view, m.community.pane, d.mode = 3, 1, 7
	d.profile = daemon.CommunitySelfProfile{CommunityIdentity: m.community.summary.CommunityIdentity, Description: "saved", Revision: 4}
	d.profileLoading, d.profileRequest = true, 9 // the once-a-second refresh is in flight
	m.key(key("e"))
	failIfFmt(t, d.form != "profile" || d.profileDraft != "saved" || d.profileBaseRevision != 4, "edit rejected during a refresh: form=%q err=%q", d.form, d.err)
	failIf(t, d.profileLoading || d.profileRequest == 9, "the in-flight refresh was not retired")

	// Before any profile has loaded for this session, editing still waits.
	fresh := commPreview()
	fresh.community.summary.Capabilities = append(fresh.community.summary.Capabilities, "self-profile")
	fresh.community.view, fresh.community.pane, fresh.community.discover.mode = 3, 1, 7
	fresh.community.discover.profileLoading = true
	fresh.key(key("e"))
	failIf(t, fresh.community.discover.form != "" || fresh.community.discover.err == "", "edited a profile that never loaded")
}
