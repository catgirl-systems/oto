package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/catgirl-systems/oto/internal/soulseek"
)

const browsePageBytes = 512 << 10

type browseDirectory struct {
	path                 string
	parent               int
	private              bool
	children             []int
	files                []soulseek.ShareEntry // Basenames; the directory path is stored once.
	firstFile, fileCount int
}

type browseSnapshot struct {
	dirs   []browseDirectory // Index zero is the implicit root; IDs are slice indexes.
	byPath map[string]int
	total  int
}

func browsePath(path string) string {
	return strings.Join(strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }), "\\")
}

func newBrowseSnapshot(groups []soulseek.ShareDirectory, limits soulseek.BrowseLimits) (*browseSnapshot, error) {
	x := &browseSnapshot{dirs: []browseDirectory{{parent: -1}}, byPath: map[string]int{"": 0}}
	files, pathBytes := 0, 0
	for _, group := range groups {
		files += len(group.Files)
	}
	if files > limits.MaxEntries {
		return nil, fmt.Errorf("%w: browse files exceed configured entry budget", soulseek.ErrTooLarge)
	}
	ensure := func(name string, private bool) (int, error) {
		name = browsePath(name)
		if strings.Count(name, "\\") >= 256 {
			return 0, fmt.Errorf("%w: browse directory nesting exceeds 256", soulseek.ErrTooLarge)
		}
		var missing []string
		current := name
		for {
			if _, ok := x.byPath[current]; ok {
				break
			}
			missing = append(missing, current)
			cut := strings.LastIndexByte(current, '\\')
			if cut < 0 {
				current = ""
			} else {
				current = current[:cut]
			}
		}
		parent := x.byPath[current]
		for i := len(missing) - 1; i >= 0; i-- {
			pathBytes += len(missing[i])
			if files+len(x.dirs) > limits.MaxEntries || pathBytes > limits.MaxDecompressedSize {
				return 0, fmt.Errorf("%w: browse directory index exceeds configured entry/path budget", soulseek.ErrTooLarge)
			}
			id := len(x.dirs)
			x.dirs = append(x.dirs, browseDirectory{path: missing[i], parent: parent, private: private})
			x.dirs[parent].children = append(x.dirs[parent].children, id)
			x.byPath[missing[i]] = id
			parent = id
		}
		if !private {
			for p := parent; p > 0; p = x.dirs[p].parent {
				x.dirs[p].private = false
			}
		}
		return parent, nil
	}
	for _, group := range groups {
		id, err := ensure(group.Name, group.Private)
		if err != nil {
			return nil, err
		}
		// Some peers send a relative subpath instead of a basename. Keep that hierarchy.
		local := group.Files[:0]
		for _, file := range group.Files {
			if cut := strings.LastIndexAny(file.Name, "/\\"); cut >= 0 {
				folder := x.dirs[id].path + "\\" + file.Name[:cut]
				target, err := ensure(folder, file.Private)
				if err != nil {
					return nil, err
				}
				file.Name = strings.Clone(file.Name[cut+1:])
				x.dirs[target].files = append(x.dirs[target].files, file)
			} else {
				local = append(local, file)
			}
		}
		clear(group.Files[len(local):])
		group.Files = local
		if x.dirs[id].files == nil {
			x.dirs[id].files = group.Files
		} else {
			x.dirs[id].files = append(x.dirs[id].files, group.Files...)
		}
	}
	next := len(x.dirs)
	for i := range x.dirs {
		d := &x.dirs[i]
		sort.SliceStable(d.children, func(a, b int) bool {
			return strings.ToLower(x.dirs[d.children[a]].path) < strings.ToLower(x.dirs[d.children[b]].path)
		})
		sort.SliceStable(d.files, func(a, b int) bool { return strings.ToLower(d.files[a].Name) < strings.ToLower(d.files[b].Name) })
		d.firstFile, d.fileCount = next, len(d.files)
		next += len(d.files)
	}
	for i := len(x.dirs) - 1; i > 0; i-- {
		d := &x.dirs[i]
		x.dirs[d.parent].fileCount += d.fileCount
	}
	x.total = next - 1
	return x, nil
}

func groupBrowseEntries(entries []soulseek.ShareEntry) []soulseek.ShareDirectory {
	var groups []soulseek.ShareDirectory
	byPath := map[string]int{}
	for _, item := range entries {
		name := browsePath(item.Name)
		folder := name
		if !item.Directory {
			if cut := strings.LastIndexByte(name, '\\'); cut >= 0 {
				folder, item.Name = name[:cut], strings.Clone(name[cut+1:])
			} else {
				folder, item.Name = "", name
			}
		}
		id, ok := byPath[folder]
		if !ok {
			folder = strings.Clone(folder)
			id = len(groups)
			byPath[folder] = id
			groups = append(groups, soulseek.ShareDirectory{Name: folder, Private: item.Private})
		}
		if !item.Private {
			groups[id].Private = false
		}
		if !item.Directory {
			groups[id].Files = append(groups[id].Files, item)
		}
	}
	return groups
}

func (x *browseSnapshot) folder(path string) (int, error) {
	path = browsePath(path)
	if id, ok := x.byPath[path]; ok {
		return id, nil
	}
	for name, id := range x.byPath {
		if strings.EqualFold(name, path) {
			return id, nil
		}
	}
	return 0, errors.New("daemon: browse folder not found")
}

func (x *browseSnapshot) ancestors(folder int) []int {
	var ids []int
	for p := folder; p > 0; p = x.dirs[p].parent {
		ids = append(ids, p)
	}
	for a, b := 0, len(ids)-1; a < b; a, b = a+1, b-1 {
		ids[a], ids[b] = ids[b], ids[a]
	}
	return ids
}

func (x *browseSnapshot) directoryEntry(id int) BrowseEntry {
	d := &x.dirs[id]
	return BrowseEntry{ID: id, Ancestors: x.ancestors(d.parent), ChildCount: len(d.children) + len(d.files), FileCount: d.fileCount,
		ShareEntry: soulseek.ShareEntry{Name: d.path, Directory: true, Private: d.private}}
}
func (x *browseSnapshot) fileEntry(folder, file int) BrowseEntry {
	d := &x.dirs[folder]
	entry := d.files[file]
	if d.path != "" {
		entry.Name = d.path + "\\" + entry.Name
	}
	return BrowseEntry{ID: d.firstFile + file, Ancestors: x.ancestors(folder), ShareEntry: entry}
}

func (x *browseSnapshot) page(ctx context.Context, req BrowsePageRequest) (BrowsePage, error) {
	if req.Cursor < 0 || len(req.Query) > 4096 || len(req.Folder) > soulseek.MaxStringSize {
		return BrowsePage{}, errors.New("daemon: invalid browse page")
	}
	folder, err := x.folder(req.Folder)
	if err != nil {
		return BrowsePage{}, err
	}
	query := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(req.Query, "/", "\\")))
	out := BrowsePage{Folder: x.dirs[folder].path, Query: query, Cursor: req.Cursor, TotalEntries: x.total, Entries: []BrowseEntry{}}
	for _, id := range x.ancestors(folder) {
		out.Ancestors = append(out.Ancestors, x.directoryEntry(id))
	}
	encoded, err := json.Marshal(out.Ancestors)
	if err != nil {
		return out, err
	}
	used := len(encoded)
	if used > browsePageBytes {
		return out, fmt.Errorf("%w: browse path exceeds page byte limit", soulseek.ErrTooLarge)
	}
	full := false
	emit := func(id, file int) error {
		position := out.Total
		out.Total++
		if position < req.Cursor || full {
			return nil
		}
		var entry BrowseEntry
		if file < 0 {
			entry = x.directoryEntry(id)
		} else {
			entry = x.fileEntry(id, file)
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if len(data) > browsePageBytes {
			return fmt.Errorf("%w: browse entry exceeds page byte limit", soulseek.ErrTooLarge)
		}
		if len(out.Entries) == BrowsePageSize || used+len(data) > browsePageBytes {
			if len(out.Entries) == 0 {
				return fmt.Errorf("%w: browse path and entry exceed page byte limit", soulseek.ErrTooLarge)
			}
			out.NextCursor = position
			full = true
			return nil
		}
		out.Entries = append(out.Entries, entry)
		used += len(data)
		return nil
	}
	if query == "" {
		d := &x.dirs[folder]
		for _, id := range d.children {
			if err := emit(id, -1); err != nil {
				return out, err
			}
		}
		for i := range d.files {
			if err := emit(folder, i); err != nil {
				return out, err
			}
		}
	} else {
		// ponytail: global find scans the snapshot; index only if measured query latency warrants it.
		prefix := strings.ToLower(x.dirs[folder].path) + "\\"
		for id := range x.dirs {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			d := &x.dirs[id]
			lower := strings.ToLower(d.path)
			if folder != 0 && id != folder && !strings.HasPrefix(lower, prefix) {
				continue
			}
			matchesFolder := strings.Contains(lower, query)
			if id != 0 && matchesFolder {
				if err := emit(id, -1); err != nil {
					return out, err
				}
			}
			for i, file := range d.files {
				if i%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return out, err
					}
				}
				matches := matchesFolder || strings.Contains(strings.ToLower(file.Name), query)
				if !matches && strings.Contains(query, "\\") {
					matches = strings.Contains(lower+"\\"+strings.ToLower(file.Name), query)
				}
				if matches {
					if err := emit(id, i); err != nil {
						return out, err
					}
				}
			}
		}
	}
	if req.Cursor > out.Total {
		return out, errors.New("daemon: browse cursor out of range")
	}
	return out, ctx.Err()
}

func (s *Service) loadedBrowse(username string, revision uint64) (loadedBrowse, error) {
	key, err := browseUsername(username)
	if err != nil {
		return loadedBrowse{}, err
	}
	s.mu.RLock()
	loaded, ok := s.browses[key]
	s.mu.RUnlock()
	if !ok || loaded.snapshot == nil {
		return loadedBrowse{}, ErrBrowseNotLoaded
	}
	if revision == 0 || loaded.result.Revision != revision {
		return loadedBrowse{}, ErrBrowseRevision
	}
	return loaded, nil
}

func (s *Service) BrowsePage(ctx context.Context, req BrowsePageRequest) (BrowsePage, error) {
	loaded, err := s.loadedBrowse(req.Username, req.Revision)
	if err != nil {
		return BrowsePage{}, err
	}
	out, err := loaded.snapshot.page(ctx, req)
	out.Revision, out.Cached, out.SavedAt = loaded.result.Revision, loaded.result.Cached, loaded.result.SavedAt
	return out, err
}

func (s *Service) OpenBrowse(ctx context.Context, username, folder, query string) (BrowsePage, error) {
	key, err := browseUsername(username)
	if err != nil {
		return BrowsePage{}, err
	}
	s.mu.Lock()
	client, browse, cfg := s.client, s.fullBrowseDirectories, s.cfg
	s.browseSeq++
	request := s.browseSeq
	previous := s.browses[key]
	previous.request = request
	s.browses[key] = previous
	s.mu.Unlock()
	var groups []soulseek.ShareDirectory
	var remoteErr error
	cached := false
	var result BrowseResult
	if client != nil {
		_, generation, progress := s.beginBrowseProgress(username)
		groups, remoteErr = browse(ctx, client, strings.TrimSpace(username), progress)
		s.finishBrowseProgress(key, generation, remoteErr == nil)
	}
	if client == nil || remoteErr != nil {
		if err := ctx.Err(); err != nil {
			return BrowsePage{}, err
		}
		cache, err := s.loadRemoteShareCache(username)
		if err != nil {
			if remoteErr != nil {
				return BrowsePage{}, remoteErr
			}
			return BrowsePage{}, err
		}
		groups, cached, result.SavedAt = groupBrowseEntries(cache.Entries), true, cache.SavedAt
	}
	x, err := newBrowseSnapshot(groups, browseLimits(cfg))
	if err != nil {
		return BrowsePage{}, err
	}
	if err := ctx.Err(); err != nil {
		return BrowsePage{}, err
	}
	// Resolve a jump before publishing, so a bad path cannot replace a valid snapshot.
	page, err := x.page(ctx, BrowsePageRequest{Folder: folder, Query: query})
	if err != nil {
		return BrowsePage{}, err
	}
	s.mu.Lock()
	if s.browses[key].request != request {
		s.mu.Unlock()
		return BrowsePage{}, ErrBrowseRevision
	}
	result.Revision, result.Cached = request, cached
	s.browses[key] = loadedBrowse{username: strings.TrimSpace(username), result: result, snapshot: x, request: request}
	s.mu.Unlock()
	page.Revision, page.Cached, page.SavedAt = result.Revision, result.Cached, result.SavedAt
	return page, nil
}

func (x *browseSnapshot) flatEntries() []soulseek.ShareEntry {
	// ponytail: explicit saving still flattens paths for the existing cache writer; stream rows if saving becomes a memory bottleneck.
	entries := make([]soulseek.ShareEntry, 0, x.total)
	for id, d := range x.dirs {
		if id != 0 {
			entries = append(entries, soulseek.ShareEntry{Name: d.path, Directory: true, Private: d.private})
		}
		for _, entry := range d.files {
			if d.path != "" {
				entry.Name = d.path + "\\" + entry.Name
			}
			entries = append(entries, entry)
		}
	}
	return entries
}

func (s *Service) QueueBrowse(ctx context.Context, req BrowseDownloadRequest) (BrowseDownloadResult, error) {
	loaded, err := s.loadedBrowse(req.Username, req.Revision)
	if err != nil {
		return BrowseDownloadResult{}, err
	}
	x := loaded.snapshot
	for id := range req.Selection {
		if id <= 0 || id > x.total {
			return BrowseDownloadResult{}, errors.New("daemon: invalid browse selection")
		}
	}
	folder := -1
	if req.Folder != "" {
		folder, err = x.folder(req.Folder)
		if err != nil {
			return BrowseDownloadResult{}, err
		}
	}
	var items []DownloadItem
	seen := map[string]bool{}
	for id, d := range x.dirs {
		if err := ctx.Err(); err != nil {
			return BrowseDownloadResult{}, err
		}
		selected := false
		if folder >= 0 {
			selected = id == folder
			if req.Recursive {
				for p := id; p >= 0; p = x.dirs[p].parent {
					if p == folder {
						selected = true
						break
					}
				}
			}
		} else {
			for p := id; p > 0; p = x.dirs[p].parent {
				if rule, ok := req.Selection[p]; ok {
					selected = rule
					break
				}
			}
		}
		for i := range d.files {
			choose := selected
			if folder < 0 {
				if rule, ok := req.Selection[d.firstFile+i]; ok {
					choose = rule
				}
			}
			if !choose {
				continue
			}
			entry := x.fileEntry(id, i).ShareEntry
			name, err := soulseek.NormalizePath(entry.Name)
			if err != nil {
				return BrowseDownloadResult{}, err
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			items = append(items, DownloadItem{Filename: name, Size: entry.Size})
		}
	}
	if len(items) == 0 {
		return BrowseDownloadResult{}, errors.New("daemon: no downloadable files selected")
	}
	s.mu.RLock()
	dir := strings.TrimSpace(req.DownloadDir)
	if dir == "" {
		dir = s.cfg.DownloadDir
	}
	items = withoutExistingFolderDownloads(items, s.journal.Downloads, req.Username, dir, s.cfg.DownloadDir)
	s.mu.RUnlock()
	if _, err := s.loadedBrowse(req.Username, req.Revision); err != nil {
		return BrowseDownloadResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return BrowseDownloadResult{}, err
	}
	out, err := s.QueueDownloads([]DownloadRequest{{Username: loaded.username, DownloadDir: req.DownloadDir, Files: items}})
	return BrowseDownloadResult{Queued: len(out)}, err
}
