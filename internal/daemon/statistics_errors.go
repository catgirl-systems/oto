package daemon

import (
	"net/netip"
	"regexp"
	"strings"
)

// net.OpError strings can contain both local and peer endpoints. Strip them
// before errors enter durable statistics, not just at display time.
var statsAddresses = regexp.MustCompile(`\[[0-9a-fA-F:.]+(?:%[a-zA-Z0-9_.-]+)?\](?::[0-9]+)?|[0-9a-fA-F:.]*:[0-9a-fA-F:.]+(?:%[a-zA-Z0-9_.-]+)?|\b[0-9]{1,3}(?:\.[0-9]{1,3}){3}\b`)

func statsError(message string) string {
	return statsAddresses.ReplaceAllStringFunc(message, func(candidate string) string {
		trailing := ""
		if strings.HasSuffix(candidate, ":") && !strings.HasSuffix(candidate, "::") {
			candidate = strings.TrimSuffix(candidate, ":")
			trailing = ":"
		}
		if _, err := netip.ParseAddrPort(candidate); err == nil {
			return "[address]" + trailing
		}
		if _, err := netip.ParseAddr(strings.Trim(candidate, "[]")); err == nil {
			return "[address]" + trailing
		}
		return candidate + trailing
	})
}
