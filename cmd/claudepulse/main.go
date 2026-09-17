// Command claudepulse (ClaudePulse) indexes a Claude Code home directory (~/.claude)
// into SQLite and serves a local dashboard on http://127.0.0.1:3333.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"claudepulse/internal/claudedir"
	"claudepulse/internal/config"
	"claudepulse/internal/indexer"
	"claudepulse/internal/store"
	"claudepulse/internal/web"
)

// version is stamped at build time: go build -ldflags "-X main.version=v1.0.0".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "claudepulse:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		return err
	}
	if cfg.Version {
		fmt.Println("claudepulse", version)
		return nil
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))

	if info, err := os.Stat(cfg.ClaudeDir); err != nil || !info.IsDir() {
		return fmt.Errorf("claude dir %q is not a directory (use -claude-dir)", cfg.ClaudeDir)
	}
	if inside, err := pathInside(cfg.DBPath, cfg.ClaudeDir); err != nil {
		return err
	} else if inside {
		return fmt.Errorf("database %q must not live inside the watched directory %q (it would trigger endless rescans)", cfg.DBPath, cfg.ClaudeDir)
	}
	if cfg.ResetDB {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(cfg.DBPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("reset db: %w", err)
			}
		}
		log.Info("database reset", "path", cfg.DBPath)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db %s: %w", cfg.DBPath, err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ix := indexer.New(claudedir.New(cfg.ClaudeDir), st, log)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ix.Run(ctx, cfg.ScanInterval)
	}()

	log.Info("starting", "version", version, "claude_dir", cfg.ClaudeDir, "db", cfg.DBPath, "scan_interval", cfg.ScanInterval)
	srv := web.New(st, ix, log)
	srv.SetInfo(web.Info{ClaudeDir: cfg.ClaudeDir, DBPath: cfg.DBPath, ScanInterval: cfg.ScanInterval, StartedAt: time.Now(), Version: version})
	err = srv.ListenAndServe(ctx, cfg.Addr)
	stop()    // a listen failure must also stop the indexer
	wg.Wait() // let an in-flight transaction roll back before the DB closes
	return err
}

// pathInside reports whether p is dir or a descendant of it.
func pathInside(p, dir string) (bool, error) {
	ap, err := filepath.Abs(p)
	if err != nil {
		return false, err
	}
	ad, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(ad, ap)
	if err != nil {
		return false, nil // different volumes
	}
	return rel == "." || !strings.HasPrefix(rel, ".."), nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
