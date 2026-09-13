package soulseek

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/netip"
	"strings"
	"time"
)

var ErrShareAddressPending = errors.New("soulseek: sharing address verification pending")

// ShareVisibility controls disclosure of a share root to one peer.
type ShareVisibility uint8

const (
	ShareHidden ShareVisibility = iota
	ShareLocked
	ShareAllowed
)

// SharePermission is the immutable snapshot returned by ClientConfig.SharePolicy.
// Roots is copied before use; nil means unrestricted only when no callback is configured.
type SharePermission struct {
	Banned       bool
	NeedsAddress bool // Deny until a bounded, separately scheduled address lookup finishes.
	Reason       string
	Roots        map[string]ShareVisibility
}

var errSharePermissionDenied = errors.New("soulseek: share permission denied")

type sharePermissionDenied struct{ reason string }

func (e *sharePermissionDenied) Error() string {
	if e.reason != "" {
		return e.reason
	}
	return errSharePermissionDenied.Error()
}

func cloneSharePermission(permission SharePermission) SharePermission {
	permission.Roots = maps.Clone(permission.Roots)
	return permission
}

func shareRoot(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if slash := strings.IndexByte(path, '/'); slash >= 0 {
		return path[:slash]
	}
	return path
}

func peerIP(addr net.Addr) netip.Addr {
	switch value := addr.(type) {
	case *net.TCPAddr:
		return value.AddrPort().Addr().Unmap()
	case *net.UDPAddr:
		return value.AddrPort().Addr().Unmap()
	case *net.IPAddr:
		if parsed, ok := netip.AddrFromSlice(value.IP); ok {
			return parsed.Unmap()
		}
	}
	return netip.Addr{}
}

func (c *Client) sharePermission(username string, address netip.Addr) SharePermission {
	callback := c.cfg.SharePolicy
	if callback == nil {
		return SharePermission{}
	}
	return cloneSharePermission(callback(username, address))
}

func (c *Client) shareVisibility(permission SharePermission, root string) ShareVisibility {
	if c.cfg.SharePolicy == nil {
		return ShareAllowed
	}
	if permission.Banned {
		return ShareHidden
	}
	return permission.Roots[root]
}

// Folder responses cannot encode locked flags. Disclose locked files only in
// search/shared-list responses, whose private sections preserve that distinction.
func (c *Client) filterShareEntries(entries []ShareEntry, permission SharePermission) []ShareEntry {
	if permission.Banned {
		return nil
	}
	out := make([]ShareEntry, 0, len(entries))
	for _, entry := range entries {
		visibility := c.shareVisibility(permission, shareRoot(entry.Name))
		if visibility != ShareAllowed {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// AnnounceShareCounts publishes only anonymously accessible roots.
func (c *Client) AnnounceShareCounts(ctx context.Context) error {
	return c.sendContext(ctx, c.shareCounts(c.shareIndex()))
}
func (c *Client) shareCounts(index *ShareIndex) (counts SharedCounts) {
	permission := c.sharePermission("", netip.Addr{})
	if permission.Banned {
		return counts
	}
	for _, file := range index.Files() {
		if c.shareVisibility(permission, file.Root) == ShareAllowed {
			if file.Directory {
				counts.Folders++
			} else {
				counts.Files++
			}
		}
	}
	return counts
}

type cachedShareAddress struct {
	address netip.Addr
	expires time.Time
}

func (c *Client) cachedPeerIP(username string) netip.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached := c.peerIPs[username]; time.Now().Before(cached.expires) {
		return cached.address
	}
	delete(c.peerIPs, username)
	return netip.Addr{}
}

func (c *Client) shareResponseContext() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shareResponseRoot
}

// The context is captured before evaluating permissions or building a response.
// Revocation aborts blocked writes and prevents publishing an obsolete snapshot.
func writeShareMessage(ctx context.Context, peer net.Conn, message Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = peer.Close() })
	defer stop()
	data, err := EncodeMessage(message)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeAll(peer, data)
}
