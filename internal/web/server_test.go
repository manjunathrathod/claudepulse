package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"claudepulse/internal/claudedir"
	"claudepulse/internal/indexer"
	"claudepulse/internal/store"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "claude-home"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ix := indexer.New(claudedir.New(root), st, log)
	if err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, ix, log).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, srv *httptest.Server, path string, v any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s → %d: %s", path, resp.StatusCode, body)
	}
	if strings.Contains(string(body), "MUST-NEVER-BE-READ") {
		t.Fatalf("GET %s leaked a secret", path)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("GET %s: bad JSON: %v\n%s", path, err, body)
	}
}

func TestHealthz(t *testing.T) {
	srv := testServer(t)
	var h struct {
		Status   string `json:"status"`
		Sessions int    `json:"indexed_sessions"`
		Scanning bool   `json:"scanning"`
	}
	getJSON(t, srv, "/healthz", &h)
	if h.Status != "ok" || h.Sessions != 2 || h.Scanning {
		t.Errorf("healthz = %+v", h)
	}
}

func TestSummary(t *testing.T) {
	srv := testServer(t)
	var s Summary
	getJSON(t, srv, "/api/v1/summary?days=0", &s)
	if s.Totals.Sessions != 2 || s.Totals.OutputTokens != 270 || s.Totals.ToolCalls != 4 {
		t.Errorf("totals = %+v", s.Totals)
	}
	if len(s.Models) != 2 || len(s.Daily) != 2 || len(s.TopTools) != 4 || len(s.Live) != 1 {
		t.Errorf("summary shape: models=%d daily=%d tools=%d live=%d", len(s.Models), len(s.Daily), len(s.TopTools), len(s.Live))
	}
	if s.Hours[10] != 2 || s.CLIVersion != "2.1.274" || len(s.StatsCache) == 0 {
		t.Errorf("hours/version/stats: %v %q %d", s.Hours, s.CLIVersion, len(s.StatsCache))
	}
	// Window filter: fixture dates are in the past relative to "now", so a tiny
	// window returns nothing but still a valid (empty) array.
	var narrow Summary
	getJSON(t, srv, "/api/v1/summary?days=1", &narrow)
	if narrow.Daily == nil {
		t.Errorf("daily should be [] not null")
	}
}

func TestSystemAndLive(t *testing.T) {
	srv := testServer(t)
	var sys map[string]any
	getJSON(t, srv, "/api/v1/system", &sys)
	if sys["cli_version"] != "2.1.274" || sys["last_cleanup"] != "2026-09-01T09:30:00.000Z" {
		t.Errorf("system = %+v", sys)
	}
	if dirs, ok := sys["dir_stats"].([]any); !ok || len(dirs) == 0 {
		t.Errorf("dir_stats missing: %+v", sys["dir_stats"])
	}
	var live []store.LiveSession
	getJSON(t, srv, "/api/v1/live", &live)
	if len(live) != 1 || live[0].Name != "alpha-1" {
		t.Errorf("live = %+v", live)
	}
}

func TestRefusesNonLoopback(t *testing.T) {
	s := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, addr := range []string{"0.0.0.0:3333", ":3333", "192.168.1.10:3333", "localhost:3333"} {
		if err := s.ListenAndServe(context.Background(), addr); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("%s: err = %v, want loopback refusal", addr, err)
		}
	}
}

func TestBusyPortFailsLoudly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	err = s.ListenAndServe(context.Background(), ln.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "cannot listen") {
		t.Errorf("busy port: err = %v, want 'cannot listen' error", err)
	}
}
