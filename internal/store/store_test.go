package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	for i := 0; i < 2; i++ {
		st, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		var n int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
			t.Errorf("open #%d: migrations applied = %d err=%v", i, n, err)
		}
		var mode string
		if err := st.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
			t.Errorf("journal_mode = %q err=%v", mode, err)
		}
		st.Close()
	}
}

func TestMetaRoundTrip(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if v, err := st.GetMeta(ctx, "missing"); err != nil || v != "" {
		t.Errorf("missing key: %q %v", v, err)
	}
	st.SetMeta(ctx, "k", "v1")
	st.SetMeta(ctx, "k", "v2")
	if v, _ := st.GetMeta(ctx, "k"); v != "v2" {
		t.Errorf("upsert: %q", v)
	}
}

func TestEmptyStoreQueries(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if tot, err := st.GetTotals(ctx); err != nil || tot.Sessions != 0 {
		t.Errorf("totals on empty store: %+v %v", tot, err)
	}
	if err := st.RebuildRollups(ctx); err != nil {
		t.Errorf("rollups on empty store: %v", err)
	}
	if _, err := st.PruneMissing(ctx, map[string]bool{}); err != nil {
		t.Errorf("prune on empty store: %v", err)
	}
}
