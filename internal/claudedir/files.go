package claudedir

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StatsCache mirrors ~/.claude/stats-cache.json (version 5).
type StatsCache struct {
	Version          int                   `json:"version"`
	LastComputedDate string                `json:"lastComputedDate"`
	DailyActivity    []StatsDailyActivity  `json:"dailyActivity"`
	DailyModelTokens []StatsDailyTokens    `json:"dailyModelTokens"`
	ModelUsage       map[string]StatsModel `json:"modelUsage"`
	TotalSessions    int                   `json:"totalSessions"`
	TotalMessages    int                   `json:"totalMessages"`
	LongestSession   *StatsLongestSession  `json:"longestSession"`
	FirstSessionDate string                `json:"firstSessionDate"`
	HourCounts       map[string]int        `json:"hourCounts"`
}

type StatsDailyActivity struct {
	Date          string `json:"date"`
	MessageCount  int    `json:"messageCount"`
	SessionCount  int    `json:"sessionCount"`
	ToolCallCount int    `json:"toolCallCount"`
}

type StatsDailyTokens struct {
	Date          string           `json:"date"`
	TokensByModel map[string]int64 `json:"tokensByModel"`
}

type StatsModel struct {
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	WebSearchRequests        int64   `json:"webSearchRequests"`
	CostUSD                  float64 `json:"costUSD"`
}

type StatsLongestSession struct {
	SessionID    string `json:"sessionId"`
	DurationMS   int64  `json:"duration"`
	MessageCount int    `json:"messageCount"`
	Timestamp    string `json:"timestamp"`
}

// ReadStatsCache decodes stats-cache.json. A missing file yields (nil, nil).
func (d Dir) ReadStatsCache() (*StatsCache, error) {
	var sc StatsCache
	ok, err := d.readJSON(d.StatsCachePath(), &sc)
	if err != nil || !ok {
		return nil, err
	}
	return &sc, nil
}

// LastUpdate mirrors .last-update-result.json.
type LastUpdate struct {
	Timestamp   string  `json:"timestamp"`
	Path        string  `json:"path"`
	Outcome     string  `json:"outcome"`
	Status      string  `json:"status"`
	VersionFrom string  `json:"version_from"`
	VersionTo   string  `json:"version_to"`
	ErrorCode   *string `json:"error_code"`
}

// ReadLastUpdate decodes .last-update-result.json. Missing → (nil, nil).
func (d Dir) ReadLastUpdate() (*LastUpdate, error) {
	var lu LastUpdate
	ok, err := d.readJSON(d.LastUpdatePath(), &lu)
	if err != nil || !ok {
		return nil, err
	}
	return &lu, nil
}

// ReadLastCleanup returns the ISO timestamp in .last-cleanup, or "" if absent.
func (d Dir) ReadLastCleanup() (string, error) {
	b, err := d.ReadFile(d.LastCleanupPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// LiveSession mirrors sessions/<pid>.json — a Claude Code process that is (or
// was recently) running. Stale files are possible after a crash.
type LiveSession struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	StartedAt  int64  `json:"startedAt"` // ms epoch
	Version    string `json:"version"`
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
	Name       string `json:"name"`
	Path       string `json:"-"`
}

// ReadLiveSessions decodes every sessions/<pid>.json. Key files are skipped.
func (d Dir) ReadLiveSessions() ([]LiveSession, error) {
	entries, err := os.ReadDir(d.SessionsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []LiveSession
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(d.SessionsDir(), e.Name())
		var ls LiveSession
		if ok, err := d.readJSON(p, &ls); err != nil || !ok {
			continue // malformed or vanished mid-scan; not fatal
		}
		ls.Path = p
		out = append(out, ls)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out, nil
}

// HistoryEntry is one line of history.jsonl.
type HistoryEntry struct {
	Display   string `json:"display"`
	Project   string `json:"project"` // real cwd, not encoded
	SessionID string `json:"sessionId"`
	Timestamp int64  `json:"timestamp"` // ms epoch
}

// Time converts the ms-epoch timestamp.
func (h HistoryEntry) Time() time.Time { return time.UnixMilli(h.Timestamp).UTC() }

// ReadHistory decodes history.jsonl, skipping malformed lines. The display
// text is truncated to maxDisplay runes (0 = keep all).
func (d Dir) ReadHistory(maxDisplay int) ([]HistoryEntry, int, error) {
	f, err := d.Open(d.HistoryPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	var out []HistoryEntry
	malformed := 0
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var h HistoryEntry
			if jerr := json.Unmarshal(line, &h); jerr != nil {
				malformed++
			} else {
				if maxDisplay > 0 {
					h.Display = truncateRunes(h.Display, maxDisplay)
				}
				out = append(out, h)
			}
		}
		if err != nil {
			break
		}
	}
	return out, malformed, nil
}

// Plan is a plan-mode document under plans/.
type Plan struct {
	Slug    string
	Title   string
	Path    string
	ModTime time.Time
	Size    int64
}

// ListPlans returns plans/*.md with the first heading as title.
func (d Dir) ListPlans() ([]Plan, error) {
	entries, err := os.ReadDir(d.PlansDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Plan
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(d.PlansDir(), e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		pl := Plan{Slug: strings.TrimSuffix(e.Name(), ".md"), Path: p, ModTime: info.ModTime().UTC(), Size: info.Size()}
		pl.Title = firstHeading(d, p)
		if pl.Title == "" {
			pl.Title = pl.Slug
		}
		out = append(out, pl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// Skill is a SKILL.md with frontmatter, from skills/ (user) or a project.
type Skill struct {
	Name        string
	Description string
	Path        string
	Origin      string // "user", "synced", "project"
}

// ListSkills finds every SKILL.md under skills/ (any depth ≤ 3).
func (d Dir) ListSkills() ([]Skill, error) {
	return d.skillsUnder(d.SkillsDir(), "user")
}

func (d Dir) skillsUnder(root, origin string) ([]Skill, error) {
	var out []Skill
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return nil
			}
			return nil
		}
		if de.IsDir() {
			if rel, _ := filepath.Rel(root, p); strings.Count(filepath.ToSlash(rel), "/") >= 4 {
				return fs.SkipDir
			}
			return nil
		}
		if de.Name() != "SKILL.md" {
			return nil
		}
		s := Skill{Path: p, Origin: origin}
		if rel, _ := filepath.Rel(root, p); strings.HasPrefix(filepath.ToSlash(rel), "synced/") {
			s.Origin = "synced"
		}
		fm := frontmatter(d, p)
		s.Name, s.Description = fm["name"], fm["description"]
		if s.Name == "" {
			s.Name = filepath.Base(filepath.Dir(p))
		}
		out = append(out, s)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Marketplace mirrors one entry of plugins/known_marketplaces.json.
type Marketplace struct {
	Name            string
	SourceKind      string
	SourceRef       string // repo, url or path depending on kind
	InstallLocation string
	LastUpdated     string
}

// ReadMarketplaces decodes plugins/known_marketplaces.json.
func (d Dir) ReadMarketplaces() ([]Marketplace, error) {
	var raw map[string]struct {
		Source struct {
			Source string `json:"source"`
			Repo   string `json:"repo"`
			URL    string `json:"url"`
			Path   string `json:"path"`
		} `json:"source"`
		InstallLocation string `json:"installLocation"`
		LastUpdated     string `json:"lastUpdated"`
	}
	ok, err := d.readJSON(d.KnownMarketplaces(), &raw)
	if err != nil || !ok {
		return nil, err
	}
	var out []Marketplace
	for name, m := range raw {
		ref := m.Source.Repo
		if ref == "" {
			ref = m.Source.URL
		}
		if ref == "" {
			ref = m.Source.Path
		}
		out = append(out, Marketplace{Name: name, SourceKind: m.Source.Source, SourceRef: ref,
			InstallLocation: m.InstallLocation, LastUpdated: m.LastUpdated})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ---- helpers ----

// readJSON decodes p into v. Returns (false, nil) when the file is absent.
func (d Dir) readJSON(p string, v any) (bool, error) {
	b, err := d.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	return true, nil
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// firstHeading returns the text of the first "# " line in a markdown file.
func firstHeading(d Dir, p string) string {
	f, err := d.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

// frontmatter parses a minimal YAML frontmatter block (key: value pairs at
// the top level only) from a markdown file.
func frontmatter(d Dir, p string) map[string]string {
	out := map[string]string{}
	f, err := d.Open(p)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return out
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue // nested key; ignore
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		out[strings.TrimSpace(k)] = v
	}
	return out
}
