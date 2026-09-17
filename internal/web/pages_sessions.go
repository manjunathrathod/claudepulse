package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"claude-monitor/internal/store"
)

const sessionsPerPage = 50

// sessionsPage is the view model for /sessions.
type sessionsPage struct {
	Sessions   []store.SessionSummary
	Total      int64
	Page, Last int
	Projects   []store.ProjectOption
	Models     []string
	Filter     sessionsQuery
	PrevURL    string
	NextURL    string
	TotalOut   int64
	TotalTools int64
	Duration   int64
}

// sessionsQuery mirrors the filter form so the template can re-populate it.
type sessionsQuery struct {
	Project int64
	Model   string
	Range   string // 7 | 30 | 90 | all
	Q       string
}

// pageURL renders a /sessions link that preserves the filters. Built with
// url.Values so it is safe to drop into an href as a whole value.
func (q sessionsQuery) pageURL(page int) string {
	v := url.Values{}
	if q.Project > 0 {
		v.Set("project", strconv.FormatInt(q.Project, 10))
	}
	if q.Model != "" {
		v.Set("model", q.Model)
	}
	if q.Range != "" && q.Range != "all" {
		v.Set("range", q.Range)
	}
	if q.Q != "" {
		v.Set("q", q.Q)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/sessions"
	}
	return "/sessions?" + v.Encode()
}

func parseSessionsQuery(r *http.Request) sessionsQuery {
	v := r.URL.Query()
	q := sessionsQuery{Model: v.Get("model"), Range: v.Get("range"), Q: strings.TrimSpace(v.Get("q"))}
	if id, err := strconv.ParseInt(v.Get("project"), 10, 64); err == nil && id > 0 {
		q.Project = id
	}
	switch q.Range {
	case "7", "30", "90":
	default:
		q.Range = "all"
	}
	q.Q = truncateRunes(q.Q, 80)
	q.Model = truncateRunes(q.Model, 64)
	return q
}

// truncateRunes cuts s to at most n runes without splitting a code point.
func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (q sessionsQuery) filter() store.SessionFilter {
	f := store.SessionFilter{ProjectID: q.Project, Model: q.Model, Query: q.Q}
	if d, err := strconv.Atoi(q.Range); err == nil && d > 0 {
		f.Since = time.Now().UTC().AddDate(0, 0, -d)
	}
	return f
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := parseSessionsQuery(r)
	page := queryInt(r, "page", 1)
	if page < 1 || page > 1_000_000 {
		page = 1
	}
	f := q.filter()
	f.Limit, f.Offset = sessionsPerPage, (page-1)*sessionsPerPage
	sessions, total, err := s.st.ListSessions(ctx, f)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	projects, err := s.st.ProjectOptions(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	models, err := s.st.ModelOptions(ctx)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	last := int((total + sessionsPerPage - 1) / sessionsPerPage)
	if last < 1 {
		last = 1
	}
	if page > last {
		// Past the end: re-query the real last page rather than show an empty table.
		page = last
		f.Offset = (page - 1) * sessionsPerPage
		if sessions, total, err = s.st.ListSessions(ctx, f); err != nil {
			s.failPage(w, r, err)
			return
		}
	}
	p := sessionsPage{Sessions: sessions, Total: total, Page: page, Last: last, Projects: projects, Models: models, Filter: q}
	if page > 1 {
		p.PrevURL = q.pageURL(page - 1)
	}
	if page < last {
		p.NextURL = q.pageURL(page + 1)
	}
	for _, se := range sessions {
		p.TotalOut += se.OutputTokens
		p.TotalTools += se.ToolCalls
		p.Duration += se.DurationMS
	}
	s.render(w, r, "sessions", http.StatusOK, layout{Title: "Sessions", Active: "sessions", Page: p})
}

// ---- session detail ----

type timelineBucket struct {
	Label    string `json:"label"`
	Prompts  int64  `json:"prompts"`
	Replies  int64  `json:"replies"`
	Tools    int64  `json:"tools"`
	Output   int64  `json:"output"`
	Subagent int64  `json:"subagent"` // replies on side chains
}

type timelineChart struct {
	BucketMinutes int              `json:"bucket_minutes"`
	Color         string           `json:"color"`
	Buckets       []timelineBucket `json:"buckets"`
}

type sessionPage struct {
	Session   store.SessionDetail
	Models    []modelShare
	Tools     []toolBar
	Subagents []store.Subagent
	Timeline  timelineChart
	Events    int
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !looksLikeSessionID(id) {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	d, ok, err := s.st.GetSession(ctx, id)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	if !ok {
		s.notFound(w, r)
		return
	}
	models, err := s.st.SessionModelUsage(ctx, id)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	tools, err := s.st.SessionToolUsage(ctx, id, 10)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	subs, err := s.st.SessionSubagents(ctx, id)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	events, err := s.st.SessionEvents(ctx, id)
	if err != nil {
		s.failPage(w, r, err)
		return
	}
	p := sessionPage{Session: d, Subagents: subs, Events: len(events)}
	p.Models = shareModels(models)
	p.Tools = barTools(tools)
	p.Timeline = buildTimeline(events)
	title := d.Title
	if title == "" {
		title = "Session " + truncate(id, 9)
	}
	s.render(w, r, "session", http.StatusOK, layout{Title: title, Active: "sessions", Page: p})
}

// looksLikeSessionID accepts UUID-shaped ids only, so arbitrary strings never
// reach the query layer or the 404 page.
func looksLikeSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
		default:
			return false
		}
	}
	return true
}

// buildTimeline buckets a session's messages. The bucket width adapts to the
// session length so the chart always has a readable number of bars.
func buildTimeline(events []store.TimelineEvent) timelineChart {
	tl := timelineChart{Color: "#3987e5"}
	if len(events) == 0 {
		return tl
	}
	// Events come ordered by timestamp text; take the true min/max anyway so
	// mixed-precision timestamps cannot put "end" before "start".
	start, end := events[0].TS, events[0].TS
	for _, e := range events[1:] {
		if e.TS.Before(start) {
			start = e.TS
		}
		if e.TS.After(end) {
			end = e.TS
		}
	}
	// Pick the smallest "nice" width that keeps the chart at ≤ maxBars bars, so
	// even a corrupt far-past timestamp cannot produce thousands of buckets.
	const maxBars = 200
	span := end.Sub(start)
	steps := []int{1, 5, 15, 60, 6 * 60, 24 * 60, 7 * 24 * 60, 30 * 24 * 60}
	tl.BucketMinutes = steps[len(steps)-1]
	for _, m := range steps {
		if span/(time.Duration(m)*time.Minute) < maxBars {
			tl.BucketMinutes = m
			break
		}
	}
	width := time.Duration(tl.BucketMinutes) * time.Minute
	// Buckets are aligned in local time so a "day" bar is the viewer's day.
	origin := truncateLocal(start, width)
	n := int(end.Sub(origin)/width) + 1
	if n > maxBars*2 { // only reachable with the largest step and a >30-year span
		n = maxBars * 2
	}
	tl.Buckets = make([]timelineBucket, n)
	for i := range tl.Buckets {
		t := origin.Add(time.Duration(i) * width)
		if tl.BucketMinutes >= 24*60 {
			tl.Buckets[i].Label = t.Format("2 Jan")
		} else {
			tl.Buckets[i].Label = t.Format("15:04")
		}
	}
	for _, e := range events {
		i := int(e.TS.Sub(origin) / width)
		if i < 0 || i >= n {
			continue
		}
		b := &tl.Buckets[i]
		switch {
		case e.Role == "user" && e.Kind == "prompt" && !e.Sidechain:
			b.Prompts++
		case e.Role == "assistant" && e.Sidechain:
			b.Subagent++
			b.Output += e.OutputTokens
			b.Tools += e.ToolCalls
		case e.Role == "assistant":
			b.Replies++
			b.Output += e.OutputTokens
			b.Tools += e.ToolCalls
		}
	}
	return tl
}

// truncateLocal floors t to a multiple of width measured from local midnight
// (for sub-day widths) or to local midnight / the local day for day-and-up
// widths, so bucket boundaries match what the viewer's clock shows.
func truncateLocal(t time.Time, width time.Duration) time.Time {
	lt := t.Local()
	midnight := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, lt.Location())
	if width >= 24*time.Hour {
		return midnight
	}
	return midnight.Add(lt.Sub(midnight).Truncate(width))
}

// shareModels/barTools are the shared view-model builders used by the
// dashboard, project and session pages.
func shareModels(models []store.ModelUsage) []modelShare {
	var total int64
	for _, m := range models {
		total += m.OutputTokens
	}
	var out []modelShare
	for _, m := range models {
		if m.OutputTokens == 0 {
			continue
		}
		ms := modelShare{Model: m.Model, Label: modelLabel(m.Model), Color: seriesColor(m.Model),
			Output: m.OutputTokens, Messages: m.Messages, Share: pct(m.OutputTokens, total)}
		if total > 0 {
			ms.Width = int(float64(m.OutputTokens) / float64(total) * 100)
		}
		out = append(out, ms)
	}
	return out
}

func barTools(tools []store.ToolUsage) []toolBar {
	var maxTool int64
	for _, t := range tools {
		if t.Count > maxTool {
			maxTool = t.Count
		}
	}
	var out []toolBar
	for _, t := range tools {
		tb := toolBar{Name: t.Name, Count: t.Count}
		if maxTool > 0 {
			tb.Width = int(float64(t.Count) / float64(maxTool) * 100)
		}
		out = append(out, tb)
	}
	return out
}

// ---- JSON ----

func (s *Server) handleSessionsJSON(w http.ResponseWriter, r *http.Request) {
	q := parseSessionsQuery(r)
	f := q.filter()
	f.Limit = queryInt(r, "limit", 100)
	if f.Limit <= 0 {
		f.Limit = 100 // 0 would mean "no LIMIT" in the store; never allow that here
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	f.Offset = queryInt(r, "offset", 0)
	rows, total, err := s.st.ListSessions(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	if rows == nil {
		rows = []store.SessionSummary{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"total": total, "sessions": rows})
}

func (s *Server) handleSessionJSON(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !looksLikeSessionID(id) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	ctx := r.Context()
	d, ok, err := s.st.GetSession(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	models, err := s.st.SessionModelUsage(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	tools, err := s.st.SessionToolUsage(ctx, id, 0)
	if err != nil {
		s.fail(w, err)
		return
	}
	subs, err := s.st.SessionSubagents(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	events, err := s.st.SessionEvents(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if models == nil {
		models = []store.ModelUsage{}
	}
	if tools == nil {
		tools = []store.ToolUsage{}
	}
	if subs == nil {
		subs = []store.Subagent{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"session": d, "models": models, "tools": tools, "subagents": subs, "timeline": buildTimeline(events),
	})
}
