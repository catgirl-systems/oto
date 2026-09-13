package soulseek

import (
	"errors"
	"os"
)

// UploadSnapshot captures a currently shared file, never an arbitrary local path.
// Fingerprint and size come from the same stat; admission rechecks the fingerprint.
type UploadSnapshot struct {
	Filename    string
	Size        uint64
	Fingerprint string
}

func (c *Client) PreviewUpload(username, filename string) (UploadSnapshot, error) {
	if err := ValidateUsername(username); err != nil {
		return UploadSnapshot{}, err
	}
	wire, local, _, err := c.validateUpload(username, filename)
	if err != nil {
		return UploadSnapshot{}, err
	}
	info, err := os.Stat(local)
	if err != nil {
		return UploadSnapshot{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return UploadSnapshot{}, errors.New("soulseek: shared path is not a regular file")
	}
	fingerprint, err := fileInfoFingerprint(info)
	if err != nil {
		return UploadSnapshot{}, err
	}
	out := UploadSnapshot{Filename: wire, Size: uint64(info.Size()), Fingerprint: fingerprint}
	// No lookup or upload is initiated by a preview. An unresolved address is a
	// visible permission failure, not an assumption that access will be granted.
	return out, c.checkUploadPermission(username, wire, c.cachedPeerIP(username))
}
func (c *Client) QueueUploadSnapshot(username string, snapshot UploadSnapshot) (UploadTarget, bool, error) {
	if err := ValidateUsername(username); err != nil {
		return UploadTarget{}, false, err
	}
	if snapshot.Fingerprint == "" {
		return UploadTarget{}, false, errors.New("soulseek: a captured file fingerprint is required")
	}
	attempt, started, err := c.registerUploadWithOptions(username, snapshot.Filename, false, false, snapshot.Fingerprint)
	if err != nil {
		return UploadTarget{}, false, err
	}
	return attempt.target, started, nil
}
