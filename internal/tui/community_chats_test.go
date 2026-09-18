package tui

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage"
	"github.com/catgirl-systems/oto/internal/storage/db"
	"github.com/charmbracelet/x/ansi"
)

func privateChatModel(t *testing.T) (model, *storage.DB) {
	t.Helper()
	cfg := config.Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password, cfg.DownloadDir = "local", "local-only", t.TempDir()
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	service, err := daemon.New(cfg, path)
	must(t, err)
	service.SetConfigPath(filepath.Join(t.TempDir(), "config.toml"))
	socket := filepath.Join(t.TempDir(), "ipc.sock")
	server := ipc.NewServer(service, socket)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done; _ = service.Close() })
	client := ipc.NewClient(socket)
	for deadline := time.Now().Add(3 * time.Second); ; {
		if _, err := client.CommunitySummary(ctx); err == nil {
			break
		}
		failIf(t, time.Now().After(deadline), "IPC not ready")
		time.Sleep(time.Millisecond)
	}
	store, err := storage.Open(path)
	must(t, err)
	t.Cleanup(func() { _ = store.Close() })
	m := newModel(ctx, client, "", false, cfg)
	m.workspace, m.width, m.height = workspaceCommunity, 120, 40
	drainChat(t, &m, m.loadCommunitySummary())
	return m, store
}
func drainChat(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0; n++ {
		failIf(t, n > 100, "unbounded chat command loop")
		cmd, queue = queue[0], queue[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		updated, next := m.Update(msg)
		*m = updated.(model)
		if next != nil {
			queue = append(queue, next)
		}
	}
}
func chatPress(text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: []rune(text)[0], Text: text})
}
func seedChat(t *testing.T, m *model, store *storage.DB, username string, count int) daemon.CommunityConversation {
	t.Helper()
	identity := m.community.summary.CommunityIdentity
	conversation, err := m.client.OpenCommunityConversation(m.ctx, daemon.CommunityOpenConversationRequest{CommunityIdentity: identity, Username: username})
	must(t, err)
	err = store.WriteTx(m.ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		for i := range count {
			if _, err := q.InsertCommunityMessage(m.ctx, db.InsertCommunityMessageParams{Account: identity.Account, ConversationID: conversation.ID, Direction: "incoming", Sender: username, Body: fmt.Sprintf("hello %03d 世界 👋", i), State: "received", Mention: 1, CreatedAt: 1700000000000}); err != nil {
				return err
			}
		}
		_, err := q.BumpCommunityRevision(m.ctx, identity.Account)
		return err
	})
	must(t, err)
	return conversation
}

func TestCommunityPrivateComposerWorkflow(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openCommunityChat("猫Alice", true))
	failIf(t, m.community.chats.conversation.ID == 0, "chat not opened")
	m.key(chatPress("q/?猫👩‍💻"))
	updated, cmd := m.Update(tea.PasteMsg{Content: "\nsecond line"})
	m = updated.(model)
	drainActivity(t, &m, cmd)
	failIf(t, m.help || m.workspace != workspaceCommunity, "paste or text became action")
	original := m.community.chats.drafts[m.chatKey()]
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	failIf(t, m.community.chats.dialog == nil || m.community.chats.dialog.confirm, "multiline lacks default-cancel preview")
	failIf(t, !strings.Contains(m.View().Content, "second line"), "preview missing")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	failIf(t, m.community.chats.drafts[m.chatKey()] != original, "cancel discarded draft")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, m.community.chats.err != "" || m.chatDraftCount() != 0, "send", m.community.chats.err)
	failIf(t, len(m.community.chats.messages) != 1 || m.community.chats.messages[0].State != "queued" || m.community.chats.messages[0].Text != strings.ReplaceAll(original.text, "\n", " "), "offline queue", m.community.chats.messages)
	// Independent draft and exact-case identity, with username completion.
	m.key(chatPress("猫A"))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab})))
	failIf(t, m.community.chats.drafts[m.chatKey()].text != "猫Alice", "completion")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	key := m.chatKey()
	drainChat(t, &m, m.openCommunityChat("猫alice", true))
	m.key(chatPress("other draft"))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	drainChat(t, &m, m.openCommunityChat("猫Alice", false))
	failIf(t, m.chatKey() != key || m.community.chats.drafts[key].text != "猫Alice" || m.chatDraftCount() != 2, "draft isolation")
	m.key(chatPress("q"))
	failIf(t, m.community.chats.dialog == nil || m.community.chats.dialog.confirm, "draft quit did not default to Cancel")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	failIf(t, m.chatDraftCount() != 2, "cancelled quit lost drafts")
	m.key(chatPress("q"))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	if _, ok := m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))().(tea.QuitMsg); !ok {
		t.Fatal("confirmed quit")
	}
}

func TestCommunityPrivatePagingReadAndTwoFrontends(t *testing.T) {
	m, store := privateChatModel(t)
	conversation := seedChat(t, &m, store, "Alice", 205)
	drainChat(t, &m, m.loadCommunitySummary())
	failIf(t, m.community.summary.Unread != 205 || m.community.summary.Mentions != 205, "unread sidebar")
	other := newModel(m.ctx, m.client, "", false, config.Default())
	other.workspace, other.width, other.height = workspaceCommunity, 40, 16
	drainChat(t, &other, other.loadCommunitySummary())
	other.community.chats.blurred = true
	drainChat(t, &other, other.openCommunityChat("Alice", true))
	other.key(chatPress("only on second frontend"))
	failIf(t, other.community.summary.Unread != 205, "background terminal marked read")
	// Read is allowed only with visible latest content, never the list/overlay.
	m.width, m.height = 40, 16
	cmd := m.openCommunityChat("Alice", false)
	m.community.pane = 0
	drainChat(t, &m, cmd)
	failIf(t, m.community.summary.Unread != 205, "narrow sidebar marked read")
	m.community.pane = 1
	drainChat(t, &m, m.readCommunityChat())
	failIf(t, m.community.summary.Unread != 0 || len(m.community.chats.messages) != 200, "visible latest read or page bound", m.community.summary)
	drainChat(t, &other, other.loadCommunitySummary())
	failIf(t, other.community.chats.drafts[other.chatKey()].text != "only on second frontend" || other.community.summary.Unread != 0, "cross-frontend state")
	m.scrollCommunityChat("home")
	anchor := m.community.chats.position.anchor
	seedChat(t, &m, store, "Alice", 1)
	drainChat(t, &m, m.loadCommunitySummary())
	failIf(t, m.community.chats.position.anchor != anchor || m.community.chats.position.follow || m.community.chats.newerCount != 1 || m.community.summary.Unread != 1, "poll stole scroll/read state", m.community.chats.position, m.community.chats.newerCount)
	drainChat(t, &m, m.key(chatPress("p")))
	failIf(t, len(m.community.chats.messages) != 5 || m.community.chats.messages[4].ID != 1, "older history", m.community.chats.messages)
	drainChat(t, &m, m.key(chatPress("n")))
	failIf(t, len(m.community.chats.messages) != 200, "newer page")
	m.key(chatPress("f"))
	m.key(chatPress("hello 001"))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, len(m.community.chats.messages) != 1 || !strings.Contains(m.community.chats.messages[0].Text, "001") || m.community.summary.Unread != 1, "find changed read state")
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd})))
	failIf(t, m.community.summary.Unread != 0 || !m.community.chats.position.follow, "End did not reach latest")
	failIf(t, m.community.chats.conversation.ID != conversation.ID, "history changed conversation")
}

func TestCommunityPrivateSendReconciliationAndActions(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openCommunityChat("Alice", true))
	m.setChatDraft("once only", 9)
	cmd := m.sendCommunityChat()
	x := cmd().(chatOperationMsg) // Simulate accepted API write, lost HTTP response.
	x.err = errors.New("response lost")
	m.applyChatOperation(x)
	id := m.community.chats.drafts[m.chatKey()].requestID
	m.key(chatPress("do not change ambiguous submission"))
	if d := m.community.chats.drafts[m.chatKey()]; d.requestID != id || d.text != "once only" {
		t.Fatal("pending submission mutated")
	}
	drainChat(t, &m, m.sendCommunityChat())
	failIf(t, len(m.community.chats.messages) != 1 || m.chatDraftCount() != 0, "retry duplicated API submission")
	m.community.chats.composing = false
	m.key(chatPress("X"))
	failIf(t, m.community.chats.dialog == nil || m.community.chats.dialog.confirm, "cancel confirmation")
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, m.community.chats.messages[0].State != "cancelled" || m.community.chats.dialog != nil, "cancel did not persist")
	m.key(chatPress("R"))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, m.community.chats.messages[0].State != "queued", "retry did not queue")
	// Clear identifies a fixed boundary and cannot erase the unresolved outbox.
	m.key(chatPress("C"))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, len(m.community.chats.messages) != 1, "clear erased queued message")
	m.setChatDraft("keep across close", 17)
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: 'w', Mod: tea.ModCtrl})))
	failIf(t, m.community.chats.conversation.ID != 0 || m.chatDraftCount() != 1, "close lost draft")
	drainChat(t, &m, m.openCommunityChat("Alice", false))
	failIf(t, len(m.community.chats.messages) != 1 || m.community.chats.drafts[m.chatKey()].text != "keep across close", "reopen lost history/draft")
}

func TestCommunityPrivateInputAndResponsiveRendering(t *testing.T) {
	m := communityViewModel()
	m.community.summary.Capabilities = append(m.community.summary.Capabilities, "private-chat")
	m.community.chats.conversation = daemon.CommunityConversation{ID: 1, Target: "猫😀"}
	m.community.target, m.community.pane = "猫😀", 1
	m.community.chats.composing = true
	m.setChatDraft("q/?世界👩‍💻\nnext", 7)
	original := m.community.chats.drafts[m.chatKey()]
	for _, text := range []string{"\x1b[31m", "\x00", "\u202e", string([]byte{255}), strings.Repeat("x", soulseek.MaxChatBytes+1)} {
		m.pasteCommunityChat(text)
		failIf(t, m.community.chats.drafts[m.chatKey()] != original || m.community.chats.err == "", "invalid paste changed draft")
	}
	m.community.chats.err = ""
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {36, 8}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		view := m.View().Content
		failIfFmt(t, lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height, "%v: %dx%d\n%s", size, lipgloss.Width(view), lipgloss.Height(view), view)
		failIfFmt(t, !strings.Contains(ansi.Strip(view), "q/?"), "composer hidden at %v\n%s", size, view)
	}
	m.community.chats.composing = false
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 16}, {20, 6}} {
		m.width, m.height = size[0], size[1]
		m.confirmChatDraftQuit()
		view := m.View().Content
		failIfFmt(t, lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height, "dialog exceeds %v", size)
	}
}

func TestCommunityPrivateStaleSessionAndNavigation(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openCommunityChat("Alice", true))
	m.setChatDraft("original account", 16)
	key, identity := m.chatKey(), m.community.summary.CommunityIdentity
	cmd := m.loadCommunityChats(true)
	msg := cmd().(chatDataMsg)
	newSummary := m.community.summary
	newSummary.Session++
	m.applyCommunitySummary(communitySummaryMsg{request: m.community.summaryRequest, summary: newSummary})
	m.applyChatData(msg)
	failIf(t, m.community.chats.historyReady || m.chatDraftCount() != 1, "stale history or lost draft")
	newSummary.Account = "other account"
	m.applyCommunitySummary(communitySummaryMsg{request: m.community.summaryRequest, summary: newSummary})
	failIf(t, m.community.chats.conversation.ID != 0 || m.community.chats.composing || m.community.chats.drafts[key].text != "original account", "account privacy/draft handling")
	// An asynchronous Ctrl+N result must not pull the user out of another workspace.
	m.community.summary.CommunityIdentity = identity
	m.community.chats.busy = true
	x := chatOperationMsg{operation: m.community.chats.operation, navigation: m.community.chats.navigation, identity: identity, kind: "unread", conversation: daemon.CommunityConversation{ID: 1, Target: "Alice"}}
	m.switchWorkspace(workspaceSearch)
	if cmd := m.applyChatOperation(x); cmd != nil || m.workspace != workspaceSearch {
		t.Fatal("late unread result stole focus")
	}
}

func TestCommunityPrivateExportFiles(t *testing.T) {
	m, store := privateChatModel(t)
	conversation := seedChat(t, &m, store, "Alice", 205)
	identity := m.community.summary.CommunityIdentity
	for _, format := range []string{"json", "text"} {
		path := filepath.Join(t.TempDir(), "export."+format)
		req := daemon.CommunityExportRequest{CommunityIdentity: identity, ConversationID: conversation.ID, Format: format, Limit: 2}
		must(t, exportChatFile(m.ctx, m.client, req, path))
		info, _ := os.Stat(path)
		failIf(t, info.Mode().Perm() != 0600, "export is not private")
		data, _ := os.ReadFile(path)
		if format == "json" {
			var messages []daemon.CommunityMessage
			if err := json.Unmarshal(data, &messages); err != nil || len(messages) != 205 {
				t.Fatal("JSON export", err, len(messages))
			}
		} else if strings.Count(string(data), "hello") != 205 {
			t.Fatal("incomplete text export")
		}
		if err := exportChatFile(m.ctx, m.client, req, path); !errors.Is(err, os.ErrExist) {
			t.Fatal("existing file overwritten", err)
		}
		if after, _ := os.ReadFile(path); string(after) != string(data) {
			t.Fatal("existing export changed")
		}
		ctx, cancel := context.WithCancel(m.ctx)
		cancel()
		if err := exportChatFile(ctx, m.client, req, path+"-cancelled"); err == nil {
			t.Fatal("cancelled export succeeded")
		}
		files, _ := os.ReadDir(filepath.Dir(path))
		failIf(t, len(files) != 1, "partial or temporary export leaked", files)
	}
	summary, err := m.client.CommunitySummary(m.ctx)
	failIf(t, err != nil || summary.Unread != 205, "export marked read")
}

func TestCommunityPrivateDelayedActionsPreserveNavigation(t *testing.T) {
	m, _ := privateChatModel(t)
	drainChat(t, &m, m.openCommunityChat("Alice", true))
	m.setChatDraft("delayed response", 16)
	response := m.sendCommunityChat()()
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	m.community.chats.position.follow = false
	drainChat(t, &m, func() tea.Msg { return response })
	failIf(t, m.community.chats.position.follow || m.chatDraftCount() != 0, "send completion stole anchor or failed to reconcile")
	response = m.chatConversationAction("close", m.community.chats.conversation.ID, 0)()
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyF6}))
	failIf(t, m.community.pane != 2, "test did not focus inspector")
	drainChat(t, &m, func() tea.Msg { return response })
	failIf(t, m.community.pane != 2, "close completion stole inspector focus")
}

func TestCommunityPrivateShortTranscriptSelectsEveryMessage(t *testing.T) {
	m := communityViewModel()
	m.community.summary.Capabilities = append(m.community.summary.Capabilities, "private-chat")
	m.community.pane = 1
	m.community.chats.conversation = daemon.CommunityConversation{ID: 1, Target: "Alice", LatestID: 3}
	m.community.chats.messages = []daemon.CommunityMessage{
		{ID: 3, Direction: "incoming", Sender: "Alice", Text: "latest", State: "received"},
		{ID: 2, Direction: "outgoing", Sender: "local", Text: "middle", State: "failed"},
		{ID: 1, Direction: "incoming", Sender: "Alice", Text: "oldest", State: "received"},
	}
	m.community.chats.position = chatPosition{follow: true, selected: 3, seenLatest: 3}
	for _, id := range []int64{2, 1} {
		m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
		failIfFmt(t, m.community.chats.position.selected != id, "Up selected %d; want %d", m.community.chats.position.selected, id)
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if message, ok := m.selectedChatMessage(); !ok || message.ID != 2 {
		t.Fatal("middle message is unreachable")
	}
	if cmd := m.key(chatPress("y")); cmd == nil {
		t.Fatal("middle message copy unavailable")
	}
	m.key(chatPress("R"))
	failIf(t, m.community.chats.dialog == nil || m.community.chats.dialog.message != 2 || m.community.chats.dialog.confirm, "middle message retry unavailable")
}

func TestCommunityPrivateTinyConfirmationControlsStayVisible(t *testing.T) {
	m := communityViewModel()
	m.width, m.height = 20, 6
	m.community.chats.conversation = daemon.CommunityConversation{ID: 1, Target: "Alice"}
	m.setChatDraft("first\nsecond", 12)
	m.community.chats.dialog = &chatDialog{kind: "paste", label: "Send this multiline message to Alice?", identity: m.community.summary.CommunityIdentity}
	for _, kind := range []string{"paste", "clear", "retry", "quit"} {
		m.community.chats.dialog.kind = kind
		view := m.View().Content
		failIfFmt(t, !strings.Contains(view, "[Cancel]") || !strings.Contains(view, "Confirm"), "%s hides confirmation controls: %q", kind, view)
		failIf(t, lipgloss.Height(view) > m.height || lipgloss.Width(view) > m.width, "tiny dialog exceeds viewport")
	}
}

func TestCommunityPrivateNextUnreadBeyondFirstPage(t *testing.T) {
	m, store := privateChatModel(t)
	account := m.community.summary.Account
	var target db.CommunityConversation
	err := store.WriteTx(m.ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		for i := range 205 {
			var err error
			target, err = q.EnsureCommunityConversation(m.ctx, db.EnsureCommunityConversationParams{Account: account, Kind: "private", Target: fmt.Sprintf("user-%03d", i)})
			if err != nil {
				return err
			}
		}
		if _, err := q.SetCommunityConversationClosed(m.ctx, db.SetCommunityConversationClosedParams{Account: account, ID: target.ID, Closed: 1}); err != nil {
			return err
		}
		if _, err := q.InsertCommunityMessage(m.ctx, db.InsertCommunityMessageParams{Account: account, ConversationID: target.ID, Direction: "incoming", Sender: target.Target, Body: "last-page unread", State: "received"}); err != nil {
			return err
		}
		_, err := q.BumpCommunityRevision(m.ctx, account)
		return err
	})
	must(t, err)
	drainChat(t, &m, m.loadCommunitySummary())
	failIf(t, len(m.community.chats.conversations) != 200, "sidebar is not paged")
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: 'n', Mod: tea.ModCtrl})))
	failIf(t, m.community.chats.conversation.ID != target.ID || m.community.chats.conversation.Closed || m.community.summary.Unread != 0, "Ctrl+N missed closed unread chat after page 1")
	m.community.chats.composing = true
	m.pasteCommunityChat("one\rtwo\r\nthree")
	failIf(t, m.community.chats.drafts[m.chatKey()].text != "one\ntwo\nthree", "terminal newline normalization")
}

func TestCommunityPrivateUnknownRequiresExplicitRetry(t *testing.T) {
	m, store := privateChatModel(t)
	drainChat(t, &m, m.openCommunityChat("Alice", true))
	m.setChatDraft("uncertain", 9)
	drainChat(t, &m, m.sendCommunityChat())
	message := m.community.chats.messages[0]
	_, err := store.Queries().SetCommunityMessageState(m.ctx, db.SetCommunityMessageStateParams{Account: m.community.summary.Account, ID: message.ID, OldState: "queued", NewState: "unknown", Error: "write interrupted"})
	must(t, err)
	m.community.chats.composing = false
	drainChat(t, &m, m.loadCommunityChats(true))
	failIf(t, !strings.Contains(m.View().Content, "[unknown]"), "uncertainty not rendered")
	m.key(chatPress("R"))
	if d := m.community.chats.dialog; d == nil || !strings.Contains(d.label, "already have reached") || d.confirm {
		t.Fatal("retry lacks explicit uncertainty warning")
	}
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	drainChat(t, &m, m.loadCommunityChats(true))
	failIf(t, m.community.chats.messages[0].State != "unknown", "default Cancel retried Unknown")
	m.key(chatPress("R"))
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	drainChat(t, &m, m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	failIf(t, m.community.chats.messages[0].State != "queued", "explicit retry did not queue Unknown")
}

func TestCommunityPrivateExportFormDoesNotCloseLaterForm(t *testing.T) {
	m, store := privateChatModel(t)
	seedChat(t, &m, store, "Alice", 1)
	drainChat(t, &m, m.openCommunityChat("Alice", false))
	path := filepath.Join(t.TempDir(), "chat.json")
	m.key(chatPress("E"))
	m.key(chatPress(path))
	cmd := m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	failIf(t, cmd == nil, "export form did not submit")
	response := cmd()
	m.key(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m.key(chatPress("f"))
	m.key(chatPress("keep typing"))
	drainChat(t, &m, func() tea.Msg { return response })
	failIf(t, m.community.chats.form != "find" || m.community.chats.input != "keep typing", "late export completion closed a later form")
	data, err := os.ReadFile(path)
	var messages []daemon.CommunityMessage
	failIf(t, err != nil || json.Unmarshal(data, &messages) != nil || len(messages) != 1, "export workflow did not write complete JSON", err)
}
