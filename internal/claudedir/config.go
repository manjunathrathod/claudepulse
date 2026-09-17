package claudedir

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// InstalledPlugin mirrors one entry of plugins/installed_plugins.json.
type InstalledPlugin struct {
	Name        string // plugin name
	Marketplace string
	Scope       string
	Version     string
	InstallPath string
	InstalledAt string
	LastUpdated string
}

// ReadInstalledPlugins decodes plugins/installed_plugins.json (version 2).
func (d Dir) ReadInstalledPlugins() ([]InstalledPlugin, error) {
	var raw struct {
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			InstallPath string `json:"installPath"`
			Version     string `json:"version"`
			InstalledAt string `json:"installedAt"`
			LastUpdated string `json:"lastUpdated"`
		} `json:"plugins"`
	}
	ok, err := d.readJSON(filepath.Join(d.PluginsDir(), "installed_plugins.json"), &raw)
	if err != nil || !ok {
		return nil, err
	}
	var out []InstalledPlugin
	for key, installs := range raw.Plugins {
		name, market, _ := strings.Cut(key, "@")
		for _, in := range installs {
			out = append(out, InstalledPlugin{Name: name, Marketplace: market, Scope: in.Scope, Version: in.Version,
				InstallPath: in.InstallPath, InstalledAt: in.InstalledAt, LastUpdated: in.LastUpdated})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// MarketplacePluginCount reads <installLocation>/.claude-plugin/marketplace.json
// and returns how many plugins it lists (0 if unreadable).
func (d Dir) MarketplacePluginCount(installLocation string) int {
	if installLocation == "" {
		return 0
	}
	var m struct {
		Plugins []json.RawMessage `json:"plugins"`
	}
	// The install location is under ~/.claude/plugins/marketplaces; guard anyway.
	if d.IsDenied(filepath.Join(installLocation, ".claude-plugin", "marketplace.json")) {
		return 0
	}
	ok, err := d.readJSON(filepath.Join(installLocation, ".claude-plugin", "marketplace.json"), &m)
	if err != nil || !ok {
		return 0
	}
	return len(m.Plugins)
}

// SyncedPluginCount counts directories under plugins/synced.
func (d Dir) SyncedPluginCount() int {
	entries, err := os.ReadDir(filepath.Join(d.PluginsDir(), "synced"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

// ProjectConfig is what a project's own directory contributes: CLAUDE.md,
// .claude/{settings.json,settings.local.json,agents,commands,skills}, .mcp.json.
type ProjectConfig struct {
	ClaudeMDBytes int64
	Settings      []byte // raw settings.json, nil if absent
	LocalSettings []byte // raw settings.local.json, nil if absent
	Agents        []string
	Commands      []string
	Skills        []Skill
	MCPServers    []string
}

// ReadProjectConfig inspects a project directory (the real cwd). It reads only
// well-known config files, never source code. Missing files are simply absent.
func ReadProjectConfig(root string) (ProjectConfig, error) {
	var pc ProjectConfig
	info, err := os.Stat(root)
	if err != nil {
		return pc, err
	}
	if !info.IsDir() {
		return pc, errors.New("not a directory")
	}
	if st, err := os.Stat(filepath.Join(root, "CLAUDE.md")); err == nil && !st.IsDir() {
		pc.ClaudeMDBytes = st.Size()
	}
	cl := filepath.Join(root, ".claude")
	pc.Settings = readSmall(filepath.Join(cl, "settings.json"))
	pc.LocalSettings = readSmall(filepath.Join(cl, "settings.local.json"))
	pc.Agents = mdNames(filepath.Join(cl, "agents"))
	pc.Commands = mdNames(filepath.Join(cl, "commands"))
	pc.Skills, _ = Dir{Root: root}.skillsUnder(filepath.Join(cl, "skills"), "project")
	if b := readSmall(filepath.Join(root, ".mcp.json")); b != nil {
		var m struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal(b, &m) == nil {
			for name := range m.Servers {
				pc.MCPServers = append(pc.MCPServers, name)
			}
			sort.Strings(pc.MCPServers)
		}
	}
	return pc, nil
}

// readSmall returns a file's bytes if it exists and is under 1 MB, else nil.
func readSmall(p string) []byte {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() || st.Size() > 1<<20 {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return b
}

// mdNames lists *.md basenames (without extension) in dir.
func mdNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	sort.Strings(out)
	return out
}

// SyncedSkillMeta is one entry of skills/synced/<id>/manifest.json.
type SyncedSkillMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	UpdatedAt   string `json:"updatedAt"`
}

// ReadSyncedSkillManifests merges every skills/synced/*/manifest.json.
func (d Dir) ReadSyncedSkillManifests() (map[string]SyncedSkillMeta, error) {
	out := map[string]SyncedSkillMeta{}
	root := filepath.Join(d.SkillsDir(), "synced")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var m struct {
			Skills []SyncedSkillMeta `json:"skills"`
		}
		if ok, err := d.readJSON(filepath.Join(root, e.Name(), "manifest.json"), &m); err != nil || !ok {
			continue
		}
		for _, s := range m.Skills {
			out[s.Name] = s
		}
	}
	return out, nil
}
