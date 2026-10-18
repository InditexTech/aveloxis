// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// Stage 1b item 4 (eval-4; N19): the Augur metric readers take a
// half-open [lo, hi) window, and the commit metrics count by the UTC day
// of cmt_author_timestamp, like /timeseries and the daily commit table.
// Before 1b they cast the TEXT cmt_author_date to DATE (the author's LOCAL
// day) in SELECT and WHERE, so one row whose date text was empty failed the
// whole query (SQLSTATE 22007), and BETWEEN made the window's end an
// instant inside the next day.
func TestCommitMetricsCountByUTCDayAndSurviveEmptyDateText(t *testing.T) {
	store, ctx := openSR5Store(t)
	suffix := time.Now().UnixNano()
	owner := fmt.Sprintf("avx-win-%d", suffix)
	var repoID int64
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id)
		VALUES ($1, $2, 'w', 1) RETURNING repo_id`, "https://github.com/"+owner+"/w", owner).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.commits WHERE repo_id = $1`, repoID)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.issues WHERE repo_id = $1`, repoID)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.repos WHERE repo_id = $1`, repoID)
	})
	// A commit authored at 23:30 on March 10 in UTC+2: its local date text
	// is 2024-03-10, its instant is 21:30 UTC on March 10. A second commit at
	// 01:30 on March 11 in UTC+2 (23:30 UTC on March 10): local day 11, UTC
	// day 10. A third row with an empty date text and no timestamp (an
	// unparseable author date) must not fail the query.
	for _, c := range []struct {
		hash, dateText string
		ts             any
	}{
		{"h1", "2024-03-10", time.Date(2024, 3, 10, 21, 30, 0, 0, time.UTC)},
		{"h2", "2024-03-11", time.Date(2024, 3, 10, 23, 30, 0, 0, time.UTC)},
		{"h3", "", nil},
	} {
		mustExecRetry(ctx, t, store, `
			INSERT INTO aveloxis_data.commits (repo_id, cmt_commit_hash, cmt_filename, cmt_author_name, cmt_author_email,
				cmt_author_date, cmt_author_timestamp, cmt_added, cmt_removed)
			VALUES ($1, $2, 'f.go', 'a', $3, $4, $5, 3, 1)`, repoID, c.hash, "a-"+c.hash+"@example.com", c.dateText, c.ts)
	}
	day10 := time.Date(2024, 3, 10, 0, 0, 0, 0, time.UTC)
	day11 := day10.AddDate(0, 0, 1)
	day12 := day11.AddDate(0, 0, 1)

	// Both timestamped commits fall on the UTC day of March 10 (N19); the
	// window [10, 11) holds them and [11, 12) holds nothing — and the
	// empty-date row breaks neither.
	rows, err := store.CodeChanges(ctx, repoID, "day", day10, day11)
	if err != nil {
		t.Fatalf("CodeChanges over a repository with an empty author date text: %v", err)
	}
	if len(rows) != 1 || rows[0].Value != 2 || !rows[0].Date.Equal(day10) {
		t.Errorf("CodeChanges [10, 11) = %+v; want one bucket on March 10 (UTC) counting 2 commits", rows)
	}
	rows, err = store.CodeChanges(ctx, repoID, "day", day11, day12)
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != 0 {
		t.Errorf("CodeChanges [11, 12) = %#v; want an empty, non-nil list (D6)", rows)
	}
	// The upper bound is exclusive: a window ending AT the first commit's
	// instant does not include it.
	rows, err = store.CodeChanges(ctx, repoID, "day", day10, time.Date(2024, 3, 10, 21, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("CodeChanges [10, 21:30) = %+v; want nothing (the bound is exclusive)", rows)
	}
	for name, read := range map[string]func() (int, error){
		"Committers": func() (int, error) {
			r, err := store.Committers(ctx, repoID, "day", day10, day11)
			if err != nil || len(r) != 1 {
				return -1, err
			}
			return r[0].Value, nil
		},
		"CodeChangesLines": func() (int, error) {
			r, err := store.CodeChangesLines(ctx, repoID, "day", day10, day11)
			if err != nil || len(r) != 1 {
				return -1, err
			}
			return int(r[0].Added), nil
		},
	} {
		if got, err := read(); err != nil || got != 2 && got != 6 {
			t.Errorf("%s over the empty-date row: %d, %v; want the two timestamped commits (2 committers; 6 added lines)", name, got, err)
		}
	}

	// An issue created at the last instant of March 10 is in a window whose
	// end is the midnight after (a day given inclusively upstream), and not
	// in one that ends at March 10's midnight.
	mustExecRetry(ctx, t, store, `
		INSERT INTO aveloxis_data.issues (repo_id, platform_issue_id, issue_number, issue_title, created_at)
		VALUES ($1, 1, 1, 'late', $2)`, repoID, time.Date(2024, 3, 10, 23, 59, 59, 0, time.UTC))
	in, err := store.IssuesNew(ctx, repoID, "day", day10, day11)
	if err != nil || len(in) != 1 || in[0].Value != 1 {
		t.Errorf("IssuesNew [10, 11) = %+v, %v; want the late issue", in, err)
	}
	out, err := store.IssuesNew(ctx, repoID, "day", day10.AddDate(0, 0, -1), day10)
	if err != nil || out == nil || len(out) != 0 {
		t.Errorf("IssuesNew [9, 10) = %#v, %v; want an empty, non-nil list", out, err)
	}
}

// Review round 1 (store reviewer): /timeseries' four series come from
// weeklySeries, whose empty result was nil — `"prs_opened": null` for a
// repository with commits but no pull requests in the window, against the
// spec's `array` (D6).
func TestTimeSeriesEmptySeriesAreEmptyLists(t *testing.T) {
	store, ctx := openSR5Store(t)
	suffix := time.Now().UnixNano()
	owner := fmt.Sprintf("avx-ts-%d", suffix)
	var repoID int64
	if err := store.pool.QueryRow(ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id) VALUES ($1, $2, 't', 1) RETURNING repo_id`, "https://github.com/"+owner+"/t", owner).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupExecRetry(context.Background(), store, `DELETE FROM aveloxis_data.repos WHERE repo_id = $1`, repoID)
	})
	ts, err := store.GetRepoTimeSeries(ctx, repoID, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for name, series := range map[string][]WeeklyDataPoint{"commits": ts.Commits, "prs_opened": ts.PRsOpened, "prs_merged": ts.PRsMerged, "issues": ts.Issues} {
		if series == nil {
			t.Errorf("%s is nil (serializes as null); want []", name)
		}
	}
}

// Stage 1b review round 2 (L10): the one bucket spelling must hand
// PostgreSQL a timestamptz — `timestamp AT TIME ZONE 'UTC'` is a
// timestamptz, and date_trunc over a timestamptz truncates in the SESSION
// zone, so a DATE handed in as `col::timestamp` came back shifted by the
// zone's offset (this host's server zone is America/Chicago: a first
// appearance on 2024-03-01 bucketed as 2024-02-01 06:00). Two pins: the
// behavior on ContributorsNew, and the type of every bucketSQL site.
func TestContributorsNewBucketsAreUTCMidnightsInAnyZone(t *testing.T) {
	store, ctx := openSR5Store(t)
	suffix := time.Now().UnixNano()
	owner := fmt.Sprintf("avx-cnew-%d", suffix)
	var repoID int64
	if err := store.pool.QueryRow(ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id) VALUES ($1, $2, 'n', 1) RETURNING repo_id`, "https://github.com/"+owner+"/n", owner).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	cntrb := "0100cafe-d000-0000-0000-0000000000c1"
	mustExecRetry(ctx, t, store, `INSERT INTO aveloxis_data.contributors (cntrb_id, cntrb_login) VALUES ($1::uuid, $2) ON CONFLICT (cntrb_login) WHERE cntrb_login != '' DO NOTHING`, cntrb, owner)
	mustExecRetry(ctx, t, store, `INSERT INTO aveloxis_data.commits (repo_id, cmt_commit_hash, cmt_filename, cmt_author_name, cmt_author_email, cmt_author_date, cmt_author_timestamp, cmt_ght_author_id)
		VALUES ($1, 'cnew', 'f.go', 'a', 'a@example.com', '2024-03-01', $2, $3::uuid)`, repoID, time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC), cntrb)
	t.Cleanup(func() {
		bg := context.Background()
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.commits WHERE repo_id = $1`, repoID)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.repos WHERE repo_id = $1`, repoID)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.contributors WHERE cntrb_id = $1::uuid`, cntrb)
	})
	// Every connection of this test runs in a non-UTC session zone, whatever
	// the server's default is.
	mustExecRetry(ctx, t, store, `ALTER DATABASE `+quoteIdent(currentDatabase(ctx, t, store))+` SET timezone = 'America/Chicago'`)
	t.Cleanup(func() {
		cleanupExecRetry(context.Background(), store, `ALTER DATABASE `+quoteIdent(currentDatabase(context.Background(), t, store))+` RESET timezone`)
	})
	store.pool.Reset() // new sessions pick the database's zone
	rows, err := store.ContributorsNew(ctx, repoID, "month", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	if len(rows) != 1 || !rows[0].Date.Equal(want) || rows[0].Value != 1 {
		t.Errorf("ContributorsNew month bucket = %+v; want one bucket at %v", rows, want)
	}
}

// Every bucketSQL site in the metric readers must be a timestamptz: the
// expressions are read from the source and typed by PostgreSQL over
// one-row relations carrying the columns they name.
func TestEveryBucketSQLSiteIsATimestamptz(t *testing.T) {
	store, ctx := openSR5Store(t)
	src := srctest.StripGoComments(srctest.PackageSource(t, "internal/db", 100))
	sites := regexp.MustCompile(`bucketSQL\("([^"]+)"\)`).FindAllStringSubmatch(src, -1)
	srctest.MinCount(t, "bucketSQL sites in internal/db", len(sites), 11)
	// One spelling (review round 3): a reader that spells `date_trunc($1,
	// …)` inline is invisible to the type check above, so the only
	// `date_trunc($1` in the package is bucketSQL's body.
	if n := strings.Count(src, "date_trunc($1"); n != 1 {
		t.Errorf("internal/db spells date_trunc($1 %d times; want 1 (bucketSQL's body) — a metric bucket goes through bucketSQL", n)
	}
	for _, m := range sites {
		var typ string
		q := `SELECT pg_typeof(` + bucketSQL(m[1]) + `)::text
			FROM (SELECT now() AS created_at, now() AS closed_at) i,
			     (SELECT now() AS created_at) ie,
			     (SELECT now() AS created_at, now() AS merged_at, now() AS closed_at) p,
			     (SELECT now() AS msg_timestamp, now() AS cmt_author_timestamp, current_date AS first_seen) x`
		if err := store.pool.QueryRow(ctx, q, "day").Scan(&typ); err != nil {
			t.Fatalf("bucketSQL(%q): %v", m[1], err)
		}
		if typ != "timestamp with time zone" {
			t.Errorf("bucketSQL(%q) is a %s; a bucket is a timestamptz instant (a timestamp input truncates in the session's zone)", m[1], typ)
		}
	}
}

func currentDatabase(ctx context.Context, t *testing.T, store *PostgresStore) string {
	t.Helper()
	var name string
	if err := store.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}
