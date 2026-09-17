package indexer

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-monitor/internal/claudedir"
	"claude-monitor/internal/store"
)

const (
	alphaSession = "aaaaaaaa-0000-4000-8000-000000000001"
	betaSession  = "bbbbbbbb-0000-4000-8000-000000000002"
)

// copyFixture copies testdata/claude-home into a temp dir so tests can mutate it.
func copyFixture(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "claude-home"))
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func newIndexer(t *testing.T, root string) (*Indexer, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(claudedir.New(root), st, log), st
}

func queryInt(t *testing.T, st *store.Store, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := st.DB().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func queryStr(t *testing.T, st *store.Store, q string, args ...any) string {
	t.Helper()
	var s string
	if err := st.DB().QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}

// TestGoldenNumbers pins the exact figures derived from the fixture. If this
// fails after a parser change, the dedupe or counting rules changed — check
// .claude/skills/claude-dir-format before "fixing" the numbers.
func TestGoldenNumbers(t *testing.T) {
	root := copyFixture(t)
	ix, st := newIndexer(t, root)
	ctx := context.Background()
	if err := ix.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	stats := ix.Status(ctx)
	if stats.Files != 3 || stats.Malformed != 1 || stats.Err != "" {
		t.Errorf("scan stats = %+v", stats)
	}

	tot, err := st.GetTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := store.Totals{
		Projects: 2, Sessions: 2, UserPrompts: 3, AssistantMsgs: 5, ToolCalls: 4, Subagents: 1,
		InputTokens: 25, OutputTokens: 270, CacheRead: 7200, CacheCreate: 420,
		FirstActivity: "2026-09-01T10:00:00.000Z", LastActivity: "2026-09-02T22:30:04.000Z",
	}
	if tot != want {
		t.Errorf("totals\n got %+v\nwant %+v", tot, want)
	}

	// Dedupe: msg_A appears on 3 lines, msg_C on 2 → still one row each.
	if n := queryInt(t, st, `SELECT COUNT(*) FROM messages WHERE id IN ('msg_A','msg_C')`); n != 2 {
		t.Errorf("deduped assistant rows = %d, want 2", n)
	}
	if n := queryInt(t, st, `SELECT thinking_tokens FROM messages WHERE id = 'msg_A'`); n != 40 {
		t.Errorf("thinking tokens = %d, want 40", n)
	}
	// Tool calls are collected across the duplicate lines.
	if n := queryInt(t, st, `SELECT COUNT(*) FROM tool_calls WHERE message_id = 'msg_C'`); n != 2 {
		t.Errorf("tool calls on msg_C = %d, want 2", n)
	}

	// Session facts.
	if s := queryStr(t, st, `SELECT title FROM sessions WHERE id = ?`, alphaSession); s != "Alpha fixture session" {
		t.Errorf("alpha title = %q", s)
	}
	if s := queryStr(t, st, `SELECT title FROM sessions WHERE id = ?`, betaSession); s != "Beta fixture session" {
		t.Errorf("beta title (from summary line) = %q", s)
	}
	if s := queryStr(t, st, `SELECT cwd || '|' || cli_version || '|' || entrypoint FROM sessions WHERE id = ?`, alphaSession); s != `C:\fixture\alpha|2.1.274|cli` {
		t.Errorf("alpha facts = %q", s)
	}
	if n := queryInt(t, st, `SELECT user_msg_count*1000 + assistant_msg_count*100 + tool_call_count*10 + subagent_count FROM sessions WHERE id = ?`, alphaSession); n != 2441 {
		t.Errorf("alpha counts packed = %d, want 2441 (2 prompts, 4 assistant, 4 tools, 1 subagent)", n)
	}
	if n := queryInt(t, st, `SELECT duration_ms FROM session_usage WHERE session_id = ?`, alphaSession); n != 601000 {
		t.Errorf("alpha duration = %d ms, want 601000 (msg_C keeps its first line's timestamp)", n)
	}

	// Subagent.
	if s := queryStr(t, st, `SELECT agent_type || '|' || msg_count FROM subagents WHERE id = 'deadbeef'`); s != "reviewer|2" {
		t.Errorf("subagent = %q", s)
	}
	if n := queryInt(t, st, `SELECT COUNT(*) FROM messages WHERE agent_id = 'deadbeef' AND is_sidechain = 1`); n != 2 {
		t.Errorf("sidechain messages = %d, want 2", n)
	}

	// Projects.
	if s := queryStr(t, st, `SELECT real_path || '|' || exists_on_disk || '|' || memory_file_count FROM projects WHERE encoded_name = 'C--fixture-alpha'`); s != `C:\fixture\alpha|0|2` {
		t.Errorf("alpha project = %q", s)
	}

	// Rollups.
	daily, err := st.GetDailyUsage(ctx, 0)
	if err != nil || len(daily) != 2 {
		t.Fatalf("daily = %+v err=%v", daily, err)
	}
	if d := daily[0]; d.Date != "2026-09-01" || d.Model != "claude-opus-5" || d.Messages != 4 || d.ToolCalls != 4 || d.OutputTokens != 200 {
		t.Errorf("daily[0] = %+v", d)
	}
	if d := daily[1]; d.Date != "2026-09-02" || d.Model != "claude-sonnet-5" || d.Messages != 1 || d.ToolCalls != 0 || d.OutputTokens != 70 {
		t.Errorf("daily[1] = %+v", d)
	}
	hours, _ := st.GetHourCounts(ctx)
	if hours[10] != 2 || hours[22] != 1 || hours[0] != 0 {
		t.Errorf("hours = %v", hours)
	}
	tools, _ := st.GetToolUsage(ctx, 0)
	if len(tools) != 4 || tools[0].Count != 1 {
		t.Errorf("tools = %+v", tools)
	}
	models, _ := st.GetModelUsage(ctx)
	if len(models) != 2 || models[0].Model != "claude-opus-5" || models[0].Messages != 4 || models[0].OutputTokens != 200 {
		t.Errorf("models = %+v", models)
	}

	// Meta + live.
	if v, _ := st.GetMeta(ctx, "cli_version"); v != "2.1.274" {
		t.Errorf("cli_version = %q", v)
	}
	if v, _ := st.GetMeta(ctx, "stats_cache"); !strings.Contains(v, `"totalSessions":1`) {
		t.Errorf("stats_cache meta = %q", v)
	}
	live, _ := st.GetLiveSessions(ctx)
	if len(live) != 1 || live[0].PID != 11111 || live[0].StartedAt != "2026-09-01T10:00:00.000Z" {
		t.Errorf("live = %+v", live)
	}

	// Secrets must never reach the database.
	for _, table := range []string{"meta", "messages", "sessions", "projects", "live_sessions"} {
		rows, err := st.DB().Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			_ = rows.Scan(ptrs...)
			for _, v := range vals {
				if s, ok := v.(string); ok && strings.Contains(s, "MUST-NEVER-BE-READ") {
					t.Errorf("secret leaked into table %s", table)
				}
			}
		}
		rows.Close()
	}
}

func TestIncrementalAppendAndShrink(t *testing.T) {
	root := copyFixture(t)
	ix, st := newIndexer(t, root)
	ctx := context.Background()
	if err := ix.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "projects", "C--fixture-beta", betaSession+".jsonl")

	// Second scan with nothing changed touches no files.
	if err := ix.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if s := ix.Status(ctx); s.FilesChanged != 0 {
		t.Errorf("unchanged rescan touched %d files", s.FilesChanged)
	}

	// Append a partial line (no newline) — must not be consumed yet.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	partial := `{"parentUuid":"a21","isSidechain":false,"type":"user","message":{"role":"user","content":"more"},"uuid":"u22","timestamp":"2026-09-02T22:40:00.000Z","sessionId":"` + betaSession + `"}`
	if _, err := f.WriteString(partial); err != nil {
		t.Fatal(err)
	}
	f.Close()
	bumpMTime(t, path)
	if err := ix.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, st, `SELECT COUNT(*) FROM messages WHERE id = 'u22'`); n != 0 {
		t.Errorf("partial line was indexed (n=%d)", n)
	}
	before := queryInt(t, st, `SELECT byte_offset FROM scan_state WHERE path = ?`, path)

	// Complete the line: only the tail is read and the new row appears.
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\n")
	f.Close()
	bumpMTime(t, path)
	if err := ix.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, st, `SELECT COUNT(*) FROM messages WHERE id = 'u22'`); n != 1 {
		t.Errorf("appended line not indexed (n=%d)", n)
	}
	after := queryInt(t, st, `SELECT byte_offset FROM scan_state WHERE path = ?`, path)
	if after != before+int64(len(partial))+1 {
		t.Errorf("offset advanced %d → %d, want +%d", before, after, len(partial)+1)
	}
	if n := queryInt(t, st, `SELECT user_msg_count FROM sessions WHERE id = ?`, betaSession); n != 2 {
		t.Errorf("beta prompt count after append = %d, want 2", n)
	}
	// Still no duplicates of the original rows.
	if n := queryInt(t, st, `SELECT COUNT(*) FROM messages WHERE session_id = ?`, betaSession); n != 3 {
		t.Errorf("beta messages = %d, want 3", n)
	}

	// Shrink (rewrite with only the first two lines) → rows reset.
	orig, _ := os.ReadFile(path)
	lines := strings.SplitAfter(string(orig), "\n")
	os.WriteFile(path, []byte(strings.Join(lines[:2], "")), 0o644)
	bumpMTime(t, path)
	if err := ix.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, st, `SELECT COUNT(*) FROM messages WHERE session_id = ?`, betaSession); n != 1 {
		t.Errorf("after shrink beta messages = %d, want 1", n)
	}

	// Delete the file → session pruned.
	os.Remove(path)
	if err := ix.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, st, `SELECT COUNT(*) FROM sessions WHERE id = ?`, betaSession); n != 0 {
		t.Errorf("deleted transcript's session still present")
	}
	if s := ix.Status(ctx); s.Pruned != 1 {
		t.Errorf("pruned = %d, want 1", s.Pruned)
	}
}

func TestProjectMetaDetectsRealDir(t *testing.T) {
	real := t.TempDir()
	os.WriteFile(filepath.Join(real, "CLAUDE.md"), []byte("# x"), 0o644)
	os.MkdirAll(filepath.Join(real, ".claude"), 0o755)
	os.WriteFile(filepath.Join(real, ".claude", "settings.local.json"), []byte("{}"), 0o644)
	m := projectMeta(claudedir.Project{}, real)
	if !m.ExistsOnDisk || !m.HasClaudeMD || !m.HasProjectSettings {
		t.Errorf("meta = %+v", m)
	}
	if m := projectMeta(claudedir.Project{}, filepath.Join(real, "missing")); m.ExistsOnDisk {
		t.Errorf("missing dir reported as existing")
	}
}

// TestProjectFlagsSurviveUnchangedRescan guards against flags being reset when
// no transcript was re-read (the real path is only learned from re-read files).
func TestProjectFlagsSurviveUnchangedRescan(t *testing.T) {
	root := copyFixture(t)
	real := t.TempDir()
	os.WriteFile(filepath.Join(real, "CLAUDE.md"), []byte("# x"), 0o644)
	// Point the alpha transcript's cwd at a directory that really exists.
	path := filepath.Join(root, "projects", "C--fixture-alpha", alphaSession+".jsonl")
	b, _ := os.ReadFile(path)
	escaped := strings.ReplaceAll(real, `\`, `\\`)
	os.WriteFile(path, []byte(strings.ReplaceAll(string(b), `C:\\fixture\\alpha`, escaped)), 0o644)

	ix, st := newIndexer(t, root)
	ctx := context.Background()
	q := `SELECT real_path || '|' || exists_on_disk || '|' || has_claude_md FROM projects WHERE encoded_name = 'C--fixture-alpha'`
	want := real + "|1|1"
	for i := 0; i < 2; i++ {
		if err := ix.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		if got := queryStr(t, st, q); got != want {
			t.Errorf("scan #%d: project = %q, want %q", i+1, got, want)
		}
	}
}

// TestStatusDoesNotBlockDuringScan: /healthz must answer while a scan runs.
func TestStatusDoesNotBlockDuringScan(t *testing.T) {
	root := copyFixture(t)
	ix, _ := newIndexer(t, root)
	ix.scanMu.Lock() // simulate a long scan holding the scan lock
	defer ix.scanMu.Unlock()
	done := make(chan struct{})
	go func() {
		ix.Status(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Status() blocked while a scan holds the scan lock")
	}
}

func TestRedactSettings(t *testing.T) {
	in := []byte(`{"model":"opus","env":{"ANTHROPIC_API_KEY":"sk-live","DEBUG":"1"},"apiKeyHelper":"/bin/helper","permissions":{"allow":["Read"]}}`)
	out, ok := RedactSettings(in)
	if !ok {
		t.Fatal("valid JSON rejected")
	}
	s := string(out)
	for _, leak := range []string{"sk-live", "/bin/helper", `"DEBUG":"1"`} {
		if strings.Contains(s, leak) {
			t.Errorf("leaked %q in %s", leak, s)
		}
	}
	for _, keep := range []string{`"model":"opus"`, `"ANTHROPIC_API_KEY":"[redacted]"`, `"apiKeyHelper":"[redacted]"`, `"allow":["Read"]`} {
		if !strings.Contains(s, keep) {
			t.Errorf("missing %q in %s", keep, s)
		}
	}
	if _, ok := RedactSettings([]byte("{nope")); ok {
		t.Error("invalid JSON accepted")
	}
}

// bumpMTime makes sure the mtime differs from the previous scan even on
// filesystems with coarse timestamps.
func bumpMTime(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}
