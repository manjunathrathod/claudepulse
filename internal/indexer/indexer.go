// Package indexer synchronises a Claude Code home directory into the store.
// It is the only writer to the database.
package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"claudepulse/internal/claudedir"
	"claudepulse/internal/claudedir/jsonl"
	"claudepulse/internal/store"

	"github.com/fsnotify/fsnotify"
)

// Indexer performs full and incremental scans.
type Indexer struct {
	dir claudedir.Dir
	st  *store.Store
	log *slog.Logger

	scanMu  sync.Mutex // serialises scans; never held while answering Status
	statsMu sync.RWMutex
	stats   Stats

	scanning     atomic.Bool
	lastDirStats time.Time

	kick     chan struct{} // coalesced "scan soon" requests (watcher, API)
	watcher  *fsnotify.Watcher
	watching atomic.Bool
}

// Stats describes the most recent scan.
type Stats struct {
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	Files         int       `json:"files"`
	FilesChanged  int       `json:"files_changed"`
	LinesRead     int       `json:"lines_read"`
	Malformed     int       `json:"malformed_lines"`
	Pruned        int64     `json:"pruned_files"`
	Err           string    `json:"error,omitempty"`
	Duration      string    `json:"duration"`
	Scanning      bool      `json:"scanning"`
	TotalSessions int64     `json:"indexed_sessions"`
}

// New creates an indexer.
func New(dir claudedir.Dir, st *store.Store, log *slog.Logger) *Indexer {
	return &Indexer{dir: dir, st: st, log: log, kick: make(chan struct{}, 1)}
}

// Status returns a copy of the latest stats.
func (ix *Indexer) Status(ctx context.Context) Stats {
	ix.statsMu.RLock()
	s := ix.stats
	ix.statsMu.RUnlock()
	s.Scanning = ix.scanning.Load()
	if n, err := ix.st.CountSessions(ctx); err == nil {
		s.TotalSessions = n
	}
	return s
}

// Run performs a scan now, then rescans whenever the file watcher reports a
// change (debounced) and at least every interval, until ctx is done.
func (ix *Indexer) Run(ctx context.Context, interval time.Duration) {
	scan := func(reason string) {
		if err := ix.Scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
			ix.log.Error("scan failed", "reason", reason, "err", err)
		}
		if ctx.Err() == nil {
			ix.syncWatches() // never race a concurrent Close (see below)
		}
	}
	// Run is the sole owner of the watcher: it is created here, Add is only
	// ever called from this goroutine (syncWatches), the event loop only
	// reads, and Close happens here after the event loop has returned. Any
	// other arrangement can deadlock fsnotify's Windows backend.
	ix.startWatcher()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ix.watch(ctx) // drain events before the first Add so the buffer cannot fill
	}()
	ix.syncWatches() // watch before the first scan so nothing created meanwhile is missed
	scan("startup")
	t := time.NewTicker(interval)
	defer t.Stop()
	defer func() {
		<-watchDone
		if ix.watcher != nil {
			ix.watcher.Close()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			scan("ticker")
		case <-ix.kick:
			scan("watch")
		}
	}
}

// Watching reports whether file-system notifications are active.
func (ix *Indexer) Watching() bool { return ix.watching.Load() }

// Scan performs one full pass: transcripts (incrementally), single-file
// sources, live sessions, rollups. Concurrent calls are serialised.
func (ix *Indexer) Scan(ctx context.Context) (err error) {
	ix.scanMu.Lock()
	defer ix.scanMu.Unlock()
	ix.scanning.Store(true)
	defer ix.scanning.Store(false)

	st := Stats{StartedAt: time.Now()}
	defer func() {
		st.FinishedAt = time.Now()
		st.Duration = st.FinishedAt.Sub(st.StartedAt).Round(time.Millisecond).String()
		if err != nil {
			st.Err = err.Error()
		}
		ix.statsMu.Lock()
		ix.stats = st
		ix.statsMu.Unlock()
		if errors.Is(err, context.Canceled) {
			return
		}
		ix.log.Info("scan complete", "files", st.Files, "changed", st.FilesChanged, "lines", st.LinesRead,
			"malformed", st.Malformed, "pruned", st.Pruned, "took", st.Duration)
	}()

	// A listing failure (drive briefly unavailable, AV holding a directory)
	// aborts the scan before anything is pruned.
	projects, err := ix.dir.ListProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	keep := map[string]bool{ix.dir.HistoryPath(): true} // history has its own cursor; never prune it
	ids := map[string]int64{}
	realPaths := map[int64]string{}
	for _, p := range projects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pid, err := ix.st.UpsertProject(ctx, p.EncodedName, p.Path)
		if err != nil {
			return err
		}
		var realPath string
		for _, tp := range p.Transcripts {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			keep[tp] = true
			sid := claudedir.SessionID(tp)
			fs, err := ix.scanTranscript(ctx, tp, sid, pid, "")
			st.Files++
			st.LinesRead += fs.lines
			st.Malformed += fs.malformed
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				ix.log.Warn("transcript scan failed", "path", tp, "err", err)
				continue
			}
			if fs.changed {
				st.FilesChanged++
			}
			if fs.cwd != "" {
				realPath = fs.cwd
			}
			subs, err := ix.dir.ListSubagents(tp)
			if err != nil {
				ix.log.Warn("list subagents failed", "path", tp, "err", err)
			}
			for _, sa := range subs {
				keep[sa.Path] = true
				ss, err := ix.scanTranscript(ctx, sa.Path, sid, pid, sa.AgentID)
				st.Files++
				st.LinesRead += ss.lines
				st.Malformed += ss.malformed
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					ix.log.Warn("subagent scan failed", "path", sa.Path, "err", err)
					continue
				}
				if ss.changed {
					st.FilesChanged++
				}
				if err := ix.recordSubagent(ctx, sa, sid); err != nil {
					ix.log.Warn("subagent meta failed", "path", sa.MetaPath, "err", err)
				}
			}
		}
		if realPath == "" {
			// Unchanged transcripts are not re-read, so fall back to the path
			// learned on an earlier scan; otherwise the flags would reset.
			realPath, err = ix.st.ProjectRealPath(ctx, pid)
			if err != nil {
				return err
			}
		}
		if err := ix.st.UpdateProjectMeta(ctx, pid, projectMeta(p, realPath)); err != nil {
			return err
		}
		ids[p.EncodedName], realPaths[pid] = pid, realPath
	}

	// An empty listing almost always means projects/ was unreadable rather
	// than the user having no history; never prune on it.
	if len(projects) > 0 {
		pruned, err := ix.st.PruneMissing(ctx, keep)
		if err != nil {
			return fmt.Errorf("prune: %w", err)
		}
		st.Pruned = pruned
	}

	if err := ix.st.RebuildRollups(ctx); err != nil {
		return fmt.Errorf("rollups: %w", err)
	}
	if err := ix.scanMeta(ctx); err != nil {
		ix.log.Warn("meta scan failed", "err", err)
	}
	ix.scanConfig(ctx, projects, ids, realPaths)
	if err := ix.scanLiveSessions(ctx); err != nil {
		ix.log.Warn("live sessions scan failed", "err", err)
	}
	if time.Since(ix.lastDirStats) > 5*time.Minute {
		if err := ix.scanDirStats(ctx); err != nil {
			ix.log.Warn("dir stats failed", "err", err)
		} else {
			ix.lastDirStats = time.Now()
		}
	}
	return nil
}

type fileScan struct {
	changed          bool
	lines, malformed int
	cwd              string
}

// scanTranscript indexes one transcript incrementally. agentID is "" for the
// main transcript and the subagent id for agent-<id>.jsonl files.
func (ix *Indexer) scanTranscript(ctx context.Context, path, sessionID string, projectID int64, agentID string) (fileScan, error) {
	var fs fileScan
	if ix.dir.IsDenied(path) {
		return fs, claudedir.ErrDenied
	}
	info, err := os.Stat(path)
	if err != nil {
		return fs, err
	}
	prev, seen, err := ix.st.GetScanState(ctx, path)
	if err != nil {
		return fs, err
	}
	offset := int64(0)
	if seen {
		if info.Size() < prev.Size {
			// Rotated or rewritten: drop everything we derived from it.
			ix.log.Debug("file shrank; reindexing", "path", path)
			if err := ix.st.Tx(ctx, func(tx *sql.Tx) error { return store.DeleteFileRows(ctx, tx, path) }); err != nil {
				return fs, err
			}
		} else if info.Size() == prev.Size && store.FormatTime(info.ModTime()) == store.FormatTime(prev.MTime) {
			return fs, nil // unchanged (compare at the stored millisecond precision)
		} else {
			offset = prev.ByteOffset
		}
	}
	fs.changed = true

	f, err := ix.dir.Open(path)
	if err != nil {
		return fs, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return fs, err
		}
	}

	var facts store.SessionFacts
	var res jsonl.Result
	err = ix.st.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.EnsureSession(ctx, tx, sessionID, projectID, mainTranscriptPath(path, agentID)); err != nil {
			return err
		}
		var scanErr error
		res, scanErr = jsonl.Scan(f, func(ln jsonl.Line) error {
			return ix.applyLine(ctx, tx, ln, path, sessionID, agentID, &facts)
		})
		if scanErr != nil {
			return scanErr
		}
		if err := store.UpdateSessionFacts(ctx, tx, sessionID, facts); err != nil {
			return err
		}
		return store.PutScanState(ctx, tx, store.ScanState{
			Path: path, Size: info.Size(), MTime: info.ModTime().UTC(), ByteOffset: offset + res.BytesConsumed,
		})
	})
	fs.lines, fs.malformed, fs.cwd = res.Lines, res.Malformed, facts.CWD
	if err != nil {
		return fs, err
	}
	ix.log.Debug("indexed", "path", filepath.Base(path), "lines", res.Lines, "from", offset)
	return fs, nil
}

// mainTranscriptPath returns the session's main transcript path for a
// subagent file (so sessions.source_path always points at the main file).
func mainTranscriptPath(path, agentID string) string {
	if agentID == "" {
		return path
	}
	// <project>/<session>/subagents/agent-x.jsonl → <project>/<session>.jsonl
	sessionDir := filepath.Dir(filepath.Dir(path))
	return sessionDir + ".jsonl"
}

// applyLine writes one decoded line into the transaction.
func (ix *Indexer) applyLine(ctx context.Context, tx *sql.Tx, ln jsonl.Line, path, sessionID, agentID string, facts *store.SessionFacts) error {
	switch ln.Type {
	case "ai-title":
		if agentID == "" && ln.AITitle != "" {
			facts.Title = ln.AITitle
		}
		return nil
	case "summary":
		if agentID == "" && ln.Summary != "" && facts.Title == "" {
			facts.Title = ln.Summary
		}
		return nil
	case "user", "assistant":
	default:
		return nil
	}
	if ln.Message == nil {
		return nil
	}
	if agentID == "" {
		if ln.CWD != "" {
			facts.CWD = ln.CWD
		}
		if ln.Version != "" {
			facts.CLIVersion = ln.Version
		}
		if ln.Entrypoint != "" {
			facts.Entrypoint = ln.Entrypoint
		}
	}
	if ln.AgentID != "" {
		agentID = ln.AgentID
	}

	m := store.MessageRow{
		SessionID: sessionID, AgentID: agentID, Role: ln.Type, TS: ln.Timestamp.Time,
		IsSidechain: ln.IsSidechain, SourcePath: path,
	}
	switch ln.Type {
	case "user":
		m.ID = ln.UUID
		m.Kind = ln.Message.Content.Kind()
	case "assistant":
		m.ID = ln.Message.ID
		m.Kind = "response"
		m.Model = ln.Message.Model
		if u := ln.Message.Usage; u != nil {
			m.Input, m.Output = u.InputTokens, u.OutputTokens
			m.CacheRead, m.CacheCreate = u.CacheReadInputTokens, u.CacheCreationInputTokens
			m.Thinking = u.OutputTokensDetails.ThinkingTokens
		}
	}
	if m.ID == "" {
		return nil // cannot dedupe without an id; skip rather than double count
	}
	if err := store.InsertMessage(ctx, tx, m); err != nil {
		return err
	}
	for _, tu := range ln.Message.Content.ToolUses() {
		if tu.ID == "" {
			continue
		}
		if err := store.InsertToolCall(ctx, tx, store.ToolCallRow{
			ID: tu.ID, SessionID: sessionID, MessageID: m.ID, AgentID: agentID, Name: tu.Name, TS: m.TS, SourcePath: path,
		}); err != nil {
			return err
		}
	}
	return nil
}

// recordSubagent reads agent-<id>.meta.json and upserts the subagent row.
func (ix *Indexer) recordSubagent(ctx context.Context, sa claudedir.Subagent, sessionID string) error {
	row := store.SubagentRow{ID: sa.AgentID, SessionID: sessionID, SourcePath: sa.Path}
	if b, err := ix.dir.ReadFile(sa.MetaPath); err == nil {
		var meta struct {
			AgentType   string `json:"agentType"`
			Description string `json:"description"`
			SpawnDepth  int    `json:"spawnDepth"`
		}
		if json.Unmarshal(b, &meta) == nil {
			row.AgentType, row.Description, row.SpawnDepth, row.HasMeta = meta.AgentType, meta.Description, meta.SpawnDepth, true
		}
	}
	return ix.st.Tx(ctx, func(tx *sql.Tx) error { return store.UpsertSubagent(ctx, tx, row) })
}

// projectMeta inspects the project's real directory (if known and present).
func projectMeta(p claudedir.Project, realPath string) store.ProjectMeta {
	m := store.ProjectMeta{RealPath: realPath}
	if p.MemoryDir != "" {
		if entries, err := os.ReadDir(p.MemoryDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
					m.MemoryFileCount++
				}
			}
		}
	}
	if realPath == "" {
		return m
	}
	if info, err := os.Stat(realPath); err == nil && info.IsDir() {
		m.ExistsOnDisk = true
		m.HasClaudeMD = fileExists(filepath.Join(realPath, "CLAUDE.md"))
		m.HasProjectSettings = fileExists(filepath.Join(realPath, ".claude", "settings.json")) ||
			fileExists(filepath.Join(realPath, ".claude", "settings.local.json"))
	}
	return m
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// scanMeta refreshes single-file sources into the meta table.
func (ix *Indexer) scanMeta(ctx context.Context) error {
	if sc, err := ix.dir.ReadStatsCache(); err != nil {
		ix.log.Warn("stats-cache", "err", err)
	} else if sc != nil {
		b, _ := json.Marshal(sc)
		if err := ix.st.SetMeta(ctx, "stats_cache", string(b)); err != nil {
			return err
		}
	}
	if lu, err := ix.dir.ReadLastUpdate(); err != nil {
		ix.log.Warn("last-update", "err", err)
	} else if lu != nil {
		b, _ := json.Marshal(lu)
		if err := ix.st.SetMeta(ctx, "last_update", string(b)); err != nil {
			return err
		}
		if lu.VersionTo != "" {
			if err := ix.st.SetMeta(ctx, "cli_version", lu.VersionTo); err != nil {
				return err
			}
		}
	}
	if lc, err := ix.dir.ReadLastCleanup(); err == nil && lc != "" {
		if err := ix.st.SetMeta(ctx, "last_cleanup", lc); err != nil {
			return err
		}
	}
	if b, err := ix.dir.ReadFile(ix.dir.SettingsPath()); err == nil {
		if red, ok := RedactSettings(b); ok {
			if err := ix.st.SetMeta(ctx, "user_settings", string(red)); err != nil {
				return err
			}
		}
	}
	if acct, err := ix.dir.ReadAccount(); err != nil {
		ix.log.Warn("account file", "err", err)
	} else if acct != nil {
		b, _ := json.Marshal(acct)
		if err := ix.st.SetMeta(ctx, "account", string(b)); err != nil {
			return err
		}
	} else if err := ix.st.DeleteMeta(ctx, "account"); err != nil {
		return err
	}
	return ix.st.SetMeta(ctx, "claude_dir", ix.dir.Root)
}

// RedactSettings strips secret-bearing values from a settings.json document
// before it is stored, at any nesting depth: every value under `env`, every
// string under a key that looks credential-like (key/token/secret/password/
// credential/auth/helper), and every `command` string (hooks, statusLine and
// helpers routinely inline tokens). Structure is preserved so the UI can still
// show which keys are set. Returns ok=false for invalid JSON.
func RedactSettings(raw []byte) ([]byte, bool) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false
	}
	out, err := json.Marshal(redactNode(doc, false))
	if err != nil {
		return nil, false
	}
	return out, true
}

const redactedMarker = "[redacted]"

var sensitiveKeyParts = []string{"key", "token", "secret", "password", "credential", "auth", "helper", "env", "command"}

func sensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, part := range sensitiveKeyParts {
		if strings.Contains(lk, part) {
			return true
		}
	}
	return false
}

// redactNode walks the document; once a sensitive key is entered every string
// beneath it is replaced.
func redactNode(v any, sensitive bool) any {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			x[k] = redactNode(child, sensitive || sensitiveKey(k))
		}
		return x
	case []any:
		for i, child := range x {
			x[i] = redactNode(child, sensitive)
		}
		return x
	case string:
		if sensitive {
			return redactedMarker
		}
		return x
	default:
		return x
	}
}

func (ix *Indexer) scanLiveSessions(ctx context.Context) error {
	live, err := ix.dir.ReadLiveSessions()
	if err != nil {
		return err
	}
	rows := make([]store.LiveSessionRow, 0, len(live))
	for _, l := range live {
		rows = append(rows, store.LiveSessionRow{
			PID: l.PID, SessionID: l.SessionID, CWD: l.CWD, Version: l.Version, Name: l.Name,
			StartedAt: time.UnixMilli(l.StartedAt).UTC(),
		})
	}
	return ix.st.ReplaceLiveSessions(ctx, rows)
}

func (ix *Indexer) scanDirStats(ctx context.Context) error {
	stats, err := ix.dir.TopLevelStats()
	if err != nil {
		return err
	}
	rows := make([]store.DirStatRow, 0, len(stats))
	for _, s := range stats {
		rows = append(rows, store.DirStatRow{Name: s.Name, Files: s.Files, Bytes: s.Bytes})
	}
	return ix.st.ReplaceDirStats(ctx, rows)
}
