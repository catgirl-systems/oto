package tui

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/soulseek"
)

func TestCommunityPeerPartialResponsesAndFencing(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openUserInspector("Alice"))
	if m.community.peer.profile.Username != "Alice" || m.community.peer.profile.State != "offline" {
		t.Fatal("offline resource not applied", m.community.peer)
	}
	id := m.community.summary.CommunityIdentity
	m.community.summary.Connected = true
	p := daemon.CommunityProfile{CommunityIdentity: id, Username: "Alice", Description: "hello 世界\nsecond line", UploadSlots: 3, QueueLength: 7, State: "ready", Generation: 1, Revision: 100, UpdatedAt: time.Now()}
	interests := daemon.CommunityDiscoveryPage{CommunityIdentity: id, Kind: "user-interests", Target: "Alice", State: "ready", Generation: 1, Revision: 100, Rows: []daemon.CommunityDiscoveryRow{{Kind: "like", Item: "techno"}}}
	m.applyCommunityPeer(communityPeerMsg{request: m.community.peer.request, identity: id, username: "Alice", profile: p, interests: interests})
	text := strings.Join(m.community.inspectorLines(), "\n")
	for _, want := range []string{"hello 世界", "Slots: 3", "Queue: 7", "like: techno"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing partial field", want, text)
		}
	}
	m.applyCommunityPeer(communityPeerMsg{request: m.community.peer.request - 1, identity: id, username: "Alice", profile: daemon.CommunityProfile{Description: "old"}})
	if m.community.peer.profile.Description != p.Description {
		t.Fatal("old request replaced profile")
	}
	m.applyCommunityPeer(communityPeerMsg{request: m.community.peer.request, identity: id, username: "alice", profile: daemon.CommunityProfile{Description: "wrong case"}})
	if m.community.peer.profile.Description != p.Description {
		t.Fatal("case-folded target")
	}
	m.community.summary.Connected = false
	if !strings.Contains(strings.Join(m.community.inspectorLines(), "\n"), "Slots (stale)") {
		t.Fatal("offline profile advertised fresh metadata")
	}
	oldRequest := m.community.peer.request
	m.community.resetUser()
	m.applyCommunityPeer(communityPeerMsg{request: oldRequest, identity: id, username: "Alice", profile: p})
	if m.community.peer.profile.Description != "" {
		t.Fatal("reset accepted old profile")
	}
}
func TestCommunityPeerPictureExplicitSaveAndNoOverwrite(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.target, m.community.pane = "Alice", 2
	id := m.community.summary.CommunityIdentity
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	picture := append(data.Bytes(), make([]byte, soulseek.MaxProfilePictureBytes-data.Len())...)
	socket := filepath.Join(t.TempDir(), "picture.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/community/profile/picture" || r.URL.Query().Get("username") != "Alice" || r.URL.Query().Get("revision") != "9" {
			http.Error(w, "unexpected picture request", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(daemon.CommunityProfilePicture{CommunityIdentity: id, Username: "Alice", ContentType: "image/png", Revision: 9, Data: picture})
	})}
	done := make(chan struct{})
	go func() { _ = server.Serve(listener); close(done) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	m.client = ipc.NewClient(socket)
	m.community.peer.profile = daemon.CommunityProfile{CommunityIdentity: id, Username: "Alice", PictureType: "image/png", PictureRevision: 9}
	m.key(chatPress("P"))
	m.key(chatPress("q?"))
	if !strings.HasSuffix(m.community.peer.path, "q?") || m.help {
		t.Fatal("path input invoked global actions")
	}
	path := filepath.Join(t.TempDir(), "picture.png")
	m.community.peer.path = path
	press := func(code rune) { drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: code}))) }
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		screen := m.View().Content
		if lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height {
			t.Fatal("picture form overflow", size)
		}
	}
	m.width, m.height = 120, 40
	press(tea.KeyEnter)
	press(tea.KeyEnter)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Cancel created picture")
	}
	press(tea.KeyEnter)
	press(tea.KeyRight)
	press(tea.KeyEnter)
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(saved, picture) {
		t.Fatal("explicit save", err, m.community.peer.err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("picture permissions", err)
	}
	m.key(chatPress("P"))
	m.community.peer.path = path
	press(tea.KeyEnter)
	press(tea.KeyRight)
	press(tea.KeyEnter)
	if !strings.Contains(m.community.peer.err, "exist") {
		t.Fatal("existing picture overwritten", m.community.peer.err)
	}
	retained, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(retained, picture) {
		t.Fatal("existing picture changed", err)
	}
}

func TestCommunityPeerAndDiscoveryCacheEviction(t *testing.T) {
	m, _ := privateChatModel(t)
	id := m.community.summary.CommunityIdentity
	m.community.target = "Alice"
	m.community.peer.profile = daemon.CommunityProfile{Generation: 4, Revision: 20, State: "ready", Description: "evicted"}
	m.community.peer.interests = daemon.CommunityDiscoveryPage{Generation: 4, Revision: 20, State: "ready"}
	profile := daemon.CommunityProfile{CommunityIdentity: id, Username: "Alice", Revision: 21, State: "idle"}
	interests := daemon.CommunityDiscoveryPage{CommunityIdentity: id, Kind: "user-interests", Target: "Alice", Revision: 21, State: "idle"}
	m.applyCommunityPeer(communityPeerMsg{request: m.community.peer.request, identity: id, username: "Alice", profile: profile, interests: interests})
	if m.community.peer.profile.State != "idle" || m.community.peer.profile.Description != "" || m.community.peer.interests.State != "idle" {
		t.Fatal("evicted resources still appear ready")
	}
	d := &m.community.discover
	d.mode = 1
	d.generation, d.revision = 4, 20
	d.state = "ready"
	page := daemon.CommunityDiscoveryPage{CommunityIdentity: id, Kind: "personal", Revision: 21, State: "idle"}
	m.applyDiscoverPage(discoverPageMsg{request: d.request, identity: id, kind: "personal", discovery: page})
	if d.state != "idle" || d.generation != 0 {
		t.Fatal("evicted discovery still appears ready", d)
	}
}

func TestCommunityPeerKeysRespectInspectorEditor(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.target, m.community.pane = "Alice", 2
	m.community.peer.profile = daemon.CommunityProfile{Username: "Alice", PictureType: "image/png", PictureRevision: 1}
	m.key(chatPress("/"))
	for _, key := range []string{"n", "p", "P", "r"} {
		if cmd := m.key(chatPress(key)); cmd != nil {
			t.Fatal("inspector text launched peer action", key)
		}
	}
	if m.community.input != "npPr" || m.community.peer.form || m.community.peer.loading {
		t.Fatal("peer actions captured inspector editor", m.community.input, m.community.peer)
	}
}

func TestCommunityPeerRenderedResourceDimensions(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.target, m.community.pane, m.community.view = "Alice", 2, 3
	m.community.user = daemon.CommunityUser{Username: "Alice", Exists: true, Status: 2, StatusFresh: true, StatsFresh: true, AddressFresh: true, Country: "FR", IP: "127.0.0.1"}
	m.community.peer.profile = daemon.CommunityProfile{Username: "Alice", State: "stale", Description: "hello 世界", Error: "profile request failed; refresh to retry", UpdatedAt: time.Now(), PictureType: "image/png", PictureWidth: 1, PictureHeight: 1, UploadAllowedKnown: true, UploadSlots: 3, QueueLength: 7, SlotsAvailable: true}
	m.community.peer.interests = daemon.CommunityDiscoveryPage{State: "ready", Rows: []daemon.CommunityDiscoveryRow{{Kind: "like", Item: "techno"}, {Kind: "dislike", Item: "noise"}}}
	m.community.discover = communityDiscoverModel{mode: 3, state: "ready", rows: []daemon.CommunityDiscoveryRow{{User: &daemon.CommunityUser{Username: "Alice"}, Rating: 9}}}
	for _, size := range [][2]int{{120, 40}, {110, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		screen := m.View().Content
		if lipgloss.Width(screen) > m.width || lipgloss.Height(screen) > m.height {
			t.Fatalf("profile overflow %v: %dx%d\n%s", size, lipgloss.Width(screen), lipgloss.Height(screen), screen)
		}
	}
}

func TestCommunityPeerVisibleColumnsAndDiscoverSelection(t *testing.T) {
	m, _ := privateChatModel(t)
	m.width, m.height = 110, 40
	m.community.pane = 1
	m.community.target = "Alice"
	cmd := m.loadCommunityPeer(false)
	if cmd == nil {
		t.Fatal("visible inspector never loads at 110 columns")
	}
	drainChat(t, &m, cmd)
	m.width = 109
	if m.loadCommunityPeer(false) != nil {
		t.Fatal("hidden profile was fetched")
	}
	m.width, m.height = 40, 16
	m.community.view, m.community.pane = 3, 0
	m.community.discover.mode = 7
	if !strings.Contains(m.View().Content, "› My profile") {
		t.Fatal("selected Discover mode scrolled off narrow screen", m.View().Content)
	}
}

func TestCommunityPeerSupporterFreshness(t *testing.T) {
	m, _ := privateChatModel(t)
	m.community.target = "Alice"
	m.community.summary.Connected = true
	check := func(want string) {
		t.Helper()
		if !strings.Contains(strings.Join(m.community.inspectorLines(), "\n"), "Supporter: "+want) {
			t.Fatal("wrong supporter freshness", want)
		}
	}
	check("unknown")
	m.community.user.Privileged, m.community.user.PrivilegeFresh = true, true
	m.community.user.PrivilegeUpdatedAt = time.Now()
	check("yes")
	m.community.user.Privileged = false
	check("no")
	m.community.user.PrivilegeFresh = false
	check("no (stale)")
	m.community.user.PrivilegeFresh = true
	m.community.summary.Connected = false
	check("no (stale)")
}
