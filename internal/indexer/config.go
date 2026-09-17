package indexer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"claudepulse/internal/claudedir"
	"claudepulse/internal/store"
)

// scanConfig refreshes everything that is not a transcript: per-project
// configuration, skills, plugins, plans and prompt history. Each source is
// small, so a full refresh per scan is cheap; history is the one file gated
// on a change check because it is rewritten as a whole.
func (ix *Indexer) scanConfig(ctx context.Context, projects []claudedir.Project, ids map[string]int64, realPaths map[int64]string) {
	if err := ix.scanProjectConfigs(ctx, projects, ids, realPaths); err != nil {
		ix.log.Warn("project config scan failed", "err", err)
	}
	if err := ix.scanSkills(ctx); err != nil {
		ix.log.Warn("skills scan failed", "err", err)
	}
	if err := ix.scanPlugins(ctx); err != nil {
		ix.log.Warn("plugins scan failed", "err", err)
	}
	if err := ix.scanPlans(ctx); err != nil {
		ix.log.Warn("plans scan failed", "err", err)
	}
	if err := ix.scanHistory(ctx); err != nil {
		ix.log.Warn("history scan failed", "err", err)
	}
}

func (ix *Indexer) scanProjectConfigs(ctx context.Context, projects []claudedir.Project, ids map[string]int64, realPaths map[int64]string) error {
	for _, p := range projects {
		pid := ids[p.EncodedName]
		root := realPaths[pid]
		if pid == 0 {
			continue
		}
		if root == "" {
			continue
		}
		pc, err := claudedir.ReadProjectConfig(root)
		if err != nil {
			// Directory gone (or not a dir): drop any config we captured earlier.
			if err := ix.st.Tx(ctx, func(tx *sql.Tx) error { return store.DeleteProjectConfig(ctx, tx, pid) }); err != nil {
				return err
			}
			continue
		}
		skillNames := make([]string, 0, len(pc.Skills))
		skillRows := make([]store.SkillRow, 0, len(pc.Skills))
		for _, s := range pc.Skills {
			skillNames = append(skillNames, s.Name)
			skillRows = append(skillRows, store.SkillRow{Path: s.Path, Origin: "project", Name: s.Name, Description: s.Description, ProjectID: pid})
		}
		err = ix.st.Tx(ctx, func(tx *sql.Tx) error {
			if err := store.PutProjectConfig(ctx, tx, store.ProjectConfigRow{
				ProjectID: pid, ClaudeMDBytes: pc.ClaudeMDBytes, Agents: pc.Agents, Commands: pc.Commands,
				Skills: skillNames, MCPServers: pc.MCPServers,
			}); err != nil {
				return err
			}
			for _, sf := range []struct {
				scope string
				raw   []byte
				name  string
			}{{"project", pc.Settings, "settings.json"}, {"local", pc.LocalSettings, "settings.local.json"}} {
				path := filepath.Join(root, ".claude", sf.name)
				if sf.raw == nil {
					if err := store.DeleteSettingsSnapshot(ctx, tx, path); err != nil {
						return err
					}
					continue
				}
				red, ok := RedactSettings(sf.raw)
				if !ok {
					red = []byte(`{"error":"settings file is not valid JSON"}`)
				}
				if err := store.PutSettingsSnapshot(ctx, tx, sf.scope, pid, path, red); err != nil {
					return err
				}
			}
			return store.ReplaceSkills(ctx, tx, "project", pid, skillRows)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (ix *Indexer) scanSkills(ctx context.Context) error {
	skills, err := ix.dir.ListSkills()
	if err != nil {
		return err
	}
	manifests, _ := ix.dir.ReadSyncedSkillManifests()
	byOrigin := map[string][]store.SkillRow{"user": {}, "synced": {}}
	for _, s := range skills {
		desc := s.Description
		if m, ok := manifests[s.Name]; ok && desc == "" {
			desc = m.Description
		}
		byOrigin[s.Origin] = append(byOrigin[s.Origin], store.SkillRow{Path: s.Path, Origin: s.Origin, Name: s.Name, Description: desc})
	}
	return ix.st.Tx(ctx, func(tx *sql.Tx) error {
		for origin, rows := range byOrigin {
			if err := store.ReplaceSkills(ctx, tx, origin, 0, rows); err != nil {
				return err
			}
		}
		return nil
	})
}

func (ix *Indexer) scanPlugins(ctx context.Context) error {
	var rows []store.PluginRow
	markets, err := ix.dir.ReadMarketplaces()
	if err != nil {
		return err
	}
	for _, m := range markets {
		rows = append(rows, store.PluginRow{
			Name: m.Name, Kind: "marketplace", SourceKind: m.SourceKind, SourceRef: m.SourceRef,
			InstallLocation: m.InstallLocation, LastUpdated: m.LastUpdated,
			PluginCount: ix.dir.MarketplacePluginCount(m.InstallLocation),
		})
	}
	installed, err := ix.dir.ReadInstalledPlugins()
	if err != nil {
		ix.log.Warn("installed_plugins.json unreadable; keeping marketplaces only", "err", err)
	}
	for _, p := range installed {
		rows = append(rows, store.PluginRow{
			Name: p.Name, Kind: "installed", SourceKind: "marketplace", SourceRef: p.Marketplace,
			InstallLocation: p.InstallPath, LastUpdated: p.LastUpdated, Version: p.Version, Scope: p.Scope,
		})
	}
	if n := ix.dir.SyncedPluginCount(); n > 0 {
		rows = append(rows, store.PluginRow{Name: "claude.ai synced plugins", Kind: "synced", PluginCount: n,
			InstallLocation: filepath.Join(ix.dir.PluginsDir(), "synced")})
	}
	return ix.st.ReplacePlugins(ctx, rows)
}

func (ix *Indexer) scanPlans(ctx context.Context) error {
	plans, err := ix.dir.ListPlans()
	if err != nil {
		return err
	}
	rows := make([]store.PlanRow, 0, len(plans))
	for _, p := range plans {
		rows = append(rows, store.PlanRow{Slug: p.Slug, Title: p.Title, MTime: store.FormatTime(p.ModTime), Size: p.Size, SourcePath: p.Path})
	}
	return ix.st.ReplacePlans(ctx, rows)
}

// historyDisplayRunes caps how much of each prompt is kept: enough to
// recognise it in a list, never the whole thing.
const historyDisplayRunes = 160

func (ix *Indexer) scanHistory(ctx context.Context) error {
	path := ix.dir.HistoryPath()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	prev, seen, err := ix.st.GetScanState(ctx, path)
	if err != nil {
		return err
	}
	if seen && prev.Size == info.Size() && store.FormatTime(prev.MTime) == store.FormatTime(info.ModTime()) {
		return nil
	}
	entries, malformed, err := ix.dir.ReadHistory(historyDisplayRunes)
	if err != nil {
		return err
	}
	if malformed > 0 {
		ix.log.Debug("history.jsonl", "malformed_lines", malformed)
	}
	rows := make([]store.HistoryRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, store.HistoryRow{TS: store.FormatTime(e.Time()), ProjectPath: e.Project, SessionID: e.SessionID, Display: e.Display})
	}
	return ix.st.ReplaceHistory(ctx, rows, store.ScanState{Path: path, Size: info.Size(), MTime: info.ModTime().UTC(), ByteOffset: info.Size()})
}
