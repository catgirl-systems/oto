package config

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestWaitForActiveUploadsOptIn(t *testing.T) {
	for _, input := range []string{`{}`, `{"uploads":{"wait_for_active_uploads_on_quit":false}}`, `{"uploads":{"wait_for_active_uploads_on_quit":true}}`} {
		cfg := Default()
		cfg.Soulseek.Username, cfg.Soulseek.Password = "test", "test"
		if err := json.Unmarshal([]byte(input), &cfg); err != nil {
			t.Fatal(err)
		}
		want := input == `{"uploads":{"wait_for_active_uploads_on_quit":true}}`
		if cfg.Uploads.WaitForActiveUploadsOnQuit != want || cfg.Redacted().Uploads.WaitForActiveUploadsOnQuit != want {
			t.Fatal("default/redaction", input)
		}
		path := filepath.Join(t.TempDir(), "config.json")
		if err := cfg.Save(path); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || got.Uploads.WaitForActiveUploadsOnQuit != want {
			t.Fatalf("roundtrip: %+v %v", got.Uploads, err)
		}
	}
}
