package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBandwidthMigration(t *testing.T) {
	canonical := `"bandwidth":{"active_profile":"Both","profiles":[{"name":"Both","upload_speed_limit_kib":64,"download_speed_limit_kib":128}]}`
	for _, test := range []struct {
		name, body string
		want       Bandwidth
		invalid    bool
	}{
		{"defaults", `{}`, defaultBandwidth(), false},
		{"legacy", `{"uploads":{"profiles":[{"name":"Fast","speed_limit_kib":1000},{"name":"Slow","speed_limit_kib":25}],"active_profile":"Slow"}}`, Bandwidth{Profiles: []BandwidthProfile{{Name: "Fast", UploadSpeedLimitKiB: 1000}, {Name: "Slow", UploadSpeedLimitKiB: 25}}, ActiveProfile: "Slow"}, false},
		{"canonical", `{` + canonical + `}`, Bandwidth{Profiles: []BandwidthProfile{{Name: "Both", UploadSpeedLimitKiB: 64, DownloadSpeedLimitKiB: 128}}, ActiveProfile: "Both"}, false},
		{"precedence", `{` + canonical + `,"uploads":{"profiles":null,"active_profile":5}}`, Bandwidth{Profiles: []BandwidthProfile{{Name: "Both", UploadSpeedLimitKiB: 64, DownloadSpeedLimitKiB: 128}}, ActiveProfile: "Both"}, false},
		{"null canonical", `{"bandwidth":null}`, Bandwidth{}, true},
		{"empty canonical", `{"bandwidth":{}}`, Bandwidth{}, true},
		{"empty list", `{"bandwidth":{"active_profile":"x","profiles":[]}}`, Bandwidth{}, true},
		{"null list", `{"bandwidth":{"active_profile":"x","profiles":null}}`, Bandwidth{}, true},
		{"bad list", `{"bandwidth":{"active_profile":"x","profiles":42}}`, Bandwidth{}, true},
		{"legacy null", `{"uploads":{"profiles":null}}`, Bandwidth{}, true},
		{"legacy empty", `{"uploads":{"profiles":[]}}`, Bandwidth{}, true},
		{"legacy bad", `{"uploads":{"profiles":42}}`, Bandwidth{}, true},
		{"legacy null active", `{"uploads":{"active_profile":null}}`, Bandwidth{}, true},
		{"legacy missing active", `{"uploads":{"profiles":[{"name":"x"}]}}`, Bandwidth{}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, c := range []Config{{}, Default()} {
				err := json.Unmarshal([]byte(test.body), &c)
				if (err != nil) != test.invalid {
					t.Fatalf("decode = %v", err)
				}
				if !test.invalid && !reflect.DeepEqual(c.Bandwidth, test.want) {
					t.Fatalf("got %+v", c.Bandwidth)
				}
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"soulseek":{"username":"u","password":"p"},"uploads":{"profiles":[{"name":"Limited","speed_limit_kib":17}],"active_profile":"Limited","limit_scope":"per_transfer","scheduling":"random"}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != legacy {
		t.Fatal("Load rewrote legacy file")
	}
	if c.Uploads.LimitScope != UploadLimitPerTransfer || c.Uploads.Scheduling != UploadSchedulingRandom {
		t.Fatal("migration changed upload behavior")
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	var saved map[string]json.RawMessage
	_ = json.Unmarshal(raw, &saved)
	if strings.Contains(string(saved["uploads"]), "profile") || len(saved["bandwidth"]) == 0 {
		t.Fatal("save did not emit canonical-only profiles")
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got.Bandwidth, c.Bandwidth) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	safe := c.Redacted()
	safe.Bandwidth.Profiles[0].Name = "mutated"
	if c.Bandwidth.Profiles[0].Name != "Limited" {
		t.Fatal("redacted profile slice aliases config")
	}
}

func TestBandwidthValidation(t *testing.T) {
	for _, name := range []string{"", " ", " leading", "trailing ", "bad\nname", strings.Repeat("x", 65)} {
		if ValidateBandwidthProfileName(name) == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
	if ValidateBandwidthProfileName(strings.Repeat("猫", 64)) != nil {
		t.Fatal("64 Unicode characters rejected")
	}
	b := defaultBandwidth()
	b.Profiles = append(b.Profiles, BandwidthProfile{Name: "unlimited"})
	if validateBandwidth(b) == nil {
		t.Fatal("case-insensitive duplicate accepted")
	}
	for _, rates := range [][2]int{{0, 0}, {0, 1000000}, {1000000, 0}, {64, 128}, {-1, 0}, {0, -1}, {1000001, 0}, {0, 1000001}} {
		c := Default()
		c.Soulseek.Username, c.Soulseek.Password = "u", "p"
		c.Bandwidth.Profiles[0].UploadSpeedLimitKiB, c.Bandwidth.Profiles[0].DownloadSpeedLimitKiB = rates[0], rates[1]
		invalid := rates[0] < 0 || rates[1] < 0 || rates[0] > 1000000 || rates[1] > 1000000
		if (c.Validate() != nil) != invalid {
			t.Fatalf("rate validation %v", rates)
		}
	}
}
