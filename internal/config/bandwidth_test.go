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
				failIfFmt(t, (err != nil) != test.invalid, "decode = %v", err)
				failIfFmt(t, !test.invalid && !reflect.DeepEqual(c.Bandwidth, test.want), "got %+v", c.Bandwidth)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"soulseek":{"username":"u","password":"p"},"uploads":{"profiles":[{"name":"Limited","speed_limit_kib":17}],"active_profile":"Limited","limit_scope":"per_transfer","scheduling":"random"}}`
	must(t, os.WriteFile(path, []byte(legacy), 0600))
	c, err := Load(path)
	must(t, err)
	raw, _ := os.ReadFile(path)
	failIf(t, string(raw) != legacy, "Load rewrote legacy file")
	failIf(t, c.Uploads.LimitScope != UploadLimitPerTransfer || c.Uploads.Scheduling != UploadSchedulingRandom, "migration changed upload behavior")
	must(t, c.Save(path))
	raw, _ = os.ReadFile(path)
	var saved map[string]json.RawMessage
	_ = json.Unmarshal(raw, &saved)
	failIf(t, strings.Contains(string(saved["uploads"]), "profile") || len(saved["bandwidth"]) == 0, "save did not emit canonical-only profiles")
	got, err := Load(path)
	failIfFmt(t, err != nil || !reflect.DeepEqual(got.Bandwidth, c.Bandwidth), "round trip: %+v %v", got, err)
	safe := c.Redacted()
	safe.Bandwidth.Profiles[0].Name = "mutated"
	failIf(t, c.Bandwidth.Profiles[0].Name != "Limited", "redacted profile slice aliases config")
}

func TestBandwidthValidation(t *testing.T) {
	for _, name := range []string{"", " ", " leading", "trailing ", "bad\nname", strings.Repeat("x", 65)} {
		failIfFmt(t, ValidateBandwidthProfileName(name) == nil, "accepted name %q", name)
	}
	failIf(t, ValidateBandwidthProfileName(strings.Repeat("猫", 64)) != nil, "64 Unicode characters rejected")
	b := defaultBandwidth()
	b.Profiles = append(b.Profiles, BandwidthProfile{Name: "unlimited"})
	failIf(t, validateBandwidth(b) == nil, "case-insensitive duplicate accepted")
	for _, rates := range [][2]int{{0, 0}, {0, 1000000}, {1000000, 0}, {64, 128}, {-1, 0}, {0, -1}, {1000001, 0}, {0, 1000001}} {
		c := Default()
		c.Soulseek.Username, c.Soulseek.Password = "u", "p"
		c.Bandwidth.Profiles[0].UploadSpeedLimitKiB, c.Bandwidth.Profiles[0].DownloadSpeedLimitKiB = rates[0], rates[1]
		invalid := rates[0] < 0 || rates[1] < 0 || rates[0] > 1000000 || rates[1] > 1000000
		failIfFmt(t, (c.Validate() != nil) != invalid, "rate validation %v", rates)
		data, err := json.Marshal(c)
		must(t, err)
		if (json.Unmarshal(data, &Config{}) != nil) != invalid {
			t.Fatalf("decoded rate validation %v", rates)
		}
	}
}
