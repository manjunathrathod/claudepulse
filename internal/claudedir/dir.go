// Package claudedir knows the on-disk layout of a Claude Code home directory
// (~/.claude) and provides typed decoders for the files inside it. It never
// writes. See .claude/skills/claude-dir-format/SKILL.md for the reference.
package claudedir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir is a handle on a Claude Code home directory.
type Dir struct {
	Root string
}

// New returns a Dir rooted at root. It does not check that root exists.
func New(root string) Dir { return Dir{Root: filepath.Clean(root)} }

// Well-known paths.
func (d Dir) SettingsPath() string    { return filepath.Join(d.Root, "settings.json") }
func (d Dir) StatsCachePath() string  { return filepath.Join(d.Root, "stats-cache.json") }
func (d Dir) HistoryPath() string     { return filepath.Join(d.Root, "history.jsonl") }
func (d Dir) LastUpdatePath() string  { return filepath.Join(d.Root, ".last-update-result.json") }
func (d Dir) LastCleanupPath() string { return filepath.Join(d.Root, ".last-cleanup") }
func (d Dir) ProjectsDir() string     { return filepath.Join(d.Root, "projects") }
func (d Dir) SessionsDir() string     { return filepath.Join(d.Root, "sessions") }
func (d Dir) PlansDir() string        { return filepath.Join(d.Root, "plans") }
func (d Dir) SkillsDir() string       { return filepath.Join(d.Root, "skills") }
func (d Dir) PluginsDir() string      { return filepath.Join(d.Root, "plugins") }
func (d Dir) KnownMarketplaces() string {
	return filepath.Join(d.PluginsDir(), "known_marketplaces.json")
}

// ErrDenied is returned for paths the monitor must never open.
var ErrDenied = errors.New("claudedir: access to this path is denied")

// IsDenied reports whether p is a secret the monitor must never read:
// the OAuth credentials file and the per-session peer-token key files.
func (d Dir) IsDenied(p string) bool {
	rel, err := filepath.Rel(d.Root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		// Outside the root entirely; not ours to read.
		return true
	}
	rel = filepath.ToSlash(rel)
	if rel == ".credentials.json" {
		return true
	}
	if strings.HasPrefix(rel, "sessions/") && strings.HasSuffix(rel, ".key") {
		return true
	}
	return false
}

// Open opens a file for reading after checking the deny-list.
func (d Dir) Open(p string) (*os.File, error) {
	if d.IsDenied(p) {
		return nil, ErrDenied
	}
	return os.Open(p)
}

// ReadFile reads a whole file after checking the deny-list.
func (d Dir) ReadFile(p string) ([]byte, error) {
	if d.IsDenied(p) {
		return nil, ErrDenied
	}
	return os.ReadFile(p)
}

// Project is one entry under projects/.
type Project struct {
	EncodedName string   // directory name, e.g. C--Users-me-Desktop-app
	Path        string   // absolute path of the project directory
	Transcripts []string // absolute paths of <session>.jsonl files
	MemoryDir   string   // absolute path of memory/ if present, else ""
}

// SessionID derives the session UUID from a transcript path.
func SessionID(transcriptPath string) string {
	return strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
}

// Subagent is one subagent transcript under <session>/subagents/.
type Subagent struct {
	AgentID  string // e.g. a83dd4d91a7d160ac (from agent-<id>.jsonl)
	Path     string // transcript path
	MetaPath string // sibling .meta.json, may not exist
}

// ListProjects enumerates projects/ and their main transcripts. Projects
// without any transcript are still returned (they may only hold memory/).
func (d Dir) ListProjects() ([]Project, error) {
	entries, err := os.ReadDir(d.ProjectsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := Project{EncodedName: e.Name(), Path: filepath.Join(d.ProjectsDir(), e.Name())}
		files, err := os.ReadDir(p.Path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // removed between the two listings
			}
			// Do not silently drop a project: the caller prunes anything it
			// cannot see, so a transient error must fail the whole listing.
			return nil, fmt.Errorf("read project %s: %w", e.Name(), err)
		}
		for _, f := range files {
			switch {
			case f.IsDir() && f.Name() == "memory":
				p.MemoryDir = filepath.Join(p.Path, "memory")
			case !f.IsDir() && strings.HasSuffix(f.Name(), ".jsonl"):
				p.Transcripts = append(p.Transcripts, filepath.Join(p.Path, f.Name()))
			}
		}
		sort.Strings(p.Transcripts)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EncodedName < out[j].EncodedName })
	return out, nil
}

// ListSubagents returns the subagent transcripts for a session transcript.
func (d Dir) ListSubagents(transcriptPath string) ([]Subagent, error) {
	dir := filepath.Join(filepath.Dir(transcriptPath), SessionID(transcriptPath), "subagents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Subagent
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
		out = append(out, Subagent{
			AgentID:  id,
			Path:     filepath.Join(dir, name),
			MetaPath: filepath.Join(dir, "agent-"+id+".meta.json"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out, nil
}

// DirStat is the size of one top-level entry of the Claude home.
type DirStat struct {
	Name  string
	Files int64
	Bytes int64
}

// TopLevelStats walks each top-level entry and sums file counts and bytes.
// Denied files are skipped entirely (not even listed by name).
func (d Dir) TopLevelStats() ([]DirStat, error) {
	entries, err := os.ReadDir(d.Root)
	if err != nil {
		return nil, err
	}
	var out []DirStat
	for _, e := range entries {
		full := filepath.Join(d.Root, e.Name())
		if d.IsDenied(full) {
			continue // secrets are not even listed by name
		}
		st := DirStat{Name: e.Name()}
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				st.Files, st.Bytes = 1, info.Size()
			}
			out = append(out, st)
			continue
		}
		_ = filepath.WalkDir(full, func(_ string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return nil
			}
			if info, err := de.Info(); err == nil {
				st.Files++
				st.Bytes += info.Size()
			}
			return nil
		})
		out = append(out, st)
	}
	return out, nil
}
