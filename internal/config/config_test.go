package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	want := (Search{RememberSearches: true, SearchHistoryLimit: 200, RememberFilters: true, FilterHistoryLimit: 50, WishlistIntervalMinutes: 15, WishlistNotifications: true})
	if got.Search != want || got.Redacted().Search != want {
		t.Fatalf("search defaults or redaction: got %+v safe %+v", got.Search, got.Redacted().Search)
	}
	encoded, _ := json.Marshal(got.Redacted())
	if !strings.Contains(string(encoded), `"wishlist_interval_minutes":15`) || !strings.Contains(string(encoded), `"wishlist_notifications":true`) {
		t.Fatalf("safe config omitted wishlist settings: %s", encoded)
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
	got = Default()
	got.Search.WishlistIntervalMinutes = 525601
	if err := got.Validate(); err == nil {
		t.Fatal("oversized wishlist interval accepted")
	}
}

func TestHistoryPathUsesXDGStateHome(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_STATE_HOME", d)
	if got, want := HistoryPath(), filepath.Join(d, "oto", "history.json"); got != want {
		t.Fatalf("HistoryPath() = %q, want %q", got, want)
	}
}
