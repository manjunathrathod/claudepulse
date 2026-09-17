package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"claude-monitor/internal/store"
)

const (
	alphaSession = "aaaaaaaa-0000-4000-8000-000000000001"
	betaSession  = "bbbbbbbb-0000-4000-8000-000000000002"
)

func TestSessionsListAndFilters(t *testing.T) {
	srv := testServer(t)
	all := getHTML(t, srv, "/sessions", http.StatusOK)
	for _, want := range []string{
		`href="/sessions/` + alphaSession + `"`, `href="/sessions/` + betaSession + `"`,
		"Alpha fixture session &lt;b&gt;", "Beta fixture session",
		`<option value="claude-opus-5"`, `<option value="claude-sonnet-5"`,
		"page 1 of 1",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("sessions list missing %q", want)
		}
	}
	if strings.Contains(all, `<b>&"x"</b>`) {
		t.Error("title rendered unescaped")
	}

	// Model filter keeps only the sonnet session; the select stays populated.
	sonnet := getHTML(t, srv, "/sessions?model=claude-sonnet-5", http.StatusOK)
	if strings.Contains(sonnet, alphaSession) || !strings.Contains(sonnet, betaSession) {
		t.Error("model filter did not narrow to the sonnet session")
	}
	if !strings.Contains(sonnet, `<option value="claude-sonnet-5" selected`) {
		t.Error("model filter not re-selected in the form")
	}
	// Title search is case-insensitive.
	q := getHTML(t, srv, "/sessions?q=BETA", http.StatusOK)
	if strings.Contains(q, alphaSession) || !strings.Contains(q, betaSession) {
		t.Error("title search failed")
	}
	// Project filter via id from the projects page.
	proj := getHTML(t, srv, "/sessions?project=1", http.StatusOK)
	if !strings.Contains(proj, `<option value="1" selected`) {
		t.Error("project filter not re-selected")
	}
	// A recent range excludes the 2026-09 fixture (unless run within 7 days of it).
	recent := getHTML(t, srv, "/sessions?range=7", http.StatusOK)
	if time.Since(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)) > 7*24*time.Hour && strings.Contains(recent, betaSession) {
		t.Error("range filter did not exclude old sessions")
	}
	// Hostile query text is escaped in the form value.
	hostile := getHTML(t, srv, `/sessions?q=%3Cscript%3Ealert(1)%3C/script%3E`, http.StatusOK)
	if strings.Contains(hostile, "<script>alert(1)") {
		t.Error("search text rendered unescaped")
	}
	// Bad page numbers fall back gracefully.
	getHTML(t, srv, "/sessions?page=-5", http.StatusOK)
	getHTML(t, srv, "/sessions?page=999", http.StatusOK)
}

func TestSessionDetail(t *testing.T) {
	srv := testServer(t)
	page := getHTML(t, srv, "/sessions/"+alphaSession, http.StatusOK)
	for _, want := range []string{
		"Alpha fixture session &lt;b&gt;", // title in <h1>
		alphaSession,                      // id shown
		`fixture\alpha`,                   // cwd
		`class="chip purple">1 subagent</span>`,
		`class="chip purple">reviewer</span>`, "Review scrubbed", // subagent row
		`id="timeline-data"`, `"bucket_minutes":1`,
		">Bash<", ">Read<", ">Edit<", ">Grep<", // tools incl. the subagent's
		"Opus 5",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("session page missing %q", want)
		}
	}
	// Hero figures: prompts 2, replies 4, tool calls 4, output 200 (see indexer golden test).
	for _, want := range []string{
		`<dt>Prompts</dt><dd>2</dd>`, `<dt>Replies</dt><dd>4</dd>`, `<dt>Tool calls</dt><dd>4</dd>`,
		`<dt>Output tokens</dt><dd title="200">200</dd>`, `<dt>Thinking</dt><dd title="40">40</dd>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("session hero missing %q", want)
		}
	}

	getHTML(t, srv, "/sessions/"+betaSession, http.StatusOK)
	getHTML(t, srv, "/sessions/aaaaaaaa-0000-4000-8000-000000000099", http.StatusNotFound)
	getHTML(t, srv, "/sessions/not-a-uuid", http.StatusNotFound)
	getHTML(t, srv, "/sessions/../etc", http.StatusNotFound)
}

func TestSessionsJSON(t *testing.T) {
	srv := testServer(t)
	var list struct {
		Total    int64                  `json:"total"`
		Sessions []store.SessionSummary `json:"sessions"`
	}
	getJSON(t, srv, "/api/v1/sessions?limit=1", &list)
	if list.Total != 2 || len(list.Sessions) != 1 {
		t.Errorf("sessions json: total=%d n=%d", list.Total, len(list.Sessions))
	}
	var detail struct {
		Session   store.SessionDetail `json:"session"`
		Timeline  timelineChart       `json:"timeline"`
		Subagents []store.Subagent    `json:"subagents"`
		Tools     []store.ToolUsage   `json:"tools"`
	}
	getJSON(t, srv, "/api/v1/sessions/"+alphaSession, &detail)
	if detail.Session.ID != alphaSession || len(detail.Subagents) != 1 || len(detail.Tools) != 4 {
		t.Errorf("session json: %+v", detail)
	}
	if detail.Timeline.BucketMinutes != 1 || len(detail.Timeline.Buckets) != 11 { // 10:00 → 10:10 inclusive
		t.Errorf("timeline: %d-min buckets ×%d", detail.Timeline.BucketMinutes, len(detail.Timeline.Buckets))
	}
	resp, err := http.Get(srv.URL + "/api/v1/sessions/not-a-uuid")
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("bad id: %v %d", err, resp.StatusCode)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestBuildTimeline(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	ev := func(off time.Duration, role, kind string, side bool, out, tools int64) store.TimelineEvent {
		return store.TimelineEvent{TS: base.Add(off), Role: role, Kind: kind, Sidechain: side, OutputTokens: out, ToolCalls: tools}
	}
	events := []store.TimelineEvent{
		ev(0, "user", "prompt", false, 0, 0),
		ev(30*time.Second, "assistant", "response", false, 100, 1),
		ev(4*time.Minute, "user", "tool_result", false, 0, 0),   // not a prompt
		ev(5*time.Minute, "assistant", "response", true, 30, 1), // subagent
		ev(9*time.Minute, "assistant", "response", false, 20, 2),
	}
	tl := buildTimeline(events)
	if tl.BucketMinutes != 1 || len(tl.Buckets) != 10 {
		t.Fatalf("buckets: %d × %d min", len(tl.Buckets), tl.BucketMinutes)
	}
	b0, b5, b9 := tl.Buckets[0], tl.Buckets[5], tl.Buckets[9]
	if b0.Prompts != 1 || b0.Replies != 1 || b0.Output != 100 || b0.Tools != 1 {
		t.Errorf("bucket 0 = %+v", b0)
	}
	if b5.Subagent != 1 || b5.Replies != 0 || b5.Output != 30 {
		t.Errorf("bucket 5 = %+v", b5)
	}
	if b9.Replies != 1 || b9.Tools != 2 || tl.Buckets[4].Prompts != 0 {
		t.Errorf("bucket 9 = %+v, bucket 4 = %+v", b9, tl.Buckets[4])
	}
	// Width adapts to span.
	long := []store.TimelineEvent{ev(0, "user", "prompt", false, 0, 0), ev(50*time.Hour, "assistant", "response", false, 1, 0)}
	if got := buildTimeline(long); got.BucketMinutes != 60 || len(got.Buckets) > 52 {
		t.Errorf("50h span → %d-min buckets ×%d, want 60-min", got.BucketMinutes, len(got.Buckets))
	}
	// A corrupt far-past timestamp must not explode the bucket count.
	corrupt := []store.TimelineEvent{ev(-56*365*24*time.Hour, "user", "prompt", false, 0, 0), ev(0, "assistant", "response", false, 1, 0)}
	if got := buildTimeline(corrupt); len(got.Buckets) > 400 {
		t.Errorf("56-year span produced %d buckets", len(got.Buckets))
	}
	// Out-of-order input still finds the true span.
	rev := []store.TimelineEvent{events[4], events[0]}
	if got := buildTimeline(rev); len(got.Buckets) != 10 {
		t.Errorf("reversed events → %d buckets, want 10", len(got.Buckets))
	}
	if got := buildTimeline(nil); got.Buckets != nil {
		t.Errorf("empty events should give no buckets: %+v", got)
	}
	if b, _ := json.Marshal(tl); !strings.Contains(string(b), `"bucket_minutes":1`) {
		t.Errorf("json shape: %s", b)
	}
}

func TestPageURLRoundTrip(t *testing.T) {
	q := sessionsQuery{Project: 7, Model: "claude-opus-5", Range: "30", Q: "a&b=c d%"}
	u, err := url.Parse(q.pageURL(3))
	if err != nil {
		t.Fatal(err)
	}
	v := u.Query()
	if u.Path != "/sessions" || v.Get("project") != "7" || v.Get("model") != "claude-opus-5" || v.Get("range") != "30" || v.Get("q") != "a&b=c d%" || v.Get("page") != "3" {
		t.Errorf("pageURL round-trip lost data: %s → %v", q.pageURL(3), v)
	}
	if got := (sessionsQuery{Range: "all"}).pageURL(1); got != "/sessions" {
		t.Errorf("empty filter → %q", got)
	}
	// Rendered into the page, the href must survive html/template intact.
	srv := testServer(t)
	html := getHTML(t, srv, "/sessions?project=1&model=claude-opus-5&q=x", http.StatusOK)
	if strings.Contains(html, "%3d") || strings.Contains(html, "%26") {
		t.Error("query separators were percent-encoded inside an href")
	}
}

func TestLooksLikeSessionID(t *testing.T) {
	for id, want := range map[string]bool{
		alphaSession: true, strings.ToUpper(alphaSession): true,
		"": false, "abc": false, "aaaaaaaa-0000-4000-8000-00000000000g": false,
		"aaaaaaaa00000400080000000000000001": false, "../../etc/passwd/xx/yyyyyyyyyyyy": false,
	} {
		if got := looksLikeSessionID(id); got != want {
			t.Errorf("looksLikeSessionID(%q) = %v, want %v", id, got, want)
		}
	}
}
