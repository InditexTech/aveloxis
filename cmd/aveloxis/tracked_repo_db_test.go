// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/srctest"
)

// H-5 (summary/59 §5, the hotfix release): `aveloxis prioritize` and
// `aveloxis recollect` resolved their URL with UpsertRepo, so a URL that was
// never added — a typo, an untracked repository — created a repos row with
// no queue entry. They look the repository up instead and refuse a URL that
// is not tracked, pointing at add-repo.
func TestTrackedRepoIDLooksUpWithoutWriting(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("integration: set AVELOXIS_TEST_DB to run")
	}
	ctx := context.Background()
	store, err := db.NewPostgresStore(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	pool := store.Pool()
	const tracked = "https://github.com/_av_tracked_probe/known"
	const unknown = "https://github.com/_av_tracked_probe/typo"
	cleanup := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM aveloxis_data.repos WHERE repo_git LIKE 'https://github.com/_av_tracked_probe/%'`)
	}
	cleanup()
	t.Cleanup(cleanup)
	var knownID int64
	if err := pool.QueryRow(ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id)
		VALUES ($1, '_av_tracked_probe', 'known', 1) RETURNING repo_id`, tracked).Scan(&knownID); err != nil {
		t.Fatal(err)
	}

	got, err := trackedRepoID(ctx, store, tracked)
	if err != nil || got != knownID {
		t.Fatalf("trackedRepoID(tracked) = %d, %v; want %d", got, err, knownID)
	}
	if got, err := trackedRepoID(ctx, store, tracked+".git"); err != nil || got != knownID {
		t.Fatalf("a .git variant of a tracked URL = %d, %v; want %d (the stored spelling)", got, err, knownID)
	}

	if _, err := trackedRepoID(ctx, store, unknown); !errors.Is(err, errRepoNotTracked) {
		t.Fatalf("trackedRepoID(unknown) error = %v; want errRepoNotTracked", err)
	} else if !strings.Contains(err.Error(), "add-repo") {
		t.Fatalf("the refusal should tell the operator to add the repository first: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM aveloxis_data.repos WHERE repo_git = $1`, unknown).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("looking up an untracked URL created %d repos row(s)", n)
	}
}

// Both commands resolve through trackedRepoID and never write a repos row.
func TestPrioritizeAndRecollectNeverUpsert(t *testing.T) {
	src := srctest.StripGoComments(srctest.Read(t, "cmd/aveloxis/main.go"))
	for _, fn := range []string{"func runPrioritize(", "func runRecollect("} {
		body := srctest.FuncBody(t, src, fn)
		if strings.Contains(body, "UpsertRepo(") {
			t.Errorf("%s writes a repos row (UpsertRepo); it must look the repository up", fn)
		}
		if !strings.Contains(body, "trackedRepoID(") {
			t.Errorf("%s must resolve its URL with trackedRepoID", fn)
		}
	}
}
