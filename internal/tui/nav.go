package tui

// The TUI has many lists that move a cursor with the same keys. These helpers
// keep that behaviour in one place so screens cannot drift apart.

// navKey moves a bounded list cursor for the shared navigation keys: up/k,
// down/j, pgup, pgdown, home and end. last is the highest valid index and step
// the page size. It reports whether the key was handled.
func navKey(key string, cursor *int, last, step int) bool {
	switch key {
	case "up", "k":
		*cursor = max(0, *cursor-1)
	case "down", "j":
		*cursor = min(last, *cursor+1)
	case "pgup":
		*cursor = max(0, *cursor-step)
	case "pgdown":
		*cursor = min(last, *cursor+step)
	case "home":
		*cursor = 0
	case "end":
		*cursor = last
	default:
		return false
	}
	return true
}

// selectKey is navKey for menus that only move one row at a time.
func selectKey(key string, choice *int, last int) bool {
	switch key {
	case "up", "k":
		*choice = max(0, *choice-1)
	case "down", "j":
		*choice = min(last, *choice+1)
	default:
		return false
	}
	return true
}

// scrollKey moves an unbounded scroll offset with the shared navigation keys.
// end jumps past any plausible content so the view clamps to the bottom.
func scrollKey(key string, offset *int, step int) bool {
	switch key {
	case "up", "k":
		*offset = max(0, *offset-1)
	case "down", "j":
		*offset++
	case "pgup":
		*offset = max(0, *offset-step)
	case "pgdown":
		*offset += step
	case "home":
		*offset = 0
	case "end":
		*offset = 1 << 20
	default:
		return false
	}
	return true
}

// dialogScrollKey moves a confirmation-dialog scroll offset, where up and pgup
// both step a whole page. end jumps to the end of a label of the given length.
func dialogScrollKey(key string, offset *int, step, label int) bool {
	switch key {
	case "up", "pgup":
		*offset = max(0, *offset-step)
	case "down", "pgdown":
		*offset += step
	case "home":
		*offset = 0
	case "end":
		*offset = label
	default:
		return false
	}
	return true
}
