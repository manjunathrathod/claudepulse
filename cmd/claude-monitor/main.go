// Command claude-monitor indexes a Claude Code home directory (~/.claude)
// into SQLite and serves a local dashboard on http://127.0.0.1:48273.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"claude-monitor/internal/claudedir"
	"claude-monitor/internal/config"
	"claude-monitor/internal/indexer"
	"claude-monitor/internal/store"
	"claude-monitor/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "claude-monitor:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))

	if info, err := os.Stat(cfg.ClaudeDir); err != nil || !info.IsDir() {
		return fmt.Errorf("claude dir %q is not a directory (use -claude-dir)", cfg.ClaudeDir)
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

	log.Info("starting", "claude_dir", cfg.ClaudeDir, "db", cfg.DBPath, "scan_interval", cfg.ScanInterval)
	srv := web.New(st, ix, log)
	srv.SetInfo(web.Info{ClaudeDir: cfg.ClaudeDir, DBPath: cfg.DBPath, ScanInterval: cfg.ScanInterval, StartedAt: time.Now()})
	err = srv.ListenAndServe(ctx, cfg.Addr)
	stop()    // a listen failure must also stop the indexer
	wg.Wait() // let an in-flight transaction roll back before the DB closes
	return err
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
