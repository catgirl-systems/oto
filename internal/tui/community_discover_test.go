package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
)

func TestCommunityDiscoverEditingDraftsAndNavigation(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.view, m.community.pane = 3, 1
	drainChat(t, &m, m.loadCommunityDiscover(false))
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	m.key(chatPress("a"))
	m.key(chatPress("TeChNo"))
	press(tea.KeyEsc)
	if m.chatDraftCount() != 1 {
		t.Fatal("interest draft not retained")
	}
	drainChat(t, &m, m.setDiscoverMode(7))
	m.key(chatPress("e"))
	updated, cmd := m.Update(tea.PasteMsg{Content: "q/?猫\rline"})
	m = updated.(model)
	drainActivity(t, &m, cmd)
	if m.community.discover.profileDraft != "q/?猫\nline" || m.chatDraftCount() != 2 {
		t.Fatal("profile overwrote interest draft")
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		screen := m.View().Content
		if lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height {
			t.Fatal("editor overflow", size)
		}
	}
	m.width, m.height = 120, 40
	press(tea.KeyEnter)
	if m.community.discover.form != "" || m.community.discover.err != "" || m.chatDraftCount() != 1 {
		t.Fatal("description save", m.community.discover.err)
	}
	drainChat(t, &m, m.setDiscoverMode(0))
	m.key(chatPress("a"))
	if m.community.discover.input != "TeChNo" {
		t.Fatal("draft not restored")
	}
	press(tea.KeyTab)
	press(tea.KeyEnter)
	d := m.community.discover
	if d.form != "" || d.err != "" || len(d.interests) != 1 || d.interests[0].Item != "techno" || d.interests[0].Opinion != "dislike" || m.chatDraftCount() != 0 {
		t.Fatal("interest save", d.err, d.interests)
	}
	m.key(chatPress("D"))
	press(tea.KeyEnter)
	if len(m.community.discover.interests) != 1 {
		t.Fatal("remove did not default to cancel")
	}
	m.key(chatPress("D"))
	press(tea.KeyRight)
	press(tea.KeyEnter)
	if len(m.community.discover.interests) != 0 {
		t.Fatal("remove failed", m.community.discover.err)
	}
	m.community.pane = 0
	drainChat(t, &m, m.setDiscoverMode(7))
	press(tea.KeyUp)
	if m.community.discover.mode != 6 {
		t.Fatal("profile trapped mode navigation")
	}
	before := m.community.pane
	press(tea.KeyF6)
	if m.community.pane == before {
		t.Fatal("Discover trapped pane navigation")
	}
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModCtrl})))
	if m.community.view != 2 {
		t.Fatal("Discover trapped subview navigation")
	}
}
func TestCommunityDiscoverStaleDescriptionAndFilteredUserIdentity(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.view, m.community.pane = 3, 1
	drainChat(t, &m, m.setDiscoverMode(7))
	m.key(chatPress("e"))
	m.pasteCommunityDiscover("draft")
	id := m.community.summary.CommunityIdentity
	remote := m.community.discover.profile
	remote.Description = "other frontend"
	changed, err := m.client.SetCommunitySelfProfile(m.ctx, remote)
	if err != nil {
		t.Fatal(err)
	}
	d := &m.community.discover
	m.applyDiscoverProfile(discoverProfileMsg{request: d.profileRequest, identity: id, profile: changed})
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	if !strings.Contains(m.community.discover.err, "changed") || m.community.discover.profileDraft != "draft" || m.chatDraftCount() != 1 {
		t.Fatal("draft implicitly rebased", m.community.discover.err)
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	if m.chatDraftCount() != 1 {
		t.Fatal("reload did not default to Cancel")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	if m.community.discover.profile.Description != "other frontend" || m.chatDraftCount() != 0 {
		t.Fatal("confirmed reload", m.community.discover.err)
	}
	m.community.discover = communityDiscoverModel{mode: 3, query: "bob", rows: []daemon.CommunityDiscoveryRow{{Kind: "user", User: &daemon.CommunityUser{Username: "Alice"}}, {Kind: "user", User: &daemon.CommunityUser{Username: "Bob"}}}}
	if m.discoverTarget() != "Bob" {
		t.Fatal("filtered row targeted another user")
	}
	m.community.discover = communityDiscoverModel{mode: 2, rows: []daemon.CommunityDiscoveryRow{{Kind: "recommendation", Item: "techno"}}}
	if m.discoverTarget() != "" {
		t.Fatal("interest used as a username")
	}
}
