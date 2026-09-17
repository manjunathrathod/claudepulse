package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestSettingsPage(t *testing.T) {
	srv := testServer(t)
	html := getHTML(t, srv, "/settings", http.StatusOK)
	for _, want := range []string{
		`class="kpi-value">opus<`, `class="kpi-value">auto<`,
		`&#34;theme&#34;: &#34;dark&#34;`, // pretty-printed, escaped JSON
		"Per-project setup", "no project config", `href="/settings?project=`,
		`class="help"`, // hover descriptions present
	} {
		if !strings.Contains(html, want) {
			t.Errorf("settings missing %q", want)
		}
	}
	sel := getHTML(t, srv, "/settings?project=1#project-settings", http.StatusOK)
	if !strings.Contains(sel, `id="project-settings"`) || !strings.Contains(sel, "no .claude/settings.json") {
		t.Error("selected project block missing")
	}
	getHTML(t, srv, "/settings?project=999", http.StatusOK) // unknown id: page still renders
}

func TestSkillsPage(t *testing.T) {
	srv := testServer(t)
	html := getHTML(t, srv, "/skills", http.StatusOK)
	for _, want := range []string{
		"skill-one", "A synced fixture skill", `class="chip purple">synced</span>`,
		"claude-plugins-official", "anthropics/claude-plugins-official", "0 plugins",
		"fixture-plugin", "v1.2.3", `class="chip">user</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("skills missing %q", want)
		}
	}
}

func TestPlansAndHistoryPage(t *testing.T) {
	srv := testServer(t)
	html := getHTML(t, srv, "/plans", http.StatusOK)
	for _, want := range []string{
		"Fixture Plan Title", "test-plan.md", "3 prompts", "page 1 of 1",
		"first prompt", "third prompt", "beta prompt", // history rows (truncated display)
		`href="/sessions/aaaaaaaa-0000-4000-8000-000000000001"`, // linked to indexed session
	} {
		if !strings.Contains(html, want) {
			t.Errorf("plans missing %q", want)
		}
	}
	q := getHTML(t, srv, "/plans?q=beta", http.StatusOK)
	if strings.Contains(q, "first prompt") || !strings.Contains(q, "beta prompt") || !strings.Contains(q, "1 prompts") {
		t.Error("history search failed")
	}
	if none := getHTML(t, srv, "/plans?q=%25", http.StatusOK); !strings.Contains(none, "No prompts match") {
		t.Error("literal % search should match nothing")
	}
	hostile := getHTML(t, srv, "/plans?q=%3Cimg%20src%3Dx%3E", http.StatusOK)
	if strings.Contains(hostile, "<img src=x>") {
		t.Error("search text rendered unescaped")
	}
	getHTML(t, srv, "/plans?page=7", http.StatusOK)
}

func TestSystemPage(t *testing.T) {
	srv := testServer(t)
	html := getHTML(t, srv, "/system", http.StatusOK)
	for _, want := range []string{
		"v2.1.274", "2.1.273 → 2.1.274", `class="chip ok">ok</span>`,
		"Index tables", "history_entries", "Disk usage", "projects",
		"alpha-1", // live session
		"Configuration", "Scan interval",
		`id="account"`, "fixture.user@example.com", "stripe_subscription", "Subscribed since",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("system missing %q", want)
		}
	}
	if strings.Contains(html, ".credentials.json") {
		t.Error("credentials file must not be listed in disk usage")
	}
}

func TestHistoryDisplayIsTruncatedAndEscaped(t *testing.T) {
	// The store keeps at most historyDisplayRunes of each prompt; the page
	// must never show more than that (guards the "no raw transcripts" rule).
	srv := testServer(t)
	html := getHTML(t, srv, "/plans", http.StatusOK)
	if strings.Contains(html, "pastedContents") {
		t.Error("history rendered a raw field")
	}
}

func TestSummarizeSettings(t *testing.T) {
	s := summarizeSettings(`{"model":"opus","theme":"dark","permissions":{"defaultMode":"auto","allow":["a","b"],"deny":["c"]},
		"hooks":{"PostToolUse":[],"Stop":[]},"env":{"A":"[redacted]"},"enabledPlugins":{"x@m":true,"y@m":false}}`)
	if s.Model != "opus" || s.DefaultMode != "auto" || s.Allow != 2 || s.Deny != 1 || s.Ask != 0 {
		t.Errorf("summary = %+v", s)
	}
	if len(s.Hooks) != 2 || s.Hooks[0] != "PostToolUse" || len(s.EnvKeys) != 1 || len(s.Plugins) != 1 || s.Plugins[0] != "x@m" {
		t.Errorf("summary lists = %+v", s)
	}
	if len(s.Keys) != 6 {
		t.Errorf("keys = %v", s.Keys)
	}
	if bad := summarizeSettings("{nope"); bad.Keys != nil {
		t.Error("invalid JSON should yield an empty summary")
	}
	if prettyJSON(`{"a":1}`) != "{\n  \"a\": 1\n}" || prettyJSON("{bad") != "{bad" {
		t.Error("prettyJSON")
	}
}
