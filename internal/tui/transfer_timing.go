package tui

import "fmt"

func addSaturated(a, b uint64) uint64 {
	if b > ^uint64(0)-a {
		return ^uint64(0)
	}
	return a + b
}

func (m model) transferTimes(node treeNode) (elapsed, eta *uint64) {
	if node.kind == treeFile && node.source >= 0 {
		x := m.transfers[node.source]
		return x.elapsedMS, x.etaSeconds
	}
	var ms, remaining, speed uint64
	known, unknown, blocked, completed := false, false, false, true
	for _, i := range node.leaves {
		x := m.transfers[i]
		if x.elapsedMS != nil {
			known = true
			ms = addSaturated(ms, *x.elapsedMS)
		} else if x.state != "queued" && x.state != "incomplete" {
			unknown = true
		}
		if x.total > x.done {
			remaining = addSaturated(remaining, x.total-x.done)
		}
		if x.state == "running" {
			speed = addSaturated(speed, x.speed)
		}
		if x.state != "completed" {
			completed = false
			if x.state != "running" && x.state != "queued" && x.state != "incomplete" {
				blocked = true
			}
		}
	}
	if known && !unknown {
		elapsed = &ms
	}
	seconds := uint64(0)
	if completed {
		eta = &seconds
	} else if !blocked && speed > 0 {
		if remaining > 0 {
			seconds = (remaining-1)/speed + 1
		}
		eta = &seconds
	}
	return elapsed, eta
}

func optionalDuration(value *uint64, milliseconds bool) string {
	if value == nil {
		return "—"
	}
	n := *value
	if milliseconds {
		n /= 1000
	}
	return formatDuration(n)
}

func (m model) transferTimeText(node treeNode) string {
	elapsed, eta := m.transferTimes(node)
	label := "Elapsed"
	if node.kind != treeFile {
		label += " (sum)"
	}
	return fmt.Sprintf("%s %s  ETA %s", label, optionalDuration(elapsed, true), optionalDuration(eta, false))
}
