package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFeatureDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"soulseek":{"username":"u","password":"p"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Search.DefaultFilter != "" || cfg.Downloads.FileNotifications || !cfg.Downloads.FolderNotifications {
		t.Fatalf("legacy defaults: %+v %+v", cfg.Search, cfg.Downloads)
	}
	cfg.Search.DefaultFilter = `type:audio,!mp3 size:>20MiB`
	cfg.Downloads.FileNotifications, cfg.Downloads.FolderNotifications = true, false
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Search.DefaultFilter != cfg.Search.DefaultFilter || !reflect.DeepEqual(loaded.Downloads, cfg.Downloads) {
		t.Fatal("feature settings did not persist")
	}
	safe := loaded.Redacted()
	if safe.Search.DefaultFilter != cfg.Search.DefaultFilter || !reflect.DeepEqual(safe.Downloads, cfg.Downloads) {
		t.Fatal("feature settings missing from safe config")
	}
}
