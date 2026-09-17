package claudedir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledPluginsAndCounts(t *testing.T) {
	d := scratchFixture(t)
	plugins, err := d.ReadInstalledPlugins()
	if err != nil || len(plugins) != 1 {
		t.Fatalf("installed = %+v err=%v", plugins, err)
	}
	p := plugins[0]
	if p.Name != "fixture-plugin" || p.Marketplace != "claude-plugins-official" || p.Version != "1.2.3" || p.Scope != "user" {
		t.Errorf("plugin = %+v", p)
	}
	if n := d.MarketplacePluginCount(`C:\fixture\plugins\marketplaces\nope`); n != 0 {
		t.Errorf("missing marketplace count = %d", n)
	}
	if n := d.MarketplacePluginCount(""); n != 0 {
		t.Errorf("empty location count = %d", n)
	}
	if n := d.SyncedPluginCount(); n != 0 {
		t.Errorf("synced count = %d", n)
	}
	// A marketplace manifest under the fixture is counted.
	mk := filepath.Join(d.Root, "plugins", "marketplaces", "tmp-market")
	os.MkdirAll(filepath.Join(mk, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(mk, ".claude-plugin", "marketplace.json"), []byte(`{"name":"tmp","plugins":[{"name":"a"},{"name":"b"}]}`), 0o644)
	if n := d.MarketplacePluginCount(mk); n != 2 {
		t.Errorf("marketplace count = %d, want 2", n)
	}
}

func TestSyncedSkillManifests(t *testing.T) {
	d := scratchFixture(t)
	root := filepath.Join(d.Root, "skills", "synced", "skill-one")
	os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"skills":[{"name":"skill-one","description":"from manifest","source":"anthropic-example"}]}`), 0o644)
	m, err := d.ReadSyncedSkillManifests()
	if err != nil || m["skill-one"].Source != "anthropic-example" {
		t.Errorf("manifests = %+v err=%v", m, err)
	}
}

func TestReadProjectConfig(t *testing.T) {
	root := t.TempDir()
	must := func(p, body string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(root, "CLAUDE.md"), "# hi\n")
	must(filepath.Join(root, ".claude", "settings.json"), `{"model":"opus","env":{"SECRET":"x"}}`)
	must(filepath.Join(root, ".claude", "agents", "reviewer.md"), "---\nname: reviewer\n---\n")
	must(filepath.Join(root, ".claude", "agents", "notes.txt"), "ignored")
	must(filepath.Join(root, ".claude", "commands", "deploy.md"), "run it")
	must(filepath.Join(root, ".claude", "skills", "my-skill", "SKILL.md"), "---\nname: my-skill\ndescription: does things\n---\n")
	must(filepath.Join(root, ".mcp.json"), `{"mcpServers":{"github":{"command":"x"},"db":{"url":"y"}}}`)

	pc, err := ReadProjectConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if pc.ClaudeMDBytes != 5 || pc.Settings == nil || pc.LocalSettings != nil {
		t.Errorf("claude.md/settings: %+v", pc)
	}
	if len(pc.Agents) != 1 || pc.Agents[0] != "reviewer" || len(pc.Commands) != 1 || pc.Commands[0] != "deploy" {
		t.Errorf("agents/commands: %v %v", pc.Agents, pc.Commands)
	}
	if len(pc.Skills) != 1 || pc.Skills[0].Name != "my-skill" || pc.Skills[0].Origin != "project" || pc.Skills[0].Description != "does things" {
		t.Errorf("skills: %+v", pc.Skills)
	}
	if len(pc.MCPServers) != 2 || pc.MCPServers[0] != "db" || pc.MCPServers[1] != "github" {
		t.Errorf("mcp: %v", pc.MCPServers)
	}
	if _, err := ReadProjectConfig(filepath.Join(root, "missing")); err == nil {
		t.Error("missing dir should error")
	}
	if pc, err := ReadProjectConfig(t.TempDir()); err != nil || pc.ClaudeMDBytes != 0 || pc.Agents != nil {
		t.Errorf("empty dir: %+v err=%v", pc, err)
	}
}

func TestTopLevelStatsHidesSecrets(t *testing.T) {
	d := fixture(t)
	stats, err := d.TopLevelStats()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stats {
		if s.Name == ".credentials.json" {
			t.Error(".credentials.json must not be listed")
		}
	}
}
