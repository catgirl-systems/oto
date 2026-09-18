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
	failIfFmt(t, strings.Join(got, "\x00") != strings.Join(want, "\x00"), "defaults = %#v", got)
	got[0] = "changed"
	failIf(t, DefaultShareExclusions()[0] != want[0], "defaults are not independent")
	if normalized, err := NormalizeShareExclusions(nil); err != nil || strings.Join(normalized, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("nil: %#v, %v", normalized, err)
	}
	empty := []string{}
	normalized, err := NormalizeShareExclusions(empty)
	failIfFmt(t, err != nil || normalized == nil || len(normalized) != 0, "empty: %#v, %v", normalized, err)
	normalized, err = NormalizeShareExclusions([]string{`dir\file/`})
	failIfFmt(t, err != nil || normalized[0] != "dir/file/", "slashes: %#v, %v", normalized, err)
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
		must(t, os.WriteFile(path, data, 0600))
		loaded, err := Load(path)
		must(t, err)
		want := DefaultShareExclusions()
		if raw == "[]" {
			want = []string{}
		} else if raw != "null" && raw != "missing" {
			want = []string{"dir/file/*"}
		}
		failIfFmt(t, loaded.ShareExclusions == nil || !slices.Equal(loaded.ShareExclusions, want), "%s: %+v", raw, loaded.ShareExclusions)
		must(t, loaded.Save(path))
		again, err := Load(path)
		failIfFmt(t, err != nil || !slices.Equal(again.ShareExclusions, want) || again.Redacted().ShareExclusions == nil, "round trip: %v", err)
	}
}
