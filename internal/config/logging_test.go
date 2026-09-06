package config

import (
	"path/filepath"
	"testing"
)

func TestLoggingDefaultsNormalizationAndValidation(t *testing.T) {
	if Default().Logging.Level != "INFO" {
		t.Fatal("default log level is not INFO")
	}
	for _, test := range []struct{ input, want string }{{"", "INFO"}, {" info ", "INFO"}, {"debug", "DEBUG"}, {"Warn", "WARN"}, {"ERROR", "ERROR"}} {
		got, err := NormalizeLogLevel(test.input)
		if err != nil || got != test.want {
			t.Fatalf("NormalizeLogLevel(%q) = %q, %v", test.input, got, err)
		}
	}
	for _, level := range []string{"trace", "NOTICE"} {
		if _, err := NormalizeLogLevel(level); err == nil {
			t.Fatalf("unsupported level %q accepted", level)
		}
	}
	cfg := Default()
	cfg.Soulseek.Username, cfg.Soulseek.Password = "u", "p"
	cfg.Logging.Level = "debug"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Logging.Level != "debug" {
		t.Fatal("Validate mutated logging level")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Logging.Level != "DEBUG" || loaded.Redacted().Logging.Level != "DEBUG" {
		t.Fatalf("logging round trip: %+v, %v", loaded.Logging, err)
	}
}
