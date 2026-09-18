package config

import (
	"path/filepath"
	"testing"
)

func TestLoggingDefaultsNormalizationAndValidation(t *testing.T) {
	failIf(t, Default().Logging.Level != "INFO", "default log level is not INFO")
	for _, test := range []struct{ input, want string }{{"", "INFO"}, {" info ", "INFO"}, {"debug", "DEBUG"}, {"Warn", "WARN"}, {"ERROR", "ERROR"}} {
		got, err := NormalizeLogLevel(test.input)
		failIfFmt(t, err != nil || got != test.want, "NormalizeLogLevel(%q) = %q, %v", test.input, got, err)
	}
	for _, level := range []string{"trace", "NOTICE"} {
		if _, err := NormalizeLogLevel(level); err == nil {
			t.Fatalf("unsupported level %q accepted", level)
		}
	}
	cfg := Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.Logging.Level = "debug"
	must(t, cfg.Validate())
	failIf(t, cfg.Logging.Level != "debug", "Validate mutated logging level")
	path := filepath.Join(t.TempDir(), "config.json")
	must(t, cfg.Save(path))
	loaded, err := Load(path)
	failIfFmt(t, err != nil || loaded.Logging.Level != "DEBUG" || loaded.Redacted().Logging.Level != "DEBUG", "logging round trip: %+v, %v", loaded.Logging, err)
}
