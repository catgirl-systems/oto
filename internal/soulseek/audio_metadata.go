package soulseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const audioProbeTimeout = 10 * time.Second
const maxAudioProbeOutput = 1 << 20
const maxAudioProbeStderr = 64 << 10

// AudioMetadata contains values that ffprobe reported for the first audio stream.
// Bitrate is in kbit/s and Duration is in seconds. A zero value means unknown.
type AudioMetadata struct {
	Bitrate    uint32
	Duration   uint32
	SampleRate uint32
	BitDepth   uint32
}

// AudioFingerprint identifies the local file and the extractor used to inspect it.
type AudioFingerprint struct {
	Size             uint64
	MTimeUnixNano    int64
	CTimeUnixNano    int64
	ExtractorVersion string
}

// AudioProbe is an optional ffprobe-backed metadata extractor.
type AudioProbe struct {
	executable string
	version    string
	err        error
}

// NewAudioProbe resolves ffprobe and records its version once. Missing ffprobe
// is represented by an unavailable probe rather than a constructor error.
func NewAudioProbe() *AudioProbe { return newAudioProbe(context.Background()) }
func newAudioProbe(ctx context.Context) *AudioProbe {
	p := &AudioProbe{}
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		p.err = fmt.Errorf("ffprobe unavailable: %w", err)
		return p
	}
	p.executable = path
	out, _, err := runProbeCommand(ctx, path, []string{"-version"}, nil)
	if err != nil {
		p.err = fmt.Errorf("ffprobe version: %w", err)
		return p
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			p.version = line
			break
		}
	}
	if p.version == "" {
		p.err = errors.New("ffprobe version: empty output")
	}
	return p
}

// Available reports whether ffprobe was found and its version could be read.
func (p *AudioProbe) Available() bool { return p != nil && p.err == nil && p.executable != "" }

// Error returns the availability error, if any.
func (p *AudioProbe) Error() error {
	if p == nil {
		return errors.New("nil audio probe")
	}
	return p.err
}

// ErrorDescription returns a human-readable availability error, or an empty string.
func (p *AudioProbe) ErrorDescription() string {
	if err := p.Error(); err != nil {
		return err.Error()
	}
	return ""
}

// Version returns the first line printed by ffprobe -version.
func (p *AudioProbe) Version() string {
	if p == nil {
		return ""
	}
	return p.version
}

// IsSupportedAudioExtension reports whether path has a supported audio extension.
func IsSupportedAudioExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp1", ".mp2", ".mp3", ".flac", ".wav", ".aif", ".aiff", ".ogg", ".opus", ".aac", ".m4a", ".m4b", ".wma", ".ape", ".wv", ".mpc", ".dsf", ".dff":
		return true
	default:
		return false
	}
}

// Fingerprint returns the file identity used by metadata caches.
func (p *AudioProbe) Fingerprint(path string) (AudioFingerprint, error) {
	var fp AudioFingerprint
	info, err := os.Stat(path)
	if err != nil {
		return fp, err
	}
	if info.Size() < 0 {
		return fp, errors.New("audio fingerprint: negative file size")
	}
	fp.Size = uint64(info.Size())
	fp.MTimeUnixNano = info.ModTime().UnixNano()
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		fp.CTimeUnixNano = stat.Ctim.Sec*int64(time.Second) + int64(stat.Ctim.Nsec)
	}
	fp.ExtractorVersion = p.Version()
	return fp, nil
}

// Probe extracts metadata. Errors are intended to be nonfatal to share scanning.
func (p *AudioProbe) Probe(ctx context.Context, path string) (AudioMetadata, error) {
	var metadata AudioMetadata
	if p == nil {
		return metadata, errors.New("nil audio probe")
	}
	if !p.Available() {
		return metadata, p.Error()
	}
	if !IsSupportedAudioExtension(path) {
		return metadata, fmt.Errorf("unsupported audio extension: %s", filepath.Ext(path))
	}
	if strings.IndexByte(path, 0) >= 0 || strings.Contains(path, "://") || strings.HasPrefix(strings.ToLower(path), "file:") {
		return metadata, errors.New("audio probe: non-local path")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	file, err := os.Open(path)
	if err != nil {
		return metadata, fmt.Errorf("audio probe open: %w", err)
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr != nil || !info.Mode().IsRegular() {
		if statErr != nil {
			return metadata, fmt.Errorf("audio probe stat: %w", statErr)
		}
		return metadata, errors.New("audio probe: input is not a regular file")
	}

	args := []string{
		"-v", "error", "-protocol_whitelist", "file",
		"-print_format", "json",
		"-show_entries", "stream=codec_type,bit_rate,duration,sample_rate,bits_per_sample,bits_per_raw_sample:format=duration",
		"-select_streams", "a:0", "/proc/self/fd/3",
	}
	out, stderr, err := runProbeCommand(ctx, p.executable, args, file)
	if err != nil {
		return metadata, fmt.Errorf("audio probe: %w%s", err, formatProbeStderr(stderr))
	}
	metadata, err = parseAudioProbe(out)
	if err != nil {
		return AudioMetadata{}, fmt.Errorf("audio probe output: %w", err)
	}
	return metadata, nil
}

type boundedBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.over {
		return 0, errors.New("output limit exceeded")
	}
	remaining := b.max - b.buf.Len()
	if len(p) > remaining {
		if remaining > 0 {
			_, _ = b.buf.Write(p[:remaining])
		}
		b.over = true
		return remaining, errors.New("output limit exceeded")
	}
	return b.buf.Write(p)
}

func runProbeCommand(ctx context.Context, executable string, args []string, file *os.File) ([]byte, []byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, audioProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executable, args...)
	cmd.Stdin = nil
	if file != nil {
		cmd.ExtraFiles = []*os.File{file}
	}
	var stdout, stderr boundedBuffer
	stdout.max, stderr.max = maxAudioProbeOutput, maxAudioProbeStderr
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.buf.Bytes(), stderr.buf.Bytes(), ctx.Err()
	}
	if runCtx.Err() != nil {
		return stdout.buf.Bytes(), stderr.buf.Bytes(), runCtx.Err()
	}
	if stdout.over || stderr.over {
		return stdout.buf.Bytes(), stderr.buf.Bytes(), errors.New("ffprobe output exceeds limit")
	}
	if err != nil {
		return stdout.buf.Bytes(), stderr.buf.Bytes(), err
	}
	return stdout.buf.Bytes(), stderr.buf.Bytes(), nil
}

func formatProbeStderr(stderr []byte) string {
	if len(stderr) == 0 {
		return ""
	}
	const maxError = 512
	if len(stderr) > maxError {
		stderr = stderr[:maxError]
	}
	return ": " + strings.TrimSpace(string(stderr))
}

type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

type ffprobeStream struct {
	CodecType        string          `json:"codec_type"`
	Bitrate          json.RawMessage `json:"bit_rate"`
	Duration         json.RawMessage `json:"duration"`
	SampleRate       json.RawMessage `json:"sample_rate"`
	BitsPerSample    json.RawMessage `json:"bits_per_sample"`
	BitsPerRawSample json.RawMessage `json:"bits_per_raw_sample"`
}

type ffprobeFormat struct {
	Duration json.RawMessage `json:"duration"`
}

func parseAudioProbe(data []byte) (AudioMetadata, error) {
	var parsed ffprobeOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		return AudioMetadata{}, err
	}
	var stream *ffprobeStream
	for i := range parsed.Streams {
		if strings.EqualFold(parsed.Streams[i].CodecType, "audio") {
			stream = &parsed.Streams[i]
			break
		}
	}
	if stream == nil {
		return AudioMetadata{}, errors.New("no audio stream")
	}
	var result AudioMetadata
	result.Bitrate = bitrateKbps(stream.Bitrate)
	result.Duration = durationSeconds(stream.Duration)
	if result.Duration == 0 {
		result.Duration = durationSeconds(parsed.Format.Duration)
	}
	result.SampleRate = uintField(stream.SampleRate, math.MaxUint32)
	bits := uintField(stream.BitsPerSample, 64)
	if bits == 0 {
		bits = uintField(stream.BitsPerRawSample, 64)
	}
	if bits > 0 {
		result.BitDepth = bits
	}
	return result, nil
}

func rawNumber(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	if raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return "", false
		}
		return strings.TrimSpace(value), true
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "true" || value == "false" {
		return "", false
	}
	return value, true
}

func uintField(raw json.RawMessage, max uint64) uint32 {
	value, ok := rawNumber(raw)
	if !ok {
		return 0
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n == 0 || n > max || n > math.MaxUint32 {
		return 0
	}
	return uint32(n)
}

func bitrateKbps(raw json.RawMessage) uint32 {
	value, ok := rawNumber(raw)
	if !ok {
		return 0
	}
	bps, err := strconv.ParseUint(value, 10, 64)
	if err != nil || bps == 0 {
		return 0
	}
	kbps := (bps + 500) / 1000
	if kbps > math.MaxUint32 {
		return 0
	}
	return uint32(kbps)
}

func durationSeconds(raw json.RawMessage) uint32 {
	value, ok := rawNumber(raw)
	if !ok {
		return 0
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds >= float64(math.MaxUint32)+1 {
		return 0
	}
	return uint32(seconds)
}
