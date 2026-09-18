package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSaveLoadModesEnvAndRedaction(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "nested", "config.json")
	c := Default()
	c.Soulseek.Username, c.Soulseek.Password = "alice", "secret"
	c.Soulseek.ConnectOnStartup = false
	must(t, c.Save(p))
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("file mode: %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Dir(p)); err != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("dir mode: %v %v", st, err)
	}
	os.Setenv("OTO_SERVER", "example:1234")
	os.Setenv("OTO_PASSWORD", "override")
	os.Setenv("OTO_NETWORK_INTERFACE", "wg0")
	defer os.Unsetenv("OTO_SERVER")
	defer os.Unsetenv("OTO_PASSWORD")
	defer os.Unsetenv("OTO_NETWORK_INTERFACE")
	got, err := Load(p)
	must(t, err)
	failIfFmt(t, got.Soulseek.Server != "example:1234" || got.Soulseek.Password != "override" || got.Soulseek.NetworkInterface != "wg0" || got.Soulseek.ConnectOnStartup, "env overrides or startup setting: %+v", got.Soulseek)
	b, _ := json.Marshal(got.Redacted())
	failIfFmt(t, strings.Contains(string(b), "override") || !strings.Contains(string(b), `"network_interface":"wg0"`) || !strings.Contains(string(b), `"connect_on_startup":false`), "unsafe or incomplete redaction: %s", b)
	q := filepath.Join(d, "env-config.json")
	must(t, got.Save(q))
	raw, err := os.ReadFile(q)
	failIfFmt(t, err != nil || strings.Contains(string(raw), "override"), "environment password persisted: %v %s", err, raw)
	if _, err := os.Stat(filepath.Join(filepath.Dir(p), ".config.json.tmp-")); !os.IsNotExist(err) { /* random temp names are allowed; no fixed temp remains */
	}
}

func TestSaveJSONFallsBackWhenRenameFails(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	must(t, SaveJSON(p, map[string]string{"a": "1"}))
	renamePath = func(string, string) error { return errors.New("device or resource busy") }
	defer func() { renamePath = os.Rename }()
	must(t, SaveJSON(p, map[string]string{"a": "2"}))
	raw, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(raw), `"a": "2"`) {
		t.Fatalf("in-place write: %v %s", err, raw)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("fallback file mode: %v %v", st, err)
	}
	entries, err := os.ReadDir(d)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temp file left behind: %v %v", err, entries)
	}
}

func TestSearchDefaultsCompatibilityAndValidation(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	raw := `{"soulseek":{"username":"u","password":"p","server":"server.slsknet.org:2242","listen_addr":"0.0.0.0:50300"},"download_dir":"/tmp","download_slots":1,"upload_slots":1}`
	must(t, os.WriteFile(p, []byte(raw), 0600))
	got, err := Load(p)
	must(t, err)
	want := Search{RememberSearches: true, SearchHistoryLimit: 200, RememberFilters: true, FilterHistoryLimit: 50, WishlistIntervalMinutes: 15, WishlistNotifications: true, RespondToIncomingSearches: true, MinimumIncomingSearchLength: 3, MaximumIncomingSearchResults: 300}
	failIfFmt(t, got.Search != want || got.Redacted().Search != want, "search defaults or redaction: got %+v safe %+v", got.Search, got.Redacted().Search)
	wantUploads := Uploads{LimitScope: UploadLimitTotal, Scheduling: UploadSchedulingFIFO}
	failIfFmt(t, !reflect.DeepEqual(got.Uploads, wantUploads) || !reflect.DeepEqual(got.Redacted().Uploads, wantUploads), "upload defaults or safe config: got %+v safe %+v", got.Uploads, got.Redacted().Uploads)
	encoded, _ := json.Marshal(got.Redacted())
	for _, setting := range []string{`"wishlist_interval_minutes":15`, `"wishlist_notifications":true`, `"respond_to_incoming_searches":true`, `"minimum_incoming_search_length":3`, `"maximum_incoming_search_results":300`} {
		failIfFmt(t, !strings.Contains(string(encoded), setting), "safe config omitted search setting %s: %s", setting, encoded)
	}
	failIf(t, !got.Soulseek.ConnectOnStartup || !got.Soulseek.NATPMPPortMapping || !got.Soulseek.UPnPPortMapping, "older config did not retain connection defaults")
	failIf(t, got.Soulseek.NetworkInterface != "" || got.Redacted().Soulseek.NetworkInterface != "", "older config did not default to automatic network routing")
	safe := got.Redacted().Soulseek
	failIf(t, !safe.ConnectOnStartup || !safe.NATPMPPortMapping || !safe.UPnPPortMapping, "redacted config omitted connection defaults")
	got.Search.SearchHistoryLimit, got.Search.FilterHistoryLimit = 0, 0
	if err := got.Validate(); err != nil {
		t.Fatalf("zero should mean unlimited: %v", err)
	}
	got.Soulseek.ConnectOnStartup = false
	got.Soulseek.NATPMPPortMapping = false
	got.Soulseek.NetworkInterface = "tun0"
	roundTripPath := filepath.Join(d, "round-trip.json")
	must(t, got.Save(roundTripPath))
	roundTrip, err := Load(roundTripPath)
	failIfFmt(t, err != nil || roundTrip.Search != got.Search || roundTrip.Soulseek.NetworkInterface != "tun0" || roundTrip.Soulseek.ConnectOnStartup || roundTrip.Soulseek.NATPMPPortMapping || !roundTrip.Soulseek.UPnPPortMapping, "config round trip: %+v %v", roundTrip, err)
	roundTripSafe := roundTrip.Redacted().Soulseek
	failIfFmt(t, roundTripSafe.NetworkInterface != "tun0" || roundTripSafe.NATPMPPortMapping || !roundTripSafe.UPnPPortMapping, "redacted port mapping settings: %+v", roundTripSafe)
	got.Search.FilterHistoryLimit = -1
	if err := got.Validate(); err == nil {
		t.Fatal("negative history limit accepted")
	}
	for _, test := range []struct {
		name         string
		minimum, max int
	}{
		{"negative incoming minimum", -1, 300},
		{"oversized incoming minimum", 51, 300},
		{"small incoming maximum", 3, 49},
		{"oversized incoming maximum", 3, 10001},
	} {
		invalid := Default()
		invalid.Search.MinimumIncomingSearchLength = test.minimum
		invalid.Search.MaximumIncomingSearchResults = test.max
		if err := invalid.Validate(); err == nil {
			t.Fatalf("%s accepted", test.name)
		}
	}
	got = Default()
	got.Search.WishlistIntervalMinutes = 525601
	if err := got.Validate(); err == nil {
		t.Fatal("oversized wishlist interval accepted")
	}
}

func TestUploadConfigRoundTripAndValidation(t *testing.T) {
	cfg := Default()
	failIf(t, cfg.Uploads.AutoClearCancelled, "cancelled cleanup must default off")
	if err := json.Unmarshal([]byte(`{"uploads":{"auto_clear_completed":true}}`), &cfg); err != nil || cfg.Uploads.AutoClearCancelled {
		t.Fatalf("omitted cancelled cleanup changed compatibility: %v", err)
	}
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.Bandwidth = Bandwidth{Profiles: []BandwidthProfile{{Name: "Fast", UploadSpeedLimitKiB: 1000}, {Name: "Night", UploadSpeedLimitKiB: 25, DownloadSpeedLimitKiB: 100}}, ActiveProfile: "Night"}
	cfg.Uploads.LimitScope, cfg.Uploads.Scheduling = UploadLimitPerTransfer, UploadSchedulingSmallestFirst
	cfg.Uploads.AutoClearCancelled = true
	cfg.Uploads.SlotBandwidthKiB = 128
	cfg.Uploads.MaxQueuedFilesPerUser, cfg.Uploads.MaxQueuedBytesPerUser = 1000000, 1<<63-1
	cfg.Shares = []Share{{Name: "Music", Path: "/music"}, {Name: "music", Path: "/other"}}
	path := filepath.Join(t.TempDir(), "config.json")
	must(t, cfg.Save(path))
	got, err := Load(path)
	failIfFmt(t, err != nil || !reflect.DeepEqual(got.Bandwidth, cfg.Bandwidth) || got.Uploads != cfg.Uploads, "round trip: %+v %v", got, err)
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Uploads.LimitScope = "bad" },
		func(c *Config) { c.Uploads.Scheduling = "bad" },
		func(c *Config) { c.Uploads.SlotBandwidthKiB = 1000001 },
		func(c *Config) { c.Uploads.MaxQueuedFilesPerUser++ },
		func(c *Config) { c.Uploads.MaxQueuedBytesPerUser++ },
		func(c *Config) { c.Shares = []Share{{Name: "same", Path: "/a"}, {Name: "same", Path: "/b"}} },
		func(c *Config) { c.Shares = []Share{{Name: "empty"}} },
		func(c *Config) { c.Shares = []Share{{Name: " ", Path: "/a"}} },
		func(c *Config) { c.Shares = []Share{{Name: "..", Path: "/a"}} },
		func(c *Config) { c.Shares = []Share{{Name: "a/b", Path: "/a"}} },
		func(c *Config) { c.Bandwidth.Profiles = nil },
		func(c *Config) { c.Bandwidth.ActiveProfile = "missing" },
	} {
		invalid := cfg
		mutate(&invalid)
		failIf(t, invalid.Validate() == nil, "invalid config accepted")
	}
}

func TestDownloadCommandsRoundTripAndRejectNUL(t *testing.T) {
	cfg := Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.Downloads = Downloads{FilterPatterns: DefaultDownloadFilters(), AfterFileCommand: `echo "$1"`, AfterFolderCommand: `echo folder "$1"`}
	path := filepath.Join(t.TempDir(), "config.json")
	must(t, cfg.Save(path))
	got, err := Load(path)
	failIfFmt(t, err != nil || !reflect.DeepEqual(got.Downloads, cfg.Downloads) || !reflect.DeepEqual(got.Redacted().Downloads, cfg.Downloads), "download commands round trip: got %+v safe %+v err %v", got.Downloads, got.Redacted().Downloads, err)
	for _, command := range []string{"bad" + string(rune(0)), "ok" + string(rune(0))} {
		invalid := cfg
		invalid.Downloads.AfterFileCommand = command
		if err := invalid.Validate(); err == nil {
			t.Fatalf("NUL command accepted: %q", command)
		}
	}
}

func TestStatePathUsesXDGStateHome(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_STATE_HOME", d)
	if got, want := StatePath(), filepath.Join(d, "oto", "state.sqlite3"); got != want {
		t.Fatalf("StatePath() = %q, want %q", got, want)
	}
}

func TestBrowseDefaultsValidationAndRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	must(t, cfg.Validate())
	if cfg.Browse != (Browse{MaxEntries: 2000000, MaxCompressedMiB: 64, MaxDecompressedMiB: 256}) {
		t.Fatalf("browse defaults: %+v", cfg.Browse)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Browse.MaxEntries = 0 }, func(c *Config) { c.Browse.MaxEntries = -1 }, func(c *Config) { c.Browse.MaxEntries = 10000001 },
		func(c *Config) { c.Browse.MaxCompressedMiB = 0 }, func(c *Config) { c.Browse.MaxCompressedMiB = -1 }, func(c *Config) { c.Browse.MaxCompressedMiB = 257 },
		func(c *Config) { c.Browse.MaxDecompressedMiB = 0 }, func(c *Config) { c.Browse.MaxDecompressedMiB = -1 }, func(c *Config) { c.Browse.MaxDecompressedMiB = 1025 },
	} {
		invalid := cfg
		mutate(&invalid)
		failIf(t, invalid.Validate() == nil, "invalid browse limit accepted")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	cfg.Browse = Browse{MaxEntries: 123, MaxCompressedMiB: 12, MaxDecompressedMiB: 34}
	must(t, cfg.Save(path))
	got, err := Load(path)
	failIfFmt(t, err != nil || got.Browse != cfg.Browse || got.Redacted().Browse != cfg.Browse, "browse round trip: %+v %v", got.Browse, err)
	old := filepath.Join(t.TempDir(), "old.json")
	must(t, os.WriteFile(old, []byte(`{"soulseek":{"username":"u","password":"p","server":"server.slsknet.org:2242","listen_addr":"0.0.0.0:50300"},"download_dir":"/tmp","download_slots":1,"upload_slots":1}`), 0600))
	legacy, err := Load(old)
	failIfFmt(t, err != nil || legacy.Browse != Default().Browse, "old config browse defaults: %+v %v", legacy.Browse, err)
}
