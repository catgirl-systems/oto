package soulseek

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

type AudioScan struct {
	Extracted   uint64 `json:"extracted"`
	Cached      uint64 `json:"cached"`
	Failed      uint64 `json:"failed"`
	Unavailable string `json:"unavailable,omitempty"`
}

// ExtractAudio enriches an unpublished shadow index. Each worker owns one file.
func (s *ShareIndex) ExtractAudio(ctx context.Context, previous *ShareIndex, report func(AudioScan)) (AudioScan, error) {
	found := false
	for _, f := range s.files {
		if !f.Directory && IsSupportedAudioExtension(f.Path) {
			found = true
			break
		}
	}
	if !found {
		return AudioScan{}, ctx.Err()
	}
	probe := newAudioProbe(ctx)
	status := AudioScan{}
	if !probe.Available() {
		status.Unavailable = probe.ErrorDescription() + "; install ffmpeg for audio metadata"
		if report != nil {
			report(status)
		}
		return status, ctx.Err()
	}
	old := make(map[string]ShareFile)
	if previous != nil {
		for _, f := range previous.files {
			if f.AudioSource != "" {
				old[f.AudioSource] = f
			}
		}
	}
	roots := make(map[string]string)
	for _, root := range s.Roots() {
		roots[root.Name] = root.Path
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				f := &s.files[i]
				source := filepath.Join(roots[f.Root], filepath.FromSlash(f.Path))
				fingerprint, err := probe.Fingerprint(source)
				if err == nil && fingerprint.Size != f.Size {
					err = ErrMalformed
				}
				cached := old[source]
				hit := err == nil && cached.AudioSource == source && cached.AudioFingerprint == fingerprint
				if hit {
					f.AudioMetadata, f.AudioFingerprint, f.AudioSource = cached.AudioMetadata, fingerprint, source
				} else if err == nil {
					var metadata AudioMetadata
					metadata, err = probe.Probe(ctx, source)
					if err == nil {
						after, statErr := probe.Fingerprint(source)
						if statErr == nil && after == fingerprint {
							f.AudioMetadata, f.AudioFingerprint, f.AudioSource = metadata, fingerprint, source
						} else {
							err = ErrMalformed
						}
					}
				}
				mu.Lock()
				if err != nil {
					status.Failed++
				} else if hit {
					status.Cached++
				} else {
					status.Extracted++
				}
				if report != nil {
					report(status)
				}
				mu.Unlock()
			}
		}()
	}
scan:
	for i, f := range s.files {
		if f.Directory || !IsSupportedAudioExtension(f.Path) {
			continue
		}
		select {
		case jobs <- i:
		case <-ctx.Done():
			break scan
		}
	}
	close(jobs)
	wg.Wait()
	return status, ctx.Err()
}

func (f ShareFile) entry(name string) ShareEntry {
	return ShareEntry{Name: name, Size: f.Size, Directory: f.Directory, Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(f.Path)), "."), Bitrate: f.Bitrate, Duration: f.Duration, SampleRate: f.SampleRate, BitDepth: f.BitDepth}
}
