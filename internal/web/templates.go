package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"path"
	"strings"
	"time"

	"claude-monitor/internal/claudedir"
	webassets "claude-monitor/web"
)

var assets = webassets.FS

// assetVersion fingerprints the embedded static files so browsers refetch
// them after an upgrade even with a long Cache-Control max-age.
var assetVersion = func() string {
	h := sha256.New()
	_ = fs.WalkDir(assets, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := assets.ReadFile(p)
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

// assetURL renders /static/<path>?v=<fingerprint>.
func assetURL(p string) string { return "/static/" + strings.TrimPrefix(p, "/") + "?v=" + assetVersion }

// staticFS serves web/static at /static/.
func staticFS() http.FileSystem {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}

// pages maps a page name to its parsed template set (base + partials + page).
type pages map[string]*template.Template

func loadPages() pages {
	names, err := fs.Glob(assets, "templates/*.html")
	if err != nil {
		panic(err)
	}
	out := pages{}
	for _, n := range names {
		base := path.Base(n)
		if base == "base.html" {
			continue
		}
		t := template.New("base.html").Funcs(funcMap)
		t = template.Must(t.ParseFS(assets, "templates/base.html", "templates/partials/*.html", n))
		out[strings.TrimSuffix(base, ".html")] = t
	}
	return out
}

// partial parses a single partial for htmx fragment responses.
func loadPartials() *template.Template {
	return template.Must(template.New("partials").Funcs(funcMap).ParseFS(assets, "templates/partials/*.html"))
}

// ---- series colours (validated palette; see docs/PLAN.md and the dataviz skill) ----

// modelSeries maps a model id to a fixed categorical slot so colour follows
// the entity, never its rank. Unknown models fold into the neutral "other".
var modelSeries = []struct {
	match string
	color string
}{
	{"opus", "#3987e5"},   // blue
	{"sonnet", "#d55181"}, // magenta
	{"haiku", "#c98500"},  // amber
}

const otherSeriesColor = "#6b7186"

func seriesColor(model string) string {
	m := strings.ToLower(model)
	for _, s := range modelSeries {
		if strings.Contains(m, s.match) {
			return s.color
		}
	}
	return otherSeriesColor
}

// modelLabel turns "claude-opus-5" into "Opus 5"; leaves unknown ids alone.
func modelLabel(model string) string {
	m := strings.TrimPrefix(model, "claude-")
	if m == "" || strings.HasPrefix(m, "<") {
		return model
	}
	parts := strings.Split(m, "-")
	for i, p := range parts {
		if p != "" && p[0] >= 'a' && p[0] <= 'z' {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	// Drop a trailing date stamp like 20251001.
	if n := len(parts); n > 1 && len(parts[n-1]) == 8 && isDigits(parts[n-1]) {
		parts = parts[:n-1]
	}
	return strings.Join(parts, " ")
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// ---- template funcs ----

var funcMap = template.FuncMap{
	"compact":     compact,
	"comma":       comma,
	"duration":    duration,
	"ago":         ago,
	"date":        dateOnly,
	"datetime":    dateTime,
	"basename":    basenameAny,
	"pctNum":      pctNum,
	"modelLabel":  modelLabel,
	"seriesColor": seriesColor,
	"json":        toJSON,
	"pct":         pct,
	"bytes":       humanBytes,
	"split":       strings.Split,
	"int64":       func(i int) int64 { return int64(i) },
	"add":         func(a, b int) int { return a + b },
	"asset":       assetURL,
	"planLabel":   claudedir.PlanLabel,
	"initial":     initial,
	"truncate":    truncate,
}

// compact renders 1284 → "1,284", 12900 → "12.9K", 4200000 → "4.2M".
func compact(n int64) string {
	f := float64(n)
	switch {
	case n < 10_000:
		return comma(n)
	case n < 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", f/1e3)) + "K"
	case n < 1_000_000_000:
		return trimZero(fmt.Sprintf("%.1f", f/1e6)) + "M"
	default:
		return trimZero(fmt.Sprintf("%.2f", f/1e9)) + "B"
	}
}

func trimZero(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// comma renders 1234567 → "1,234,567".
func comma(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// duration renders milliseconds → "3h 42m", "12m", "45s".
func duration(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	d := time.Duration(ms) * time.Millisecond
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// parseTS accepts the stored RFC3339 text, a time.Time, or any (from JSON
// maps) and reports whether a usable time came out.
func parseTS(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, !x.IsZero()
	case string:
		if x == "" {
			return time.Time{}, false
		}
		t, err := time.Parse(time.RFC3339Nano, x)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	default:
		return time.Time{}, false
	}
}

// ago renders a stored timestamp as a relative phrase; future times read
// "in 2h", past ones "2h ago".
func ago(s any) string {
	t, ok := parseTS(s)
	if !ok {
		return "—"
	}
	d := time.Since(t)
	future := d < 0
	if future {
		d = -d
	}
	var rel string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		rel = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		rel = fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		rel = fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return t.Local().Format("2 Jan 2006")
	}
	if future {
		return "in " + rel
	}
	return rel + " ago"
}

func dateOnly(s any) string {
	t, ok := parseTS(s)
	if !ok {
		return "—"
	}
	return t.Local().Format("2 Jan 2006")
}

func dateTime(s any) string {
	t, ok := parseTS(s)
	if !ok {
		return "—"
	}
	return t.Local().Format("2 Jan 2006 15:04")
}

func toJSON(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	// Keep </script> from terminating the block.
	return template.JS(strings.ReplaceAll(string(b), "</", "<\\/"))
}

func pct(part, whole int64) string {
	if whole <= 0 {
		return "0%"
	}
	p := float64(part) / float64(whole) * 100
	if p < 1 && p > 0 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", math.Round(p))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func truncate(s string, n int) string {
	r := []rune(s)
	if n <= 1 || len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// pctNum is pct as a bare integer for CSS widths/heights.
func pctNum(part, whole int64) int {
	if whole <= 0 || part <= 0 {
		return 0
	}
	return int(math.Round(float64(part) / float64(whole) * 100))
}

// initial returns the first letter of s, upper-cased, for avatar badges.
func initial(s string) string {
	for _, r := range s {
		return strings.ToUpper(string(r))
	}
	return "?"
}
