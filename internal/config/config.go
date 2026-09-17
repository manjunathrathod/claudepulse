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

// DefaultPort is 3333 (chosen by the user). A busy port is a hard error, never
// a silent fallback.
const DefaultPort = "3333"

// Config is the fully resolved runtime configuration.
type Config struct {
	Addr         string        // listen address, loopback only
	ClaudeDir    string        // directory to monitor (~/.claude)
	DBPath       string        // SQLite file
	ScanInterval time.Duration // periodic full-rescan cadence
	LogLevel     string        // debug|info|warn|error
	ResetDB      bool          // delete the DB file before starting
	Version      bool          // print the version and exit
}

// DefaultClaudeDir mirrors how Claude Code locates its data directory: the
// CLAUDE_CONFIG_DIR environment variable when set, otherwise ~/.claude in the
// current user's home (%USERPROFILE% on Windows, $HOME on Linux/macOS).
func DefaultClaudeDir(getenv func(string) string) string {
	if v := getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return filepath.Clean(v)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

// Load parses args (without the program name) on top of CP_* environment
// variables and built-in defaults.
func Load(args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	def := Config{
		Addr:         "127.0.0.1:" + DefaultPort,
		ClaudeDir:    DefaultClaudeDir(getenv),
		DBPath:       filepath.Join("data", "claudepulse.db"),
		ScanInterval: 30 * time.Second,
		LogLevel:     "info",
	}
	if v := getenv("CP_ADDR"); v != "" {
		def.Addr = v
	}
	if v := getenv("CP_CLAUDE_DIR"); v != "" {
		def.ClaudeDir = v
	}
	if v := getenv("CP_DB"); v != "" {
		def.DBPath = v
	}
	if v := getenv("CP_SCAN_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("CP_SCAN_INTERVAL: %w", err)
		}
		def.ScanInterval = d
	}
	if v := getenv("CP_LOG_LEVEL"); v != "" {
		def.LogLevel = v
	}

	fs := flag.NewFlagSet("claudepulse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := def
	fs.StringVar(&cfg.Addr, "addr", def.Addr, "listen address (loopback only)")
	fs.StringVar(&cfg.ClaudeDir, "claude-dir", def.ClaudeDir, "Claude Code home directory to monitor")
	fs.StringVar(&cfg.DBPath, "db", def.DBPath, "SQLite database file")
	fs.DurationVar(&cfg.ScanInterval, "scan-interval", def.ScanInterval, "periodic full-rescan interval")
	fs.StringVar(&cfg.LogLevel, "log-level", def.LogLevel, "log level: debug, info, warn, error")
	fs.BoolVar(&cfg.ResetDB, "reset-db", false, "delete the database file and re-index from scratch")
	fs.BoolVar(&cfg.Version, "version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.ScanInterval < time.Second {
		return Config{}, fmt.Errorf("scan-interval must be at least 1s, got %s", cfg.ScanInterval)
	}
	return cfg, nil
}
