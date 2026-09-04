package tui

import (
	"sort"
	"strconv"
	"strings"
)

type treeNodeKind uint8

const (
	treeFolder treeNodeKind = iota
	treeFile
	treeUser
	treeShareRoot
)

type treeNode struct {
	id, parent, label, path, user, detail string
	kind                                  treeNodeKind
	source                                int
	children, leaves                      []int
	loaded, loading                       bool
	size, request                         uint64
}

type treeState struct {
	nodes           []treeNode
	byID            map[string]int
	roots, visible  []int
	expanded        map[string]bool
	defaultExpanded bool
}

func newTree(defaultExpanded bool, previous treeState) treeState {
	expanded := previous.expanded
	if expanded == nil {
		expanded = map[string]bool{}
	}
	return treeState{byID: map[string]int{}, expanded: expanded, defaultExpanded: defaultExpanded}
}

func treeParts(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
}

func treeID(parts ...string) string { return strings.Join(parts, "\x00") }

func (t *treeState) add(id, parent, label, path, user, detail string, kind treeNodeKind, source int) int {
	if i, ok := t.byID[id]; ok {
		if source >= 0 {
			t.nodes[i].source = source
		}
		return i
	}
	i := len(t.nodes)
	t.nodes = append(t.nodes, treeNode{id: id, parent: parent, label: label, path: path, user: user, detail: detail, kind: kind, source: source})
	t.byID[id] = i
	if parent == "" {
		t.roots = append(t.roots, i)
	} else if p, ok := t.byID[parent]; ok {
		t.nodes[p].children = append(t.nodes[p].children, i)
	}
	return i
}

func (t *treeState) expandedNode(node treeNode) bool {
	if value, ok := t.expanded[node.id]; ok {
		return value
	}
	return t.defaultExpanded
}

func (t *treeState) sortChildren(roots bool) {
	less := func(a, b int) bool {
		x, y := t.nodes[a], t.nodes[b]
		xDir, yDir := x.kind != treeFile, y.kind != treeFile
		if xDir != yDir {
			return xDir
		}
		return strings.ToLower(x.label) < strings.ToLower(y.label)
	}
	if roots {
		sort.SliceStable(t.roots, func(i, j int) bool { return less(t.roots[i], t.roots[j]) })
	}
	for i := range t.nodes {
		sort.SliceStable(t.nodes[i].children, func(a, b int) bool { return less(t.nodes[i].children[a], t.nodes[i].children[b]) })
	}
}

func (t *treeState) finish(previous treeState, cursor int) int {
	cursorID := previous.cursorID(cursor)
	for i := len(t.nodes) - 1; i >= 0; i-- {
		if t.nodes[i].kind == treeFile && t.nodes[i].source >= 0 {
			t.nodes[i].leaves = []int{t.nodes[i].source}
		}
		for _, child := range t.nodes[i].children {
			t.nodes[i].leaves = append(t.nodes[i].leaves, t.nodes[child].leaves...)
		}
	}
	t.rebuildVisible()
	if cursorID != "" {
		for i, node := range t.visible {
			if t.nodes[node].id == cursorID {
				return i
			}
		}
	}
	return max(0, min(cursor, len(t.visible)-1))
}

func (t *treeState) cursorID(cursor int) string {
	if cursor >= 0 && cursor < len(t.visible) {
		return t.nodes[t.visible[cursor]].id
	}
	return ""
}

func (t *treeState) rebuildVisible() {
	t.visible = t.visible[:0]
	var visit func(int)
	visit = func(index int) {
		t.visible = append(t.visible, index)
		node := t.nodes[index]
		if node.kind == treeFile || !t.expandedNode(node) {
			return
		}
		for _, child := range node.children {
			visit(child)
		}
	}
	for _, root := range t.roots {
		visit(root)
	}
}

func (t *treeState) node(cursor int) (int, *treeNode) {
	if cursor < 0 || cursor >= len(t.visible) {
		return -1, nil
	}
	index := t.visible[cursor]
	return index, &t.nodes[index]
}

func (t *treeState) cursorForSource(source int) int {
	for cursor, index := range t.visible {
		if t.nodes[index].source == source {
			return cursor
		}
	}
	return 0
}

func (t *treeState) toggle(cursor int) int {
	_, node := t.node(cursor)
	if node == nil || node.kind == treeFile {
		return cursor
	}
	t.expanded[node.id] = !t.expandedNode(*node)
	t.rebuildVisible()
	return max(0, min(cursor, len(t.visible)-1))
}

func (t *treeState) right(cursor int) int {
	_, node := t.node(cursor)
	if node == nil || node.kind == treeFile {
		return cursor
	}
	if !t.expandedNode(*node) {
		t.expanded[node.id] = true
		t.rebuildVisible()
		return cursor
	}
	if len(node.children) == 0 {
		return cursor
	}
	return min(cursor+1, len(t.visible)-1)
}

func (t *treeState) left(cursor int) int {
	_, node := t.node(cursor)
	if node == nil {
		return cursor
	}
	if node.kind != treeFile && t.expandedNode(*node) {
		t.expanded[node.id] = false
		t.rebuildVisible()
		return cursor
	}
	if node.parent != "" {
		for i, visible := range t.visible {
			if t.nodes[visible].id == node.parent {
				return i
			}
		}
	}
	return cursor
}

func (t *treeState) depth(index int) int {
	depth := 0
	for t.nodes[index].parent != "" {
		parent, ok := t.byID[t.nodes[index].parent]
		if !ok {
			break
		}
		depth++
		index = parent
	}
	return depth
}

func (t *treeState) selection(index int, selected map[int]bool) (chosen, total int) {
	for _, source := range t.nodes[index].leaves {
		total++
		if selected[source] {
			chosen++
		}
	}
	return chosen, total
}

func (m *model) currentTree() *treeState {
	switch m.workspace {
	case workspaceSearch:
		return &m.searchTree
	case workspaceBrowse:
		return &m.browseTree
	case workspaceTransfers:
		return &m.transferTrees[m.transferTab]
	case workspaceShares:
		return &m.shareTree
	default:
		return nil
	}
}

func buildSearchTree(results []result, previous treeState, cursor int) (treeState, int) {
	t := newTree(true, previous)
	duplicates := map[string]int{}
	for source, item := range results {
		userID := treeID("search", item.user)
		t.add(userID, "", item.user, "", item.user, "", treeUser, -1)
		parent, path := userID, ""
		parts := treeParts(item.path)
		for i, part := range parts {
			if path == "" {
				path = part
			} else {
				path += "\\" + part
			}
			last := i == len(parts)-1
			if last && !item.directory {
				base := treeID("search-file", item.user, path)
				ordinal := duplicates[base]
				duplicates[base]++
				t.add(base+"\x00"+strconv.Itoa(ordinal), parent, part, path, item.user, "", treeFile, source)
				continue
			}
			id := treeID("search-dir", item.user, path)
			t.add(id, parent, part, path, item.user, "", treeFolder, -1)
			parent = id
		}
	}
	t.sortChildren(false)
	return t, t.finish(previous, cursor)
}

func normalizeBrowseQuery(query string) string {
	return strings.ToLower(normalizeBrowsePath(strings.TrimSpace(query)))
}

func browseMatchCount(entries []entry, query string) int {
	query = normalizeBrowseQuery(query)
	count := 0
	for _, item := range entries {
		if query == "" || strings.Contains(strings.ToLower(normalizeBrowsePath(item.name)), query) {
			count++
		}
	}
	return count
}

func buildBrowseTree(entries []entry, query string, previous treeState, cursor int) (treeState, int) {
	t := newTree(true, previous)
	duplicates := map[string]int{}
	query = normalizeBrowseQuery(query)
	for source, item := range entries {
		if query != "" && !strings.Contains(strings.ToLower(normalizeBrowsePath(item.name)), query) {
			continue
		}
		parent, path := "", ""
		parts := treeParts(item.name)
		for i, part := range parts {
			if path == "" {
				path = part
			} else {
				path += "\\" + part
			}
			last := i == len(parts)-1
			if last && !item.directory {
				base := treeID("browse-file", path)
				ordinal := duplicates[base]
				duplicates[base]++
				t.add(base+"\x00"+strconv.Itoa(ordinal), parent, part, path, "", "", treeFile, source)
				continue
			}
			id := treeID("browse-dir", path)
			directorySource := -1
			if last {
				directorySource = source
			}
			t.add(id, parent, part, path, "", "", treeFolder, directorySource)
			parent = id
		}
	}
	t.sortChildren(true)
	return t, t.finish(previous, cursor)
}

func buildTransferTree(transfers []transfer, direction string, previous treeState, cursor int) (treeState, int) {
	t := newTree(true, previous)
	for source, item := range transfers {
		if item.direction != direction {
			continue
		}
		userID := treeID("transfer", direction, item.user)
		t.add(userID, "", item.user, "", item.user, "", treeUser, -1)
		parent, path := userID, ""
		parts := treeParts(item.filename)
		for i, part := range parts {
			if path == "" {
				path = part
			} else {
				path += "\\" + part
			}
			if i == len(parts)-1 {
				t.add(treeID("transfer-file", direction, item.id), parent, part, path, item.user, "", treeFile, source)
				continue
			}
			id := treeID("transfer-dir", direction, item.user, path)
			t.add(id, parent, part, path, item.user, "", treeFolder, -1)
			parent = id
		}
	}
	t.sortChildren(true)
	return t, t.finish(previous, cursor)
}

func buildShareRoots(shares []share, previous treeState, cursor int, reset bool) (treeState, int) {
	if reset {
		previous = treeState{}
	}
	t := newTree(false, previous)
	var clone func(int, string)
	clone = func(oldIndex int, parent string) {
		old := previous.nodes[oldIndex]
		index := t.add(old.id, parent, old.label, old.path, old.user, old.detail, old.kind, old.source)
		t.nodes[index].loaded, t.nodes[index].loading = old.loaded, false
		t.nodes[index].size = old.size
		for _, child := range old.children {
			clone(child, old.id)
		}
	}
	for source, item := range shares {
		id := treeID("share", item.name)
		index := t.add(id, "", item.name, item.name, "", item.path, treeShareRoot, source)
		if old, ok := previous.byID[id]; ok {
			t.nodes[index].loaded = previous.nodes[old].loaded
			for _, child := range previous.nodes[old].children {
				clone(child, id)
			}
		}
	}
	t.sortChildren(true)
	return t, t.finish(previous, cursor)
}

func sameShareRoots(shares []share, tree treeState) bool {
	if len(shares) != len(tree.roots) {
		return false
	}
	for _, item := range shares {
		index, ok := tree.byID[treeID("share", item.name)]
		if !ok || tree.nodes[index].detail != item.path {
			return false
		}
	}
	return true
}

func (t *treeState) addShareChildren(parentID string, entries []entry, cursor int) int {
	cursorID := t.cursorID(cursor)
	parentIndex, ok := t.byID[parentID]
	if !ok {
		return cursor
	}
	parent := &t.nodes[parentIndex]
	parent.loaded, parent.loading = true, false
	for _, item := range entries {
		path := parent.path + "\\" + item.name
		kind := treeFile
		if item.directory {
			kind = treeFolder
		}
		id := treeID("share-node", path)
		index := t.add(id, parent.id, item.name, path, "", "", kind, -1)
		t.nodes[index].size = item.size
		t.nodes[index].loaded = !item.directory
	}
	t.sortChildren(false)
	t.rebuildVisible()
	for i, index := range t.visible {
		if t.nodes[index].id == cursorID {
			return i
		}
	}
	return max(0, min(cursor, len(t.visible)-1))
}
