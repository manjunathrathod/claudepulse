package config

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaultClaudeDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := DefaultClaudeDir(env(nil)); got != filepath.Join(home, ".claude") {
		t.Errorf("default = %q", got)
	}
	if got := DefaultClaudeDir(env(map[string]string{"CLAUDE_CONFIG_DIR": "/srv/cc/"})); got != filepath.Clean("/srv/cc/") {
		t.Errorf("CLAUDE_CONFIG_DIR not honoured: %q", got)
	}
}

func TestLoadPrecedence(t *testing.T) {
	e := env(map[string]string{"CP_ADDR": "127.0.0.1:1", "CP_DB": "env.db", "CLAUDE_CONFIG_DIR": "/cc"})
	cfg, err := Load([]string{"-db", "flag.db"}, e, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:1" || cfg.DBPath != "flag.db" || cfg.ClaudeDir != filepath.Clean("/cc") {
		t.Errorf("cfg = %+v", cfg)
	}
	if _, err := Load([]string{"-scan-interval", "10ms"}, env(nil), io.Discard); err == nil {
		t.Error("sub-second scan interval accepted")
	}
	if _, err := Load(nil, env(map[string]string{"CP_SCAN_INTERVAL": "nope"}), io.Discard); err == nil {
		t.Error("bad CP_SCAN_INTERVAL accepted")
	}
}
