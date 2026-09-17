package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"claudepulse/internal/store"
)

func getHTML(t *testing.T, srv *httptest.Server, path string, wantStatus int) string {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s → %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET %s content-type = %q", path, ct)
	}
	s := string(body)
	if strings.Contains(s, "MUST-NEVER-BE-READ") {
		t.Fatalf("GET %s leaked a secret", path)
	}
	// Transcript text must never be rendered; the fixture's prompts all contain
	// "scrubbed". The one sanctioned exception is the prompt-history list on
	// /plans, which shows a truncated first line of each prompt by design.
	if !strings.HasPrefix(path, "/plans") && strings.Contains(s, "prompt scrubbed") {
		t.Fatalf("GET %s rendered transcript content", path)
	}
	return s
}

func TestDashboardPage(t *testing.T) {
	srv := testServer(t)
	html := getHTML(t, srv, "/?days=0", http.StatusOK)
	for _, want := range []string{
		`class="kpi-value" title="deduped per API message">270<`,    // output tokens
		"Alpha fixture session &lt;b&gt;&amp;&#34;x&#34;&lt;/b&gt;", // ai-title, escaped
		"Beta fixture session", // summary fallback
		`id="daily-data"`,
		`"label":"Opus 5","color":"#3987e5"`,
		`"label":"Sonnet 5","color":"#d55181"`,
		"alpha-1", // live session name
		`class="status-pill`,
		`class="account-name">fixture.user<`, `class="chip plan">Pro</span>`, "Plan usage", "5-hour window", "29% · resets", "reset · was 4%",
		`/projects/`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	// The raw title must never appear unescaped anywhere on the page.
	if strings.Contains(html, `<b>&"x"</b>`) {
		t.Error("session title rendered unescaped")
	}
	// Window filter: the fixture is dated 2026-09-01, so a 30-day window may be
	// empty depending on "now"; either way the page must render.
	getHTML(t, srv, "/", http.StatusOK)
	getHTML(t, srv, "/?days=banana", http.StatusOK)
}

func TestErrorPageIsHTML(t *testing.T) {
	srv := testServer(t)
	body := getHTML(t, srv, "/projects/does-not-exist", http.StatusNotFound)
	if !strings.Contains(body, `class="err-code mono">404<`) || !strings.Contains(body, "/projects/does-not-exist") {
		t.Errorf("404 page = %s", body[:200])
	}
}

func TestProjectsPages(t *testing.T) {
	srv := testServer(t)
	list := getHTML(t, srv, "/projects", http.StatusOK)
	for _, want := range []string{`fixture\alpha`, "orphaned", "memory 2", `href="/projects/`} {
		if !strings.Contains(list, want) {
			t.Errorf("projects list missing %q", want)
		}
	}
	// Follow the first project link.
	i := strings.Index(list, `href="/projects/`)
	href := list[i+len(`href="`):]
	href = href[:strings.Index(href, `"`)]
	detail := getHTML(t, srv, href, http.StatusOK)
	if !strings.Contains(detail, "Sessions") || !strings.Contains(detail, "Top tools") {
		t.Errorf("project detail missing sections")
	}
	getHTML(t, srv, "/projects/999999", http.StatusNotFound)
	getHTML(t, srv, "/projects/abc", http.StatusNotFound)
	getHTML(t, srv, "/nope", http.StatusNotFound)
}

func TestPartialsAndStatic(t *testing.T) {
	srv := testServer(t)
	live := getHTML(t, srv, "/partials/live", http.StatusOK)
	if !strings.Contains(live, `id="live-sessions"`) || !strings.Contains(live, "alpha-1") {
		t.Errorf("live partial = %s", live)
	}
	idx := getHTML(t, srv, "/partials/index", http.StatusOK)
	if !strings.Contains(idx, `id="index-status"`) || !strings.Contains(idx, "2 sessions") {
		t.Errorf("index partial = %s", idx)
	}
	for _, p := range []string{"/static/app.css", "/static/app.js", "/static/vendor/htmx.min.js", "/static/vendor/chart.umd.min.js"} {
		resp, err := http.Get(srv.URL + p + "?v=" + assetVersion)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", p, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestFormatting(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{{0, "0"}, {999, "999"}, {9999, "9,999"}, {12900, "12.9K"}, {1_000_000, "1M"}, {4_200_000, "4.2M"}, {2_500_000_000, "2.5B"}}
	for _, c := range cases {
		if got := compact(c.in); got != c.want {
			t.Errorf("compact(%d) = %q, want %q", c.in, got, c.want)
		}
	}
	if comma(1234567) != "1,234,567" || comma(-1000) != "-1,000" {
		t.Errorf("comma: %s %s", comma(1234567), comma(-1000))
	}
	if duration(0) != "—" || duration(45_000) != "45s" || duration(601_000) != "10m 01s" || duration(13_320_000) != "3h 42m" {
		t.Errorf("duration: %s %s %s", duration(45_000), duration(601_000), duration(13_320_000))
	}
	if modelLabel("claude-opus-5") != "Opus 5" || modelLabel("claude-haiku-4-5-20251001") != "Haiku 4 5" || modelLabel("<synthetic>") != "<synthetic>" {
		t.Errorf("modelLabel: %q %q %q", modelLabel("claude-opus-5"), modelLabel("claude-haiku-4-5-20251001"), modelLabel("<synthetic>"))
	}
	if seriesColor("claude-opus-5") != "#3987e5" || seriesColor("claude-sonnet-5") != "#d55181" || seriesColor("weird") != otherSeriesColor {
		t.Error("seriesColor mapping changed — re-validate the palette with the dataviz validator")
	}
	if basenameAny(`C:\Users\me\Desktop\App`) != "App" || basenameAny("/home/me/app/") != "app" || basenameAny("E:\\") != "E:" {
		t.Errorf("basenameAny: %q %q %q", basenameAny(`C:\Users\me\Desktop\App`), basenameAny("/home/me/app/"), basenameAny("E:\\"))
	}
	if ago(time.Now().Add(-3*time.Hour).UTC().Format(time.RFC3339Nano)) != "3h ago" || ago(time.Now().Add(2*time.Hour+time.Minute)) != "in 2h" || ago(time.Now().Add(-10*time.Second)) != "just now" {
		t.Errorf("ago: %q %q", ago(time.Now().Add(-3*time.Hour)), ago(time.Now().Add(2*time.Hour+time.Minute)))
	}
	if humanBytes(1536) != "1.5 KB" || humanBytes(157*1024*1024) != "157.0 MB" {
		t.Errorf("humanBytes: %s %s", humanBytes(1536), humanBytes(157*1024*1024))
	}
	if pctNum(1, 3) != 33 || pctNum(0, 0) != 0 || pct(1, 300) != "<1%" {
		t.Errorf("pct helpers: %d %d %s", pctNum(1, 3), pctNum(0, 0), pct(1, 300))
	}
	if out := string(toJSON("</script>")); strings.Contains(out, "</") {
		t.Errorf("toJSON must escape </: %s", out)
	}
}

func TestBuildDailyFillsGapsAndDropsEmptySeries(t *testing.T) {
	rows := []store.DailyUsage{
		{Date: "2026-09-01", Model: "claude-opus-5", OutputTokens: 10},
		{Date: "2026-09-03", Model: "claude-sonnet-5", OutputTokens: 5},
		{Date: "2026-09-03", Model: "<synthetic>", OutputTokens: 0},
	}
	d := buildDaily(rows, 0)
	if len(d.Datasets) != 2 || d.Datasets[0].Label != "Opus 5" || d.Datasets[1].Label != "Sonnet 5" {
		t.Fatalf("datasets = %+v", d.Datasets)
	}
	if len(d.Labels) < 3 || d.Labels[0] != "2026-09-01" || d.Labels[1] != "2026-09-02" {
		t.Errorf("labels not gap-filled: %v", d.Labels[:3])
	}
	if d.Datasets[0].Data[0] != 10 || d.Datasets[0].Data[1] != 0 || d.Datasets[1].Data[2] != 5 {
		t.Errorf("data misaligned: %v / %v", d.Datasets[0].Data[:3], d.Datasets[1].Data[:3])
	}
}

func TestBuildDailyIgnoresCorruptDatesAndHonoursWindow(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	rows := []store.DailyUsage{
		{Date: "1970-01-01", Model: "claude-opus-5", OutputTokens: 1}, // corrupt epoch
		{Date: "0001-01-02", Model: "claude-opus-5", OutputTokens: 1}, // zero time
		{Date: "not-a-date", Model: "claude-opus-5", OutputTokens: 1},
		{Date: "2999-01-01", Model: "claude-opus-5", OutputTokens: 1}, // future
		{Date: today, Model: "claude-opus-5", OutputTokens: 7},
	}
	d := buildDaily(rows, 0)
	if len(d.Labels) != 1 || d.Labels[0] != today || len(d.Datasets) != 1 || d.Datasets[0].Data[0] != 7 {
		t.Errorf("corrupt dates leaked into the axis: labels=%d datasets=%+v", len(d.Labels), d.Datasets)
	}
	// A 30-day window has 31 labels (today plus the 30 days before) — the same
	// boundary the store uses for date >= now-30.
	if w := buildDaily(rows, 30); len(w.Labels) != 31 || w.Labels[30] != today {
		t.Errorf("30-day window: %d labels, last=%q", len(w.Labels), w.Labels[len(w.Labels)-1])
	}
	if only := buildDaily(rows[:4], 0); len(only.Labels) != 0 || len(only.Datasets) != 0 {
		t.Errorf("all-corrupt input should yield an empty chart, got %+v", only)
	}
}
