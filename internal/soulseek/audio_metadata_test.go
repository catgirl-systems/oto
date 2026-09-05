package soulseek

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAudioProbeMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	probe := NewAudioProbe()
	if probe.Available() || probe.ErrorDescription() == "" {
		t.Fatalf("missing ffprobe reported as available: %#v", probe)
	}
}

func TestAudioProbeParsingAndBounds(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffprobe")
	writeFakeFFProbe(t, fake, `
if [ "$1" = "-version" ]; then echo 'ffprobe version fake-1'; exit 0; fi
printf '%s' '{"streams":[{"codec_type":"video"},{"codec_type":"audio","bit_rate":"192000","duration":"12.75","sample_rate":48000,"bits_per_sample":24}],"format":{"duration":"99"}}'
`)
	t.Setenv("PATH", dir)
	path := filepath.Join(dir, "song.flac")
	if err := os.WriteFile(path, []byte("not really audio"), 0600); err != nil {
		t.Fatal(err)
	}
	probe := NewAudioProbe()
	got, err := probe.Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	want := AudioMetadata{Bitrate: 192, Duration: 12, SampleRate: 48000, BitDepth: 24}
	if got != want {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
	for _, suffix := range []string{" {}", " trailing"} {
		if _, err := parseAudioProbe([]byte(`{"streams":[{"codec_type":"audio"}]}` + suffix)); err == nil {
			t.Fatalf("accepted trailing JSON content: %q", suffix)
		}
	}

	writeFakeFFProbe(t, fake, `
if [ "$1" = "-version" ]; then echo 'ffprobe version fake-1'; exit 0; fi
printf '%*s' 1100000 x
`)
	if _, err := probe.Probe(context.Background(), path); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("large output error = %v", err)
	}
}

func TestAudioProbeCancellation(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffprobe")
	writeFakeFFProbe(t, fake, `
if [ "$1" = "-version" ]; then echo 'ffprobe version fake-1'; exit 0; fi
sleep 30
`)
	t.Setenv("PATH", dir)
	path := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	probe := NewAudioProbe()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := probe.Probe(ctx, path)
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("cancellation error = %v, elapsed = %s", err, time.Since(start))
	}
}

func TestSupportedAudioExtensions(t *testing.T) {
	for _, ext := range []string{"mp1", "MP2", "mp3", "flac", "wav", "aif", "aiff", "ogg", "opus", "aac", "m4a", "m4b", "wma", "ape", "wv", "mpc", "dsf", "dff"} {
		if !IsSupportedAudioExtension("track." + ext) {
			t.Errorf(".%s not supported", ext)
		}
	}
	for _, path := range []string{"track.txt", "track", "http://host/a.mp3"} {
		if IsSupportedAudioExtension(path) == (path != "http://host/a.mp3") {
			if path != "http://host/a.mp3" {
				t.Errorf("%s unexpectedly supported", path)
			}
		}
	}
}

func TestAudioFingerprint(t *testing.T) {
	probe := &AudioProbe{version: "ffprobe version test"}
	path := filepath.Join(t.TempDir(), "track.wav")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	fp, err := probe.Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if fp.Size != 3 || fp.MTimeUnixNano == 0 || fp.ExtractorVersion == "" {
		t.Fatalf("fingerprint = %#v", fp)
	}
}

func TestAudioProbeGeneratedWAV(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is absent")
	}
	path := filepath.Join(t.TempDir(), "generated.wav")
	writeWAV(t, path, 8000, 16, 1)
	probe := NewAudioProbe()
	got, err := probe.Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SampleRate != 8000 || got.BitDepth != 16 || got.Duration != 1 {
		t.Fatalf("metadata = %#v", got)
	}
}

func writeFakeFFProbe(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
}

func writeWAV(t *testing.T, path string, sampleRate, bits, seconds int) {
	t.Helper()
	channels, samples := 1, sampleRate*seconds
	blockAlign := channels * bits / 8
	dataSize := samples * blockAlign
	b := make([]byte, 44+dataSize)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+dataSize))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], uint16(channels))
	binary.LittleEndian.PutUint32(b[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(b[28:], uint32(sampleRate*blockAlign))
	binary.LittleEndian.PutUint16(b[32:], uint16(blockAlign))
	binary.LittleEndian.PutUint16(b[34:], uint16(bits))
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(dataSize))
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
