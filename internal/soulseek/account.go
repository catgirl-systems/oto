package soulseek

// AccountIdentity belongs to this connection, not to later configuration edits.
func (c *Client) AccountIdentity() (server, username string) { return c.cfg.Address, c.cfg.Username }
