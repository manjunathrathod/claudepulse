package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"claude-monitor/internal/store"
)

// Info is runtime configuration shown on the System page.
type Info struct {
	ClaudeDir    string
	DBPath       string
	ScanInterval time.Duration
	StartedAt    time.Time
	Version      string
}

// SetInfo records runtime configuration for display.
func (s *Server) SetInfo(i Info) { s.info = i }

// ---- settings ----

type settingsSummary struct {
	Model, DefaultMode, Theme, UpdateChannel string
	Allow, Deny, Ask                         int
	Hooks                                    []string
	EnvKeys                                  []string
	Plugins                                  []string
	Keys                                     []string // top-level keys, sorted
}

type settingsPage struct {
	UserJSON     string
	UserSummary  settingsSummary
	UserPath     string
	Projects     []store.ProjectConfigView
	Selected     *store.ProjectConfigView
	SelectedJSON []store.SettingsSnapshot
	WithConfig   int
}

// summarizeSettings pulls the headline facts out of a (redacted) settings doc.
func summarizeSettings(raw string) settingsSummary {
	var sum settingsSummary
	var doc map[string]any
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return sum
	}
	for k := range doc {
		sum.Keys = append(sum.Keys, k)
	}
	sort.Strings(sum.Keys)
	str := func(k string) string {
		if v, ok := doc[k].(string); ok {
			return v
		}
		return ""
	}
	sum.Model, sum.Theme, sum.UpdateChannel = str("model"), str("theme"), str("autoUpdatesChannel")
	if perms, ok := doc["permissions"].(map[string]any); ok {
		if v, ok := perms["defaultMode"].(string); ok {
			sum.DefaultMode = v
		}
		count := func(k string) int {
			if a, ok := perms[k].([]any); ok {
				return len(a)
			}
			return 0
		}
		sum.Allow, sum.Deny, sum.Ask = count("allow"), count("deny"), count("ask")
	}
	if hooks, ok := doc["hooks"].(map[string]any); ok {
		for k := range hooks {
			sum.Hooks = append(sum.Hooks, k)
		}
		sort.Strings(sum.Hooks)
	}
	if env, ok := doc["env"].(map[string]any); ok {
		for k := range env {
			sum.EnvKeys = append(sum.EnvKeys, k)
		}
		sort.Strings(sum.EnvKeys)
	}
	if pl, ok := doc["enabledPlugins"].(map[string]any); ok {
		for k, v := range pl {
			if b, ok := v.(bool); !ok || b {
				sum.Plugins = append(sum.Plugins, k)
			}
		}
		sort.Strings(sum.Plugins)
	}
	return sum
}

// prettyJSON re-indents a JSON document for display; invalid input is returned as is.
func prettyJSON(raw string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return buf.String()
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw, _ := s.st.GetMeta(ctx, "user_settings")
	dir, _ := s.st.GetMeta(ctx, "claude_dir")
	p := settingsPage{UserJSON: prettyJSON(raw), UserSummary: summarizeSettings(raw), UserPath: dir + string(os.PathSeparator) + "settings.json"}
	projects, err := s.st.ListProjectConfigs(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	p.Projects = projects
	for _, pr := range projects {
		if pr.ClaudeMDBytes > 0 || pr.HasSettings || pr.HasLocal || len(pr.Agents)+len(pr.Commands)+len(pr.Skills)+len(pr.MCPServers) > 0 {
			p.WithConfig++
		}
	}
	if id, err := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64); err == nil && id > 0 {
		for i := range projects {
			if projects[i].ProjectID == id {
				p.Selected = &projects[i]
				snaps, err := s.st.ProjectSettings(ctx, id)
				if err != nil {
					s.failPage(w, r, err)
					return
				}
				for i := range snaps {
					snaps[i].JSON = prettyJSON(snaps[i].JSON)
				}
				p.SelectedJSON = snaps
			}
		}
	}
	s.render(w, r, "settings", http.StatusOK, layout{Title: "Settings", Active: "settings", Page: p})
}

// ---- skills & plugins ----

type skillsPage struct {
	User, Synced, Project []store.SkillView
	Marketplaces          []store.PluginRow
	Installed             []store.PluginRow
	Synced_               []store.PluginRow
	TotalSkills           int
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	skills, err := s.st.ListSkills(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	p := skillsPage{TotalSkills: len(skills)}
	for _, sk := range skills {
		switch sk.Origin {
		case "user":
			p.User = append(p.User, sk)
		case "synced":
			p.Synced = append(p.Synced, sk)
		default:
			p.Project = append(p.Project, sk)
		}
	}
	for _, kind := range []string{"marketplace", "installed", "synced"} {
		rows, err := s.st.ListPlugins(ctx, kind)
		if err != nil {
			s.failPage(w, r, err)
			return
		}
		switch kind {
		case "marketplace":
			p.Marketplaces = rows
		case "installed":
			p.Installed = rows
		default:
			p.Synced_ = rows
		}
	}
	s.render(w, r, "skills", http.StatusOK, layout{Title: "Skills & plugins", Active: "skills", Page: p})
}

// ---- plans & history ----

const historyPerPage = 50

type plansPage struct {
	Plans            []store.PlanRow
	History          []store.HistoryView
	Total            int64
	Query            string
	Page, Last       int
	PrevURL, NextURL string
}

func historyURL(q string, page int) string {
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/plans"
	}
	return "/plans?" + v.Encode()
}

func (s *Server) handlePlans(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	plans, err := s.st.ListPlans(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	q := truncateRunes(strings.TrimSpace(r.URL.Query().Get("q")), 80)
	page := queryInt(r, "page", 1)
	if page < 1 || page > 1_000_000 {
		page = 1
	}
	f := store.HistoryFilter{Query: q, Limit: historyPerPage, Offset: (page - 1) * historyPerPage}
	hist, total, err := s.st.ListHistory(ctx, f)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	last := int((total + historyPerPage - 1) / historyPerPage)
	if last < 1 {
		last = 1
	}
	if page > last {
		page = last
		f.Offset = (page - 1) * historyPerPage
		if hist, total, err = s.st.ListHistory(ctx, f); err != nil {
			s.failPage(w, r, err)
			return
		}
	}
	p := plansPage{Plans: plans, History: hist, Total: total, Query: q, Page: page, Last: last}
	if page > 1 {
		p.PrevURL = historyURL(q, page-1)
	}
	if page < last {
		p.NextURL = historyURL(q, page+1)
	}
	s.render(w, r, "plans", http.StatusOK, layout{Title: "Plans & history", Active: "plans", Page: p})
}

// ---- system ----

type systemPage struct {
	Info        Info
	Uptime      int64
	DBBytes     int64
	Counts      map[string]int64
	CountKeys   []string
	Dirs        []store.DirStat
	DirsTotal   int64
	LastUpdate  map[string]any
	LastCleanup string
	Live        []store.LiveSession
	GoVersion   string
}

func (s *Server) handleSystemPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := systemPage{Info: s.info, GoVersion: goVersion}
	if !s.info.StartedAt.IsZero() {
		p.Uptime = time.Since(s.info.StartedAt).Milliseconds()
	}
	if st, err := os.Stat(s.info.DBPath); err == nil {
		p.DBBytes = st.Size()
		for _, suffix := range []string{"-wal", "-shm"} {
			if st2, err := os.Stat(s.info.DBPath + suffix); err == nil {
				p.DBBytes += st2.Size()
			}
		}
	}
	counts, err := s.st.TableCounts(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	p.Counts = counts
	for k := range counts {
		p.CountKeys = append(p.CountKeys, k)
	}
	sort.Strings(p.CountKeys)
	dirs, err := s.st.GetDirStats(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	p.Dirs = dirs
	for _, d := range dirs {
		p.DirsTotal += d.Bytes
	}
	if raw, _ := s.st.GetMeta(ctx, "last_update"); raw != "" {
		json.Unmarshal([]byte(raw), &p.LastUpdate)
	}
	p.LastCleanup, _ = s.st.GetMeta(ctx, "last_cleanup")
	if p.Live, err = s.st.GetLiveSessions(ctx); err != nil {
		s.failPage(w, r, err)
		return
	}
	s.render(w, r, "system", http.StatusOK, layout{Title: "System", Active: "system", Page: p})
}
