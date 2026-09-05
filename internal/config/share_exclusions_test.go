package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestShareExclusionsDefaultsAndNormalization(t *testing.T) {
	want := []string{".*", ".*/", "@eaDir/", "#recycle/", "#snapshot/", "desktop.ini", "Thumbs.db", "System Volume Information/", "$RECYCLE.BIN/", "lost+found/", "*.part", "*.partial", "*.crdownload", "*.tmp", "*.temp", "*.bak", "*~"}
	got := DefaultShareExclusions()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("defaults = %#v", got)
	}
	got[0] = "changed"
	if DefaultShareExclusions()[0] != want[0] {
		t.Fatal("defaults are not independent")
	}
	if normalized, err := NormalizeShareExclusions(nil); err != nil || strings.Join(normalized, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("nil: %#v, %v", normalized, err)
	}
	empty := []string{}
	normalized, err := NormalizeShareExclusions(empty)
	if err != nil || normalized == nil || len(normalized) != 0 {
		t.Fatalf("empty: %#v, %v", normalized, err)
	}
	normalized, err = NormalizeShareExclusions([]string{`dir\file/`})
	if err != nil || normalized[0] != "dir/file/" {
		t.Fatalf("slashes: %#v, %v", normalized, err)
	}
}

func TestNormalizeShareExclusionsRejectsInvalidAndLimits(t *testing.T) {
	for _, rule := range []string{"", " \t", "\x00", "/tmp", `C:\\tmp`, "C:/tmp", "//server/share", "foo/./bar", "foo/../bar", ".", ".."} {
		if _, err := NormalizeShareExclusions([]string{rule}); err == nil {
			t.Errorf("accepted %q", rule)
		}
	}
	if _, err := NormalizeShareExclusions(make([]string, 257)); err == nil {
		t.Error("accepted too many rules")
	}
	if _, err := NormalizeShareExclusions([]string{strings.Repeat("x", 1025)}); err == nil {
		t.Error("accepted oversized rule")
	}
}

func TestShareExclusionConfigRoundTrip(t *testing.T) {
	for _, raw := range []string{"null", "[]", `["dir\\file/*"]`, "missing"} {
		cfg := Default()
		cfg.Soulseek.Username, cfg.Soulseek.Password = "test", "password"
		data, _ := json.Marshal(cfg)
		var fields map[string]json.RawMessage
		json.Unmarshal(data, &fields)
		if raw == "missing" {
			delete(fields, "share_exclusions")
		} else {
			fields["share_exclusions"] = json.RawMessage(raw)
		}
		data, _ = json.Marshal(fields)
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		want := DefaultShareExclusions()
		if raw == "[]" {
			want = []string{}
		} else if raw != "null" && raw != "missing" {
			want = []string{"dir/file/*"}
		}
		if loaded.ShareExclusions == nil || !slices.Equal(loaded.ShareExclusions, want) {
			t.Fatalf("%s: %+v", raw, loaded.ShareExclusions)
		}
		if err := loaded.Save(path); err != nil {
			t.Fatal(err)
		}
		again, err := Load(path)
		if err != nil || !slices.Equal(again.ShareExclusions, want) || again.Redacted().ShareExclusions == nil {
			t.Fatalf("round trip: %v", err)
		}
	}
}
