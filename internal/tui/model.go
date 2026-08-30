package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/catgirl-systems/slsk-tui/internal/config"
	"github.com/catgirl-systems/slsk-tui/internal/daemon"
	"github.com/catgirl-systems/slsk-tui/internal/ipc"
	"github.com/catgirl-systems/slsk-tui/internal/soulseek"
	"github.com/charmbracelet/x/ansi"
)

type snapshot struct {
	status daemon.Status
	user   string
	err    string
}
type result struct {
	user, path   string
	size         uint64
	directory    bool
	free         bool
	speed, queue uint32
}
type entry struct {
	name      string
	size      uint64
	directory bool
}
type transfer struct {
	id, user, filename, direction, state string
	done, total                          uint64
	queue                                uint32
}
type share struct{ name, path string }
type download struct {
	filename string
	size     uint64
}

type model struct {
	ctx                                    context.Context
	client                                 *ipc.Client
	configPath                             string
	transient                              bool
	setup, help, confirm, editing, loading bool
	width, height                          int
	workspace, cursor                      int
	input, query, browseUser               string
	setupField                             int
	setupVals                              [5]string
	setupErr                               string
	status                                 snapshot
	results                                []result
	entries                                []entry
	transfers                              []transfer
	shares                                 []share
	selected                               map[int]bool
	err                                    string
}

func (m model) rows() int {
	switch m.workspace {
	case 0:
		return len(m.results)
	case 1:
		return len(m.entries)
	case 2:
		return len(m.transfers)
	default:
		return len(m.shares)
	}
}

func newModel(ctx context.Context, c *ipc.Client, path string, transient bool, cfg config.Config) model {
	return model{ctx: ctx, client: c, configPath: path, transient: transient, width: 80, height: 24, selected: map[int]bool{}, setupVals: [5]string{cfg.Soulseek.Username, cfg.Soulseek.Password, cfg.Soulseek.ListenAddr, cfg.DownloadDir, ""}}
}
func (m model) View() tea.View {
	var b strings.Builder
	if m.setup {
		b.WriteString(m.setupView())
	} else {
		b.WriteString(m.mainView())
	}
	if m.help {
		b.WriteString("\n\nHELP  tab/shift-tab workspace • arrows/j/k move • enter/space select • d download • / edit • ? help • q quit\nSource: https://github.com/catgirl-systems/slsk-tui • AGPL-3.0-only • no warranty")
	}
	if m.confirm {
		b.WriteString("\n\nActive transfers will be interrupted. Quit? [y/N]")
	}
	return tea.NewView(b.String())
}
func (m model) setupView() string {
	labels := []string{"Username", "Password", "Listen address", "Download path", "Share (name:path, optional)"}
	var b strings.Builder
	b.WriteString("slsk-tui setup\n\n")
	for i, l := range labels {
		v := m.setupVals[i]
		if i == 1 {
			v = strings.Repeat("•", utf8.RuneCountInString(v))
		}
		mark := " "
		if i == m.setupField {
			mark = ">"
		}
		fmt.Fprintf(&b, "%s %-27s %s\n", mark, l, v)
	}
	b.WriteString("\nenter: next/save • esc: quit\n")
	if m.setupErr != "" {
		b.WriteString("Error: " + m.setupErr + "\n")
	}
	return b.String()
}
func (m model) mainView() string {
	names := []string{"Search", "Browse", "Transfers", "Shares"}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  [%s]  %s  user:%s shares:%d transfers:%d\n", accent("slsk-tui"), names[m.workspace], m.status.status, m.status.user, len(m.shares), len(m.transfers))
	for i, n := range names {
		if i == m.workspace {
			fmt.Fprintf(&b, "%s ", accent("["+n+"]"))
		} else {
			fmt.Fprintf(&b, " %s  ", n)
		}
	}
	b.WriteString("\n\n")
	switch m.workspace {
	case 0:
		m.renderSearch(&b)
	case 1:
		m.renderBrowse(&b)
	case 2:
		m.renderTransfers(&b)
	case 3:
		m.renderShares(&b)
	}
	if m.err != "" {
		b.WriteString("\nError: " + m.err)
	}
	b.WriteString("\n\n")
	if m.workspace == 0 {
		b.WriteString("/ search  space select  d download")
	}
	if m.workspace == 1 {
		b.WriteString("/ username  space select  d download")
	}
	if m.workspace == 2 {
		b.WriteString("d cancel  r retry  c clear")
	}
	if m.workspace == 3 {
		b.WriteString("/ add (name:path)  d remove  r rescan")
	}
	b.WriteString("  • ? help  • q quit")
	return b.String()
}
func (m model) renderSearch(b *strings.Builder) {
	fmt.Fprintf(b, "Query: %s\n", m.query)
	if m.loading {
		b.WriteString("Searching…\n")
	}
	if len(m.results) == 0 && !m.loading {
		b.WriteString("No results. Type / then a query and press enter.\n")
		return
	}
	for i, x := range m.results {
		cursor, mark := " ", "[ ]"
		if i == m.cursor {
			cursor = ">"
		}
		if m.selected[i] {
			mark = "[x]"
		}
		availability := fmt.Sprintf("q:%d %dKiB/s", x.queue, x.speed/1024)
		if x.free {
			availability = "free " + availability
		}
		fmt.Fprintf(b, "%s%s %s  %s  %s  %d bytes\n", cursor, mark, trunc(x.user+"/"+x.path, m.width-38), map[bool]string{true: "dir", false: "file"}[x.directory], availability, x.size)
	}
}
func (m model) renderBrowse(b *strings.Builder) {
	fmt.Fprintf(b, "User: %s\n", m.browseUser)
	if m.loading {
		b.WriteString("Loading shares…\n")
	}
	if len(m.entries) == 0 && !m.loading {
		b.WriteString("No files. Type / for a username and press enter.\n")
		return
	}
	for i, x := range m.entries {
		cursor, mark := " ", "[ ]"
		if i == m.cursor {
			cursor = ">"
		}
		if m.selected[i] {
			mark = "[x]"
		}
		depth := strings.Count(strings.ReplaceAll(x.name, "\\", "/"), "/")
		fmt.Fprintf(b, "%s%s %*s%s  %d bytes\n", cursor, mark, depth*2, "", trunc(filepath.Base(strings.ReplaceAll(x.name, "\\", "/")), m.width-16), x.size)
	}
}
func (m model) renderTransfers(b *strings.Builder) {
	if len(m.transfers) == 0 {
		b.WriteString("No transfers. Queue a file from Search or Browse.\n")
		return
	}
	for i, x := range m.transfers {
		p := ""
		if x.total > 0 {
			p = fmt.Sprintf(" %d%%", x.done*100/x.total)
		}
		if x.queue > 0 {
			p += fmt.Sprintf(" queue:%d", x.queue)
		}
		fmt.Fprintf(b, "%s %s %s %s%s\n", map[bool]string{true: ">", false: " "}[i == m.cursor], trunc(x.filename, m.width-35), x.state, x.direction, p)
	}
}
func (m model) renderShares(b *strings.Builder) {
	if len(m.shares) == 0 {
		b.WriteString("No shares configured.\n")
		return
	}
	for i, x := range m.shares {
		fmt.Fprintf(b, "%s %s  %s\n", map[bool]string{true: ">", false: " "}[i == m.cursor], x.name, trunc(x.path, m.width-8))
	}
}
func trunc(s string, n int) string {
	if n < 4 {
		return ""
	}
	return ansi.Truncate(s, n, "…")
}

func accent(s string) string {
	if os.Getenv("NO_COLOR") != "" {
		return s
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Render(s)
}

func (m *model) setupKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "esc" {
		return tea.Quit
	}
	if s == "enter" {
		if m.setupField < 4 {
			m.setupField++
			return nil
		}
		cfg := config.Default()
		cfg.Soulseek.Username = strings.TrimSpace(m.setupVals[0])
		cfg.Soulseek.Password = m.setupVals[1]
		cfg.Soulseek.ListenAddr = strings.TrimSpace(m.setupVals[2])
		cfg.DownloadDir = strings.TrimSpace(m.setupVals[3])
		if x := strings.TrimSpace(m.setupVals[4]); x != "" {
			p := strings.SplitN(x, ":", 2)
			if len(p) != 2 {
				m.setupErr = "share must be name:path"
				return nil
			}
			cfg.Shares = []config.Share{{Name: strings.TrimSpace(p[0]), Path: strings.TrimSpace(p[1])}}
		}
		if cfg.Soulseek.Username == "" || cfg.Soulseek.Password == "" {
			m.setupErr = "username and password are required"
			return nil
		}
		if err := cfg.Save(m.configPath); err != nil {
			m.setupErr = err.Error()
			return nil
		}
		if m.client == nil {
			return tea.Quit
		}
		m.setup = false
		return nil
	}
	if s == "backspace" {
		v := m.setupVals[m.setupField]
		if len(v) > 0 {
			m.setupVals[m.setupField] = v[:len(v)-1]
		}
		return nil
	}
	if t := k.Key().Text; t != "" {
		m.setupVals[m.setupField] += t
	}
	return nil
}

func toResults(x []daemon.SearchResult) []result {
	r := make([]result, len(x))
	for i, v := range x {
		r[i] = result{user: v.Username, path: v.Path, size: v.Size, directory: v.Directory, free: v.SlotFree, speed: v.Speed, queue: v.Queue}
	}
	return r
}
func toEntries(x []soulseek.ShareEntry) []entry {
	r := make([]entry, len(x))
	for i, v := range x {
		r[i] = entry{v.Name, v.Size, v.Directory}
	}
	return r
}
func toTransfers(x []daemon.Transfer) []transfer {
	r := make([]transfer, len(x))
	for i, v := range x {
		r[i] = transfer{id: v.ID, user: v.Username, filename: v.Filename, direction: v.Direction, state: v.State, done: v.Done, total: v.Total, queue: v.Queue}
	}
	return r
}
func toShares(x []config.Share) []share {
	r := make([]share, len(x))
	for i, v := range x {
		r[i] = share{v.Name, v.Path}
	}
	return r
}
