package soulseek

// BrowseLimits bounds received share/folder responses, not search or server traffic.
// Byte limits bound wire payloads; decoded entries and the UI use additional memory.
type BrowseLimits struct {
	MaxEntries          int
	MaxCompressedSize   int
	MaxDecompressedSize int
}

func (l BrowseLimits) withDefaults() BrowseLimits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = maxShareEntries
	}
	if l.MaxCompressedSize <= 0 {
		l.MaxCompressedSize = MaxFrameSize
	}
	if l.MaxDecompressedSize <= 0 {
		l.MaxDecompressedSize = MaxDecompressedSize
	}
	return l
}

// ConfigureBrowseLimits applies to subsequent browses. In-flight browses keep a snapshot.
func (c *Client) ConfigureBrowseLimits(limits BrowseLimits) {
	c.mu.Lock()
	c.cfg.BrowseLimits = limits.withDefaults()
	c.mu.Unlock()
}

func (c *Client) BrowseLimits() BrowseLimits {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.BrowseLimits.withDefaults()
}
