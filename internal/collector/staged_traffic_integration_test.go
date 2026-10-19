// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/model"
)

// TestZeroTrafficReadIsStoredEndToEnd drives the recorded read of a
// repository without traffic through the whole write path: the
// collection step stages it, the staging writer flushes it, and
// ProcessRepo writes it with UpsertRepoClone and UpsertRepoTraffic.
// A successful read with no traffic must leave rows of 0, never no
// rows: the stored days are the days observed, so a consumer can tell
// a repository nobody visited (rows of 0) from one Aveloxis cannot
// read (no rows) and from a missed read (missing days).
//
// The fixture holds real GitHub responses read on Monday 2026-10-19
// (UTC): 14 zero-filled daily buckets per kind from 2026-10-06 to
// 2026-10-19, and weekly buckets labelled 2026-10-06 (the window start,
// a Tuesday), 2026-10-12 and 2026-10-19. Of those weeks only the one
// from Monday 2026-10-12 to Sunday 2026-10-18 lies entirely inside the
// window. The empty path and referrer lists store no snapshot row.
//
// Gated on AVELOXIS_TEST_DB (scratch DB only).
func TestZeroTrafficReadIsStoredEndToEnd(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := db.NewPostgresStore(ctx, dsn, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := db.RunMigrations(ctx, store, logger); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	// An independent connection checks the stored rows without trusting
	// the store under test.
	raw, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(raw.Close) // not defer: deferred calls run before the data cleanups (SR-9)

	const slug = "_avtraffic_zero_e2e"
	cleanup := func() {
		bg := context.Background() // never t.Context() in a cleanup (TEST-4)
		of := `(SELECT repo_id FROM aveloxis_data.repos WHERE repo_git ILIKE '%` + slug + `%')`
		for _, sql := range []string{
			`DELETE FROM aveloxis_data.repo_clones WHERE repo_id IN ` + of,
			`DELETE FROM aveloxis_data.repo_views WHERE repo_id IN ` + of,
			`DELETE FROM aveloxis_data.repo_traffic_paths WHERE repo_id IN ` + of,
			`DELETE FROM aveloxis_data.repo_traffic_referrers WHERE repo_id IN ` + of,
			`DELETE FROM aveloxis_data.repo_traffic_windows WHERE repo_id IN ` + of,
			`DELETE FROM aveloxis_data.repo_traffic_weeks WHERE repo_id IN ` + of,
			`DELETE FROM aveloxis_ops.staging WHERE repo_id IN ` + of,
			// ProcessRepo records the repository's collection status.
			`DELETE FROM aveloxis_ops.collection_status WHERE repo_id IN ` + of,
			`DELETE FROM aveloxis_data.repos WHERE repo_git ILIKE '%` + slug + `%'`,
		} {
			if _, err := raw.Exec(bg, sql); err != nil {
				t.Errorf("traffic fixture cleanup: %v", err)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	repoID, err := store.UpsertRepo(ctx, &model.Repo{
		Platform: model.PlatformGitHub,
		GitURL:   "https://github.com/" + slug + "/Repo",
		Owner:    slug, Name: "Repo",
	})
	if err != nil {
		t.Fatal(err)
	}

	traffic, clones := zeroTrafficFixture(t)
	client := &trafficFakeClient{plat: model.PlatformGitHub, clones: clones, traffic: traffic}
	sc := &StagedCollector{client: client, logger: logger}
	sw := db.NewStagingWriter(store, repoID, int16(model.PlatformGitHub), logger)
	result := &CollectResult{}
	sc.collectTraffic(ctx, sw, slug, "Repo", time.Date(2026, time.October, 19, 16, 45, 0, 0, time.UTC), result)
	if len(result.Errors) != 0 {
		t.Fatalf("collecting the zero-traffic read recorded errors: %v", result.Errors)
	}
	if err := sw.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	proc := NewProcessor(store, logger)
	if err := proc.ProcessRepo(ctx, repoID, int16(model.PlatformGitHub)); err != nil {
		t.Fatalf("ProcessRepo: %v", err)
	}
	if proc.errors != 0 {
		t.Errorf("ProcessRepo counted %d row processing failures, want 0", proc.errors)
	}

	rowsOf := func(query string) []string {
		t.Helper()
		rows, err := raw.Query(ctx, query, repoID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	check := func(what string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got  %q\n want %q", what, got, want)
		}
	}

	var zeroDays []string
	for d := 6; d <= 19; d++ {
		zeroDays = append(zeroDays, fmt.Sprintf("2026-10-%02d 0/0", d))
	}
	check("daily views", rowsOf(`
		SELECT to_char(view_timestamp AT TIME ZONE 'UTC', 'YYYY-MM-DD') || ' ' || total_views || '/' || unique_views
		FROM aveloxis_data.repo_views WHERE repo_id = $1 ORDER BY view_timestamp`), zeroDays)
	check("daily clones", rowsOf(`
		SELECT to_char(clone_timestamp AT TIME ZONE 'UTC', 'YYYY-MM-DD') || ' ' || total_clones || '/' || unique_clones
		FROM aveloxis_data.repo_clones WHERE repo_id = $1 ORDER BY clone_timestamp`), zeroDays)
	check("window rows", rowsOf(`
		SELECT traffic_kind || ' ' || window_start::text || '..' || window_end::text || ' ' || uniques
		FROM aveloxis_data.repo_traffic_windows WHERE repo_id = $1 ORDER BY 1`),
		[]string{"clones 2026-10-06..2026-10-19 0", "views 2026-10-06..2026-10-19 0"})
	check("weekly rows", rowsOf(`
		SELECT traffic_kind || ' ' || week_start::text || ' ' || uniques
		FROM aveloxis_data.repo_traffic_weeks WHERE repo_id = $1 ORDER BY 1`),
		[]string{"clones 2026-10-12 0", "views 2026-10-12 0"})
	check("path snapshot", rowsOf(`
		SELECT path FROM aveloxis_data.repo_traffic_paths WHERE repo_id = $1 ORDER BY 1`), nil)
	check("referrer snapshot", rowsOf(`
		SELECT referrer FROM aveloxis_data.repo_traffic_referrers WHERE repo_id = $1 ORDER BY 1`), nil)
	check("unprocessed staging rows", rowsOf(`
		SELECT entity_type FROM aveloxis_ops.staging WHERE repo_id = $1 AND NOT processed ORDER BY 1`), nil)
}
