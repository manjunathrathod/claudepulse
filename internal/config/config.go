// Package config resolves runtime configuration from flags, environment
// variables and defaults (flag > env > default).
package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// DefaultPort is deliberately obscure: unassigned by IANA and not used by any
// common developer tool (3000/5173/8000/8080/8888/9090 are all avoided).
const DefaultPort = "48273"

// Config is the fully resolved runtime configuration.
type Config struct {
	Addr         string        // listen address, loopback only
	ClaudeDir    string        // directory to monitor (~/.claude)
	DBPath       string        // SQLite file
	ScanInterval time.Duration // periodic full-rescan cadence
	LogLevel     string        // debug|info|warn|error
	ResetDB      bool          // delete the DB file before starting
}

// Load parses args (without the program name) on top of CM_* environment
// variables and built-in defaults.
func Load(args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	home, _ := os.UserHomeDir()
	def := Config{
		Addr:         "127.0.0.1:" + DefaultPort,
		ClaudeDir:    filepath.Join(home, ".claude"),
		DBPath:       filepath.Join("data", "claude-monitor.db"),
		ScanInterval: 30 * time.Second,
		LogLevel:     "info",
	}
	if v := getenv("CM_ADDR"); v != "" {
		def.Addr = v
	}
	if v := getenv("CM_CLAUDE_DIR"); v != "" {
		def.ClaudeDir = v
	}
	if v := getenv("CM_DB"); v != "" {
		def.DBPath = v
	}
	if v := getenv("CM_SCAN_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("CM_SCAN_INTERVAL: %w", err)
		}
		def.ScanInterval = d
	}
	if v := getenv("CM_LOG_LEVEL"); v != "" {
		def.LogLevel = v
	}

	fs := flag.NewFlagSet("claude-monitor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := def
	fs.StringVar(&cfg.Addr, "addr", def.Addr, "listen address (loopback only)")
	fs.StringVar(&cfg.ClaudeDir, "claude-dir", def.ClaudeDir, "Claude Code home directory to monitor")
	fs.StringVar(&cfg.DBPath, "db", def.DBPath, "SQLite database file")
	fs.DurationVar(&cfg.ScanInterval, "scan-interval", def.ScanInterval, "periodic full-rescan interval")
	fs.StringVar(&cfg.LogLevel, "log-level", def.LogLevel, "log level: debug, info, warn, error")
	fs.BoolVar(&cfg.ResetDB, "reset-db", false, "delete the database file and re-index from scratch")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.ScanInterval < time.Second {
		return Config{}, fmt.Errorf("scan-interval must be at least 1s, got %s", cfg.ScanInterval)
	}
	return cfg, nil
}
