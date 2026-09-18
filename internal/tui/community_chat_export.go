package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/catgirl-systems/oto/internal/daemon"
	"github.com/catgirl-systems/oto/internal/ipc"
)

func (m *model) exportCommunityChat(format, path string) tea.Cmd {
	c := &m.community.chats
	if c.busy || c.conversation.ID == 0 || m.client == nil {
		return nil
	}
	if strings.TrimSpace(path) == "" {
		c.inputErr = "Choose a new file in an existing directory"
		return nil
	}
	ctx, cancel, op := m.beginChatOperation()
	identity, client := m.community.summary.CommunityIdentity, m.client
	req := daemon.CommunityExportRequest{CommunityIdentity: identity, ConversationID: c.conversation.ID, ThroughID: c.conversation.LatestID, Format: format, Query: c.position.query}
	return func() tea.Msg {
		defer cancel()
		err := exportChatFile(ctx, client, req, path)
		return chatOperationMsg{operation: op, identity: identity, kind: "export", err: err}
	}
}

// Stream bounded pages into a private temporary file. Link publishes atomically
// without overwriting an existing path (including a symlink); errors leave no
// partial export. The returned upper bound prevents arrivals extending the job.
func exportChatFile(ctx context.Context, client *ipc.Client, req daemon.CommunityExportRequest, path string) error {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, path[2:])
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".oto-chat-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	first := true
	if req.Format == "json" {
		if _, err := io.WriteString(f, "[\n"); err != nil {
			return err
		}
	}
	for {
		page, err := client.ExportCommunityHistory(ctx, req)
		if err != nil {
			return err
		}
		if page.CommunityIdentity != req.CommunityIdentity || page.ConversationID != req.ConversationID || page.Format != req.Format {
			return daemon.ErrCommunitySession
		}
		if req.ThroughID != 0 && page.ThroughID != req.ThroughID {
			return errors.New("community: export upper bound changed")
		}
		if req.Format == "text" {
			if _, err := io.WriteString(f, page.Text); err != nil {
				return err
			}
		} else {
			for _, message := range page.Messages {
				if !first {
					if _, err := io.WriteString(f, ",\n"); err != nil {
						return err
					}
				}
				if err := json.NewEncoder(f).Encode(message); err != nil {
					return err
				}
				first = false
			}
		}
		if page.NextCursor == 0 {
			break
		}
		if page.NextCursor <= req.Cursor || page.NextCursor > page.ThroughID {
			return errors.New("community: invalid export cursor")
		}
		req.Cursor, req.ThroughID = page.NextCursor, page.ThroughID
	}
	if req.Format == "json" {
		if _, err := io.WriteString(f, "]\n"); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
