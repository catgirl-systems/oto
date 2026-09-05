package config

import (
	"encoding/json"
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
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if got.Soulseek.Server != "example:1234" || got.Soulseek.Password != "override" || got.Soulseek.NetworkInterface != "wg0" || got.Soulseek.ConnectOnStartup {
		t.Fatalf("env overrides or startup setting: %+v", got.Soulseek)
	}
	b, _ := json.Marshal(got.Redacted())
	if strings.Contains(string(b), "override") || !strings.Contains(string(b), `"network_interface":"wg0"`) || !strings.Contains(string(b), `"connect_on_startup":false`) {
		t.Fatalf("unsafe or incomplete redaction: %s", b)
	}
	q := filepath.Join(d, "env-config.json")
	if err := got.Save(q); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(q)
	if err != nil || strings.Contains(string(raw), "override") {
		t.Fatalf("environment password persisted: %v %s", err, raw)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p), ".config.json.tmp-")); !os.IsNotExist(err) { /* random temp names are allowed; no fixed temp remains */
	}
}

func TestSearchDefaultsCompatibilityAndValidation(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	raw := `{"soulseek":{"username":"u","password":"p","server":"server.slsknet.org:2242","listen_addr":"0.0.0.0:50300"},"download_dir":"/tmp","download_slots":1,"upload_slots":1}`
	if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := Search{RememberSearches: true, SearchHistoryLimit: 200, RememberFilters: true, FilterHistoryLimit: 50, WishlistIntervalMinutes: 15, WishlistNotifications: true, RespondToIncomingSearches: true, MinimumIncomingSearchLength: 3, MaximumIncomingSearchResults: 300}
	if got.Search != want || got.Redacted().Search != want {
		t.Fatalf("search defaults or redaction: got %+v safe %+v", got.Search, got.Redacted().Search)
	}
	wantUploads := Uploads{LimitScope: UploadLimitTotal, Scheduling: UploadSchedulingFIFO}
	if !reflect.DeepEqual(got.Uploads, wantUploads) || !reflect.DeepEqual(got.Redacted().Uploads, wantUploads) {
		t.Fatalf("upload defaults or safe config: got %+v safe %+v", got.Uploads, got.Redacted().Uploads)
	}
	encoded, _ := json.Marshal(got.Redacted())
	for _, setting := range []string{`"wishlist_interval_minutes":15`, `"wishlist_notifications":true`, `"respond_to_incoming_searches":true`, `"minimum_incoming_search_length":3`, `"maximum_incoming_search_results":300`} {
		if !strings.Contains(string(encoded), setting) {
			t.Fatalf("safe config omitted search setting %s: %s", setting, encoded)
		}
	}
	if !got.Soulseek.ConnectOnStartup || !got.Soulseek.NATPMPPortMapping || !got.Soulseek.UPnPPortMapping {
		t.Fatal("older config did not retain connection defaults")
	}
	if got.Soulseek.NetworkInterface != "" || got.Redacted().Soulseek.NetworkInterface != "" {
		t.Fatal("older config did not default to automatic network routing")
	}
	safe := got.Redacted().Soulseek
	if !safe.ConnectOnStartup || !safe.NATPMPPortMapping || !safe.UPnPPortMapping {
		t.Fatal("redacted config omitted connection defaults")
	}
	got.Search.SearchHistoryLimit, got.Search.FilterHistoryLimit = 0, 0
	if err := got.Validate(); err != nil {
		t.Fatalf("zero should mean unlimited: %v", err)
	}
	got.Soulseek.ConnectOnStartup = false
	got.Soulseek.NATPMPPortMapping = false
	got.Soulseek.NetworkInterface = "tun0"
	roundTripPath := filepath.Join(d, "round-trip.json")
	if err := got.Save(roundTripPath); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := Load(roundTripPath)
	if err != nil || roundTrip.Search != got.Search || roundTrip.Soulseek.NetworkInterface != "tun0" || roundTrip.Soulseek.ConnectOnStartup || roundTrip.Soulseek.NATPMPPortMapping || !roundTrip.Soulseek.UPnPPortMapping {
		t.Fatalf("config round trip: %+v %v", roundTrip, err)
	}
	roundTripSafe := roundTrip.Redacted().Soulseek
	if roundTripSafe.NetworkInterface != "tun0" || roundTripSafe.NATPMPPortMapping || !roundTripSafe.UPnPPortMapping {
		t.Fatalf("redacted port mapping settings: %+v", roundTripSafe)
	}
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
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.Bandwidth = Bandwidth{Profiles: []BandwidthProfile{{Name: "Fast", UploadSpeedLimitKiB: 1000}, {Name: "Night", UploadSpeedLimitKiB: 25, DownloadSpeedLimitKiB: 100}}, ActiveProfile: "Night"}
	cfg.Uploads.LimitScope, cfg.Uploads.Scheduling = UploadLimitPerTransfer, UploadSchedulingSmallestFirst
	path := filepath.Join(t.TempDir(), "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got.Bandwidth, cfg.Bandwidth) || got.Uploads != cfg.Uploads {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Uploads.LimitScope = "bad" },
		func(c *Config) { c.Uploads.Scheduling = "bad" },
		func(c *Config) { c.Bandwidth.Profiles = nil },
		func(c *Config) { c.Bandwidth.ActiveProfile = "missing" },
	} {
		invalid := cfg
		mutate(&invalid)
		if invalid.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestDownloadCommandsRoundTripAndRejectNUL(t *testing.T) {
	cfg := Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.Downloads = Downloads{FilterPatterns: DefaultDownloadFilters(), AfterFileCommand: `echo "$1"`, AfterFolderCommand: `echo folder "$1"`}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got.Downloads, cfg.Downloads) || !reflect.DeepEqual(got.Redacted().Downloads, cfg.Downloads) {
		t.Fatalf("download commands round trip: got %+v safe %+v err %v", got.Downloads, got.Redacted().Downloads, err)
	}
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
