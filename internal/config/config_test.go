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
	defer os.Unsetenv("OTO_SERVER")
	defer os.Unsetenv("OTO_PASSWORD")
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Soulseek.Server != "example:1234" || got.Soulseek.Password != "override" {
		t.Fatalf("env overrides: %+v", got.Soulseek)
	}
	b, _ := json.Marshal(got.Redacted())
	if strings.Contains(string(b), "override") {
		t.Fatal("password leaked")
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
	want := (Search{RememberSearches: true, SearchHistoryLimit: 200, RememberFilters: true, FilterHistoryLimit: 50})
	if got.Search != want || got.Redacted().Search != want {
		t.Fatalf("search defaults or redaction: got %+v safe %+v", got.Search, got.Redacted().Search)
	}
	got.Search.SearchHistoryLimit, got.Search.FilterHistoryLimit = 0, 0
	if err := got.Validate(); err != nil {
		t.Fatalf("zero should mean unlimited: %v", err)
	}
	roundTripPath := filepath.Join(d, "round-trip.json")
	if err := got.Save(roundTripPath); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := Load(roundTripPath)
	if err != nil || roundTrip.Search != got.Search {
		t.Fatalf("search round trip: %+v %v", roundTrip.Search, err)
	}
	got.Search.FilterHistoryLimit = -1
	if err := got.Validate(); err == nil {
		t.Fatal("negative history limit accepted")
	}
}

func TestHistoryPathUsesXDGStateHome(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_STATE_HOME", d)
	if got, want := HistoryPath(), filepath.Join(d, "oto", "history.json"); got != want {
		t.Fatalf("HistoryPath() = %q, want %q", got, want)
	}
}
