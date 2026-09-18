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
		must(t, json.Unmarshal([]byte(input), &cfg))
		want := input == `{"uploads":{"wait_for_active_uploads_on_quit":true}}`
		failIf(t, cfg.Uploads.WaitForActiveUploadsOnQuit != want || cfg.Redacted().Uploads.WaitForActiveUploadsOnQuit != want, "default/redaction", input)
		path := filepath.Join(t.TempDir(), "config.json")
		must(t, cfg.Save(path))
		got, err := Load(path)
		failIfFmt(t, err != nil || got.Uploads.WaitForActiveUploadsOnQuit != want, "roundtrip: %+v %v", got.Uploads, err)
	}
}
