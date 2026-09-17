package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// seed inserts two projects and three sessions with enough data for the
// filter queries, without going through the indexer.
func seed(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	p1, _ := st.UpsertProject(ctx, "P--one", "/p/one")
	p2, _ := st.UpsertProject(ctx, "P--two", "/p/two")
	type sess struct {
		id    string
		pid   int64
		title string
		model string
		at    time.Time
	}
	now := time.Now().UTC()
	for _, s := range []sess{
		{"11111111-0000-4000-8000-000000000001", p1, "Refactor the parser", "claude-opus-5", now.Add(-2 * time.Hour)},
		{"22222222-0000-4000-8000-000000000002", p1, "Write docs", "claude-sonnet-5", now.AddDate(0, 0, -40)},
		{"33333333-0000-4000-8000-000000000003", p2, "", "claude-opus-5", now.AddDate(0, 0, -10)},
	} {
		err := st.Tx(ctx, func(tx *sql.Tx) error {
			if err := EnsureSession(ctx, tx, s.id, s.pid, "/t/"+s.id+".jsonl"); err != nil {
				return err
			}
			if err := InsertMessage(ctx, tx, MessageRow{ID: "u-" + s.id, SessionID: s.id, Role: "user", Kind: "prompt", TS: s.at, SourcePath: "x"}); err != nil {
				return err
			}
			if err := InsertMessage(ctx, tx, MessageRow{ID: "a-" + s.id, SessionID: s.id, Role: "assistant", Kind: "response",
				Model: s.model, TS: s.at.Add(time.Minute), Output: 100, SourcePath: "x"}); err != nil {
				return err
			}
			if err := InsertToolCall(ctx, tx, ToolCallRow{ID: "t-" + s.id, SessionID: s.id, MessageID: "a-" + s.id, Name: "Bash", TS: s.at, SourcePath: "x"}); err != nil {
				return err
			}
			return UpdateSessionFacts(ctx, tx, s.id, SessionFacts{Title: s.title, CWD: "/p"})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RebuildRollups(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestListSessionsFilters(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	cases := []struct {
		name  string
		f     SessionFilter
		want  int64
		first string
	}{
		{"all", SessionFilter{}, 3, "11111111-0000-4000-8000-000000000001"},
		{"project", SessionFilter{ProjectID: 1}, 2, "11111111-0000-4000-8000-000000000001"},
		{"model", SessionFilter{Model: "sonnet"}, 1, "22222222-0000-4000-8000-000000000002"},
		{"since", SessionFilter{Since: time.Now().UTC().AddDate(0, 0, -30)}, 2, "11111111-0000-4000-8000-000000000001"},
		{"query", SessionFilter{Query: "  DOCS "}, 1, "22222222-0000-4000-8000-000000000002"},
		{"combined", SessionFilter{ProjectID: 1, Model: "opus", Query: "parser"}, 1, "11111111-0000-4000-8000-000000000001"},
		{"none", SessionFilter{Query: "nothing-matches"}, 0, ""},
		{"limit", SessionFilter{Limit: 1, Offset: 1}, 3, "33333333-0000-4000-8000-000000000003"},
	}
	for _, c := range cases {
		rows, total, err := st.ListSessions(ctx, c.f)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if total != c.want {
			t.Errorf("%s: total = %d, want %d", c.name, total, c.want)
		}
		if c.first != "" && (len(rows) == 0 || rows[0].ID != c.first) {
			t.Errorf("%s: first row = %+v, want %s", c.name, rows, c.first)
		}
		if c.name == "limit" && len(rows) != 1 {
			t.Errorf("limit: got %d rows", len(rows))
		}
	}
	// LIKE metacharacters in user input are literal: '%' and '_' match only
	// titles that contain those characters.
	if _, total, _ := st.ListSessions(ctx, SessionFilter{Query: "%"}); total != 0 {
		t.Errorf("literal %% query total = %d, want 0", total)
	}
	if _, total, _ := st.ListSessions(ctx, SessionFilter{Query: "r_f"}); total != 0 {
		t.Errorf("literal _ query total = %d, want 0 (must not match 'ref')", total)
	}
	if _, total, _ := st.ListSessions(ctx, SessionFilter{Model: "%"}); total != 0 {
		t.Errorf("literal %% model total = %d, want 0", total)
	}
}

func TestSessionDetailQueries(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	id := "11111111-0000-4000-8000-000000000001"
	d, ok, err := st.GetSession(ctx, id)
	if err != nil || !ok || d.Title != "Refactor the parser" || d.CWD != "/p" || d.OutputTokens != 100 || d.ToolCalls != 1 {
		t.Errorf("GetSession = %+v ok=%v err=%v", d, ok, err)
	}
	if _, ok, err := st.GetSession(ctx, "99999999-0000-4000-8000-000000000009"); ok || err != nil {
		t.Errorf("missing session: ok=%v err=%v", ok, err)
	}
	ev, err := st.SessionEvents(ctx, id)
	if err != nil || len(ev) != 2 || ev[0].Role != "user" || ev[1].ToolCalls != 1 || ev[1].OutputTokens != 100 {
		t.Errorf("events = %+v err=%v", ev, err)
	}
	tools, _ := st.SessionToolUsage(ctx, id, 0)
	if len(tools) != 1 || tools[0].Name != "Bash" {
		t.Errorf("tools = %+v", tools)
	}
	opts, _ := st.ProjectOptions(ctx)
	if len(opts) != 2 || opts[0].Name != "P--one" || opts[0].Sessions != 2 {
		t.Errorf("project options = %+v", opts)
	}
	models, _ := st.ModelOptions(ctx)
	if len(models) != 2 || models[0] != "claude-opus-5" {
		t.Errorf("model options = %v", models)
	}
	if subs, err := st.SessionSubagents(ctx, id); err != nil || len(subs) != 0 {
		t.Errorf("subagents = %+v err=%v", subs, err)
	}
}
