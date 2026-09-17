// Package web serves the dashboard pages and the /api/v1 JSON endpoints.
// Handlers are read-only; all SQL lives in the store package.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"claudepulse/internal/indexer"
	"claudepulse/internal/store"
)

// Server wires the mux to the store and indexer.
type Server struct {
	st       *store.Store
	ix       *indexer.Indexer
	log      *slog.Logger
	mux      *http.ServeMux
	pages    pages
	partials *template.Template
	addr     string
	info     Info
}

// goVersion is recorded at build time for the System page.
var goVersion = runtime.Version()

// New builds the router.
func New(st *store.Store, ix *indexer.Indexer, log *slog.Logger) *Server {
	s := &Server{st: st, ix: ix, log: log, mux: http.NewServeMux(), pages: loadPages(), partials: loadPartials()}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/summary", s.handleSummary)
	s.mux.HandleFunc("GET /api/v1/daily", s.handleDaily)
	s.mux.HandleFunc("GET /api/v1/live", s.handleLive)
	s.mux.HandleFunc("GET /api/v1/system", s.handleSystem)
	s.mux.HandleFunc("GET /api/v1/sessions", s.handleSessionsJSON)
	s.mux.HandleFunc("GET /api/v1/sessions/{id}", s.handleSessionJSON)

	s.mux.HandleFunc("GET /{$}", s.handleDashboard)
	s.mux.HandleFunc("GET /projects", s.handleProjects)
	s.mux.HandleFunc("GET /projects/{id}", s.handleProject)
	s.mux.HandleFunc("GET /sessions", s.handleSessions)
	s.mux.HandleFunc("GET /sessions/{id}", s.handleSession)
	s.mux.HandleFunc("GET /settings", s.handleSettings)
	s.mux.HandleFunc("GET /skills", s.handleSkills)
	s.mux.HandleFunc("GET /plans", s.handlePlans)
	s.mux.HandleFunc("GET /system", s.handleSystemPage)
	s.mux.HandleFunc("GET /partials/live", s.handleLivePartial)
	s.mux.HandleFunc("GET /partials/index", s.handleIndexPartial)
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServer(staticFS()))))
	s.mux.HandleFunc("/", s.notFound)
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

// Handler returns the root handler (with logging middleware).
func (s *Server) Handler() http.Handler { return s.logging(s.mux) }

// ListenAndServe binds addr (loopback only) and serves until ctx is done.
// A busy port is a hard error — the app must never silently pick another.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bad listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("listen address %q must use a literal loopback IP such as 127.0.0.1 (hostnames are not accepted)", addr)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("refusing to listen on non-loopback address %q", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s (is another claudepulse running?): %w", addr, err)
	}
	s.addr = addr
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	s.log.Info("listening", "url", "http://"+addr)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "took", time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		s.log.Warn("encode response", "err", err)
	}
}

// fail logs the real error and returns a generic body (paths and SQL details
// stay in the log).
func (s *Server) fail(w http.ResponseWriter, err error) {
	s.log.Error("handler error", "err", err)
	s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error; see server log"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	st := s.ix.Status(r.Context())
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":           "ok",
		"indexed_sessions": st.TotalSessions,
		"scanning":         st.Scanning,
		"last_scan":        st,
	})
}

// Summary is the dashboard payload.
type Summary struct {
	Totals     store.Totals        `json:"totals"`
	Models     []store.ModelUsage  `json:"models"`
	Daily      []store.DailyUsage  `json:"daily"`
	Hours      [24]int64           `json:"hours_utc"`
	TopTools   []store.ToolUsage   `json:"top_tools"`
	Live       []store.LiveSession `json:"live_sessions"`
	StatsCache json.RawMessage     `json:"claude_stats_cache,omitempty"`
	CLIVersion string              `json:"cli_version,omitempty"`
	Index      indexer.Stats       `json:"index"`
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days := queryInt(r, "days", 30)
	var (
		sum Summary
		err error
	)
	if sum.Totals, err = s.st.GetTotals(ctx); err != nil {
		s.fail(w, err)
		return
	}
	if sum.Models, err = s.st.GetModelUsage(ctx); err != nil {
		s.fail(w, err)
		return
	}
	if sum.Daily, err = s.st.GetDailyUsage(ctx, days); err != nil {
		s.fail(w, err)
		return
	}
	if sum.Hours, err = s.st.GetHourCounts(ctx); err != nil {
		s.fail(w, err)
		return
	}
	if sum.TopTools, err = s.st.GetToolUsage(ctx, 10); err != nil {
		s.fail(w, err)
		return
	}
	if sum.Live, err = s.st.GetLiveSessions(ctx); err != nil {
		s.fail(w, err)
		return
	}
	if raw, err := s.st.GetMeta(ctx, "stats_cache"); err == nil && raw != "" {
		sum.StatsCache = json.RawMessage(raw)
	}
	sum.CLIVersion, _ = s.st.GetMeta(ctx, "cli_version")
	sum.Index = s.ix.Status(ctx)
	if sum.Models == nil {
		sum.Models = []store.ModelUsage{}
	}
	if sum.Daily == nil {
		sum.Daily = []store.DailyUsage{}
	}
	if sum.TopTools == nil {
		sum.TopTools = []store.ToolUsage{}
	}
	if sum.Live == nil {
		sum.Live = []store.LiveSession{}
	}
	s.writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleDaily(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.GetDailyUsage(r.Context(), queryInt(r, "days", 30))
	if err != nil {
		s.fail(w, err)
		return
	}
	if rows == nil {
		rows = []store.DailyUsage{}
	}
	s.writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.GetLiveSessions(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if rows == nil {
		rows = []store.LiveSession{}
	}
	s.writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dirs, err := s.st.GetDirStats(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := map[string]any{"dir_stats": dirs, "index": s.ix.Status(ctx)}
	for _, k := range []string{"cli_version", "last_cleanup", "claude_dir"} {
		v, _ := s.st.GetMeta(ctx, k)
		out[k] = v
	}
	if raw, _ := s.st.GetMeta(ctx, "last_update"); raw != "" {
		out["last_update"] = json.RawMessage(raw)
	}
	s.writeJSON(w, http.StatusOK, out)
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}
