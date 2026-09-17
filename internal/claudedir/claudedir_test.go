package claudedir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) Dir {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "testdata", "claude-home"))
	if err != nil {
		t.Fatal(err)
	}
	return New(abs)
}

// scratchFixture copies the fixture into a temp dir for tests that write.
func scratchFixture(t *testing.T) Dir {
	t.Helper()
	src := fixture(t).Root
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(dst)
}

func TestDenyList(t *testing.T) {
	d := fixture(t)
	denied := []string{
		filepath.Join(d.Root, ".credentials.json"),
		filepath.Join(d.Root, "sessions", "11111.deadbeef.key"),
		filepath.Join(d.Root, "..", "outside.json"),
	}
	for _, p := range denied {
		if !d.IsDenied(p) {
			t.Errorf("%s should be denied", p)
		}
		if _, err := d.ReadFile(p); !errors.Is(err, ErrDenied) {
			t.Errorf("ReadFile(%s) err = %v, want ErrDenied", p, err)
		}
		if _, err := d.Open(p); !errors.Is(err, ErrDenied) {
			t.Errorf("Open(%s) err = %v, want ErrDenied", p, err)
		}
	}
	allowed := []string{
		d.SettingsPath(),
		filepath.Join(d.Root, "sessions", "11111.json"),
		filepath.Join(d.Root, "projects", "C--fixture-alpha", "memory", "MEMORY.md"),
	}
	for _, p := range allowed {
		if d.IsDenied(p) {
			t.Errorf("%s should be allowed", p)
		}
	}
}

func TestListProjectsAndSubagents(t *testing.T) {
	d := fixture(t)
	ps, err := d.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].EncodedName != "C--fixture-alpha" || ps[1].EncodedName != "C--fixture-beta" {
		t.Fatalf("projects = %+v", ps)
	}
	if len(ps[0].Transcripts) != 1 || ps[0].MemoryDir == "" {
		t.Errorf("alpha = %+v", ps[0])
	}
	if SessionID(ps[0].Transcripts[0]) != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Errorf("SessionID = %q", SessionID(ps[0].Transcripts[0]))
	}
	subs, err := d.ListSubagents(ps[0].Transcripts[0])
	if err != nil || len(subs) != 1 || subs[0].AgentID != "deadbeef" {
		t.Fatalf("subagents = %+v err=%v", subs, err)
	}
	if subs, _ := d.ListSubagents(ps[1].Transcripts[0]); len(subs) != 0 {
		t.Errorf("beta should have no subagents, got %+v", subs)
	}
}

func TestSimpleDecoders(t *testing.T) {
	d := fixture(t)

	sc, err := d.ReadStatsCache()
	if err != nil || sc == nil || sc.Version != 5 || sc.ModelUsage["claude-opus-5"].OutputTokens != 510 || sc.HourCounts["10"] != 2 {
		t.Errorf("stats cache: %+v err=%v", sc, err)
	}
	lu, err := d.ReadLastUpdate()
	if err != nil || lu == nil || lu.VersionTo != "2.1.274" || lu.ErrorCode != nil {
		t.Errorf("last update: %+v err=%v", lu, err)
	}
	if lc, err := d.ReadLastCleanup(); err != nil || lc != "2026-09-01T09:30:00.000Z" {
		t.Errorf("last cleanup: %q err=%v", lc, err)
	}
	live, err := d.ReadLiveSessions()
	if err != nil || len(live) != 1 || live[0].PID != 11111 || live[0].Name != "alpha-1" {
		t.Errorf("live sessions: %+v err=%v", live, err)
	}
	hist, malformed, err := d.ReadHistory(5)
	if err != nil || len(hist) != 3 || malformed != 1 {
		t.Fatalf("history: n=%d malformed=%d err=%v", len(hist), malformed, err)
	}
	if hist[0].Project != `C:\fixture\alpha` || hist[0].Display != "first…" || hist[0].Time().Year() != 2026 {
		t.Errorf("history[0] = %+v", hist[0])
	}
	plans, err := d.ListPlans()
	if err != nil || len(plans) != 1 || plans[0].Title != "Fixture Plan Title" || plans[0].Slug != "test-plan" {
		t.Errorf("plans: %+v err=%v", plans, err)
	}
	skills, err := d.ListSkills()
	if err != nil || len(skills) != 1 || skills[0].Name != "skill-one" || skills[0].Origin != "synced" ||
		skills[0].Description != "A synced fixture skill" {
		t.Errorf("skills: %+v err=%v", skills, err)
	}
	mk, err := d.ReadMarketplaces()
	if err != nil || len(mk) != 1 || mk[0].SourceRef != "anthropics/claude-plugins-official" {
		t.Errorf("marketplaces: %+v err=%v", mk, err)
	}
	stats, err := d.TopLevelStats()
	if err != nil || len(stats) == 0 {
		t.Fatalf("stats: %+v err=%v", stats, err)
	}
	var sawProjects bool
	for _, s := range stats {
		if s.Name == "projects" && s.Files == 6 {
			sawProjects = true
		}
	}
	if !sawProjects {
		t.Errorf("projects dir stat missing or wrong: %+v", stats)
	}
}

func TestMissingDirIsNotAnError(t *testing.T) {
	d := New(filepath.Join(t.TempDir(), "nope"))
	if ps, err := d.ListProjects(); err != nil || ps != nil {
		t.Errorf("ListProjects on missing dir: %v %v", ps, err)
	}
	if sc, err := d.ReadStatsCache(); err != nil || sc != nil {
		t.Errorf("ReadStatsCache on missing dir: %v %v", sc, err)
	}
	if live, err := d.ReadLiveSessions(); err != nil || live != nil {
		t.Errorf("ReadLiveSessions on missing dir: %v %v", live, err)
	}
}
