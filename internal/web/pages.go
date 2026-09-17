package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"claude-monitor/internal/claudedir"
	"claude-monitor/internal/indexer"
	"claude-monitor/internal/store"
)

// layout is the data every page template receives at top level.
type layout struct {
	Title      string
	Active     string // nav key
	CLIVersion string
	ClaudeDir  string
	Index      indexer.Stats
	Addr       string // listen address shown in the sidebar
	Days       int
	Page       any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, status int, l layout) {
	t, ok := s.pages[name]
	if !ok {
		s.failPage(w, r, errNoTemplate(name))
		return
	}
	ctx := r.Context()
	l.CLIVersion, _ = s.st.GetMeta(ctx, "cli_version")
	l.ClaudeDir, _ = s.st.GetMeta(ctx, "claude_dir")
	l.Index = s.ix.Status(ctx)
	l.Addr = s.addr
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base.html", l); err != nil {
		s.failPage(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// failPage is fail() for HTML routes: logs the real error, renders the styled
// error page with a generic message, and falls back to plain text if even
// that template fails.
func (s *Server) failPage(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("page error", "path", r.URL.Path, "err", err)
	t, ok := s.pages["error"]
	if ok {
		l := layout{Title: "Something went wrong", Addr: s.addr, Page: errorPage{Code: "500", Message: "Internal error; see the server log."}}
		var buf bytes.Buffer
		if terr := t.ExecuteTemplate(&buf, "base.html", l); terr == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			buf.WriteTo(w)
			return
		}
	}
	http.Error(w, "internal error; see server log", http.StatusInternalServerError)
}

type errorPage struct {
	Code, Message, Path string
}

func (s *Server) renderPartial(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.partials.ExecuteTemplate(&buf, name, data); err != nil {
		s.fail(w, err) // htmx ignores 5xx bodies; JSON is fine here
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

type errNoTemplate string

func (e errNoTemplate) Error() string { return "no template named " + string(e) }

// ---- dashboard ----

type kpi struct {
	Label, Value, Hint string
	Help               string // one-line hover description
	Accent             string // "blue" | "purple" | ""
}

type chartDataset struct {
	Label string  `json:"label"`
	Color string  `json:"color"`
	Data  []int64 `json:"data"`
}

type dailyChart struct {
	Labels   []string       `json:"labels"`
	Datasets []chartDataset `json:"datasets"`
}

type modelShare struct {
	Model, Label, Color string
	Output, Messages    int64
	Share               string
	Width               int
}

type toolBar struct {
	Name  string
	Count int64
	Width int
}

type dashboardPage struct {
	KPIs        []kpi
	Daily       dailyChart
	Hours       []int64
	HoursMax    int64
	Models      []modelShare
	Tools       []toolBar
	Recent      []store.SessionSummary
	Live        []store.LiveSession
	Dirs        []store.DirStat
	DirsTotal   int64
	Stats       *claudedir.StatsCache
	LastUpdate  *claudedir.LastUpdate
	LastCleanup string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days := queryInt(r, "days", 30)
	if days != 0 && days != 30 && days != 90 {
		days = 30
	}

	tot, err := s.st.GetTotals(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	models, err := s.st.GetModelUsage(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	daily, err := s.st.GetDailyUsage(ctx, days)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	hours, err := s.st.GetHourCounts(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	tools, err := s.st.GetToolUsage(ctx, 8)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	recent, err := s.st.RecentSessions(ctx, 8)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	live, err := s.st.GetLiveSessions(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	dirs, err := s.st.GetDirStats(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}

	p := dashboardPage{Recent: recent, Live: live, Hours: hours[:], Daily: buildDaily(daily, days)}
	p.KPIs = []kpi{
		{Label: "Output tokens", Value: compact(tot.OutputTokens), Hint: "deduped per API message", Accent: "blue", Help: "Tokens Claude generated in replies, counted once per API message"},
		{Label: "Cache read tokens", Value: compact(tot.CacheRead), Hint: "prompt-cache hits", Accent: "purple", Help: "Context re-read from the prompt cache instead of being resent"},
		{Label: "Sessions", Value: comma(tot.Sessions), Hint: "across " + comma(tot.Projects) + " projects", Help: "Conversations found under ~/.claude/projects"},
		{Label: "Prompts", Value: comma(tot.UserPrompts), Hint: comma(tot.AssistantMsgs) + " replies", Help: "Messages you typed; replies are Claude's answers"},
		{Label: "Tool calls", Value: comma(tot.ToolCalls), Hint: comma(tot.Subagents) + " subagents", Help: "Times Claude ran a tool such as Bash, Edit or Read"},
		{Label: "Live now", Value: strconv.Itoa(len(live)), Hint: "Claude Code processes", Help: "Claude Code sessions currently running on this machine"},
	}
	for _, h := range hours {
		if h > p.HoursMax {
			p.HoursMax = h
		}
	}
	p.Models = shareModels(models)
	p.Tools = barTools(tools)
	for _, d := range dirs {
		p.DirsTotal += d.Bytes
	}
	if len(dirs) > 6 {
		dirs = dirs[:6]
	}
	p.Dirs = dirs
	if raw, _ := s.st.GetMeta(ctx, "stats_cache"); raw != "" {
		var sc claudedir.StatsCache
		if json.Unmarshal([]byte(raw), &sc) == nil {
			p.Stats = &sc
		}
	}
	if raw, _ := s.st.GetMeta(ctx, "last_update"); raw != "" {
		var lu claudedir.LastUpdate
		if json.Unmarshal([]byte(raw), &lu) == nil {
			p.LastUpdate = &lu
		}
	}
	p.LastCleanup, _ = s.st.GetMeta(ctx, "last_cleanup")

	s.render(w, r, "dashboard", http.StatusOK, layout{Title: "Overview", Active: "dashboard", Days: days, Page: p})
}

// buildDaily pivots (date, model) rows into a gap-free stacked series. Series
// order is fixed (opus, sonnet, haiku, other) so colours never shift.
func buildDaily(rows []store.DailyUsage, days int) dailyChart {
	var out dailyChart
	if len(rows) == 0 {
		return out
	}
	// Claude Code did not exist before 2025; anything earlier is a corrupt
	// timestamp and must not stretch the axis (one bogus 1970 row would
	// otherwise produce tens of thousands of labels).
	earliest := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Now().UTC().Truncate(24 * time.Hour)
	byDate := map[string]map[string]int64{}
	seen := map[string]bool{}
	var first time.Time
	for _, r := range rows {
		d, err := time.Parse("2006-01-02", r.Date)
		if err != nil || d.Before(earliest) || d.After(end) {
			continue
		}
		if first.IsZero() || d.Before(first) {
			first = d
		}
		if byDate[r.Date] == nil {
			byDate[r.Date] = map[string]int64{}
		}
		byDate[r.Date][r.Model] += r.OutputTokens
		seen[r.Model] = true
	}
	if first.IsZero() {
		return out
	}
	start := first
	if days > 0 {
		// Same boundary as store.GetDailyUsage (date >= now-days).
		start = end.AddDate(0, 0, -days)
	}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		out.Labels = append(out.Labels, d.Format("2006-01-02"))
	}
	models := make([]string, 0, len(seen))
	for m := range seen {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		ri, rj := seriesRank(models[i]), seriesRank(models[j])
		if ri != rj {
			return ri < rj
		}
		return models[i] < models[j]
	})
	for _, m := range models {
		ds := chartDataset{Label: modelLabel(m), Color: seriesColor(m)}
		var sum int64
		for _, lbl := range out.Labels {
			v := byDate[lbl][m]
			sum += v
			ds.Data = append(ds.Data, v)
		}
		if sum == 0 {
			continue // all-zero series only adds legend noise
		}
		out.Datasets = append(out.Datasets, ds)
	}
	return out
}

func seriesRank(model string) int {
	c := seriesColor(model)
	for i, s := range modelSeries {
		if s.color == c {
			return i
		}
	}
	return len(modelSeries)
}

// ---- projects ----

type projectsPage struct {
	Projects       []store.ProjectSummary
	Orphaned       int
	TotalSessions  int64
	TotalOutput    int64
	TotalToolCalls int64
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListProjects(r.Context())
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	p := projectsPage{Projects: list}
	for _, pr := range list {
		if pr.RealPath != "" && !pr.ExistsOnDisk {
			p.Orphaned++
		}
		p.TotalSessions += pr.Sessions
		p.TotalOutput += pr.OutputTokens
		p.TotalToolCalls += pr.ToolCalls
	}
	s.render(w, r, "projects", http.StatusOK, layout{Title: "Projects", Active: "projects", Page: p})
}

type projectPage struct {
	Project  store.ProjectSummary
	Sessions []store.SessionSummary
	Models   []modelShare
	Tools    []toolBar
	Duration int64
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	pr, ok, err := s.st.GetProject(ctx, id)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	if !ok {
		s.notFound(w, r)
		return
	}
	sessions, err := s.st.ListProjectSessions(ctx, id)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	models, err := s.st.ProjectModelUsage(ctx, id)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	tools, err := s.st.ProjectToolUsage(ctx, id, 8)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	p := projectPage{Project: pr, Sessions: sessions, Models: shareModels(models), Tools: barTools(tools)}
	for _, se := range sessions {
		p.Duration += se.DurationMS
	}
	title := pr.EncodedName
	if pr.RealPath != "" {
		title = basenameAny(pr.RealPath)
	}
	s.render(w, r, "project", http.StatusOK, layout{Title: title, Active: "projects", Page: p})
}

// basenameAny handles both Windows and POSIX separators regardless of host OS.
func basenameAny(p string) string {
	trimmed := strings.TrimRight(p, `\/`)
	if trimmed == "" {
		return p // a bare root such as E:\ or /
	}
	if i := strings.LastIndexAny(trimmed, `\/`); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

// ---- partials (htmx) ----

func (s *Server) handleLivePartial(w http.ResponseWriter, r *http.Request) {
	live, err := s.st.GetLiveSessions(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.renderPartial(w, "live.html", live)
}

func (s *Server) handleIndexPartial(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, "index-status.html", s.ix.Status(r.Context()))
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "error", http.StatusNotFound, layout{Title: "Not found", Page: errorPage{Code: "404", Message: "Nothing here.", Path: r.URL.Path}})
}
