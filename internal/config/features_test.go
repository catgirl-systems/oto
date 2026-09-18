package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFeatureDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	must(t, os.WriteFile(path, []byte(`{"soulseek":{"username":"u","password":"p"}}`), 0600))
	cfg, err := Load(path)
	must(t, err)
	failIfFmt(t, cfg.Search.DefaultFilter != "" || cfg.Downloads.FileNotifications || !cfg.Downloads.FolderNotifications || cfg.Downloads.AutoClearFiltered, "legacy defaults: %+v %+v", cfg.Search, cfg.Downloads)
	cfg.Search.DefaultFilter = `type:audio,!mp3 size:>20MiB`
	cfg.Downloads.FileNotifications, cfg.Downloads.FolderNotifications, cfg.Downloads.AutoClearFiltered = true, false, true
	must(t, cfg.Save(path))
	loaded, err := Load(path)
	must(t, err)
	failIf(t, loaded.Search.DefaultFilter != cfg.Search.DefaultFilter || !reflect.DeepEqual(loaded.Downloads, cfg.Downloads), "feature settings did not persist")
	safe := loaded.Redacted()
	failIf(t, safe.Search.DefaultFilter != cfg.Search.DefaultFilter || !reflect.DeepEqual(safe.Downloads, cfg.Downloads), "feature settings missing from safe config")
}
