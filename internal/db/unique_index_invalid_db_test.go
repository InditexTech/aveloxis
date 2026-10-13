// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// H-2 (summary/59 §5, the hotfix release): the dedup-gated unique indexes
// were gated on pg_indexes, which also lists an INVALID index — the
// leftover of an interrupted CREATE INDEX CONCURRENTLY. The gate then
// returned early on every later migrate and the index stayed INVALID for
// good. For idx_commits_repo_hash_file that is the arbiter of every commit
// insert's ON CONFLICT. The gate must treat an INVALID index as missing:
// drop it and build it again.

// indexValid reads pg_index.indisvalid for schema.name; ok is false when no
// such index exists.
func indexValid(ctx context.Context, t *testing.T, store *PostgresStore, schema, name string) (valid, ok bool) {
	t.Helper()
	err := store.pool.QueryRow(ctx, `
		SELECT i.indisvalid FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`, schema, name).Scan(&valid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false
		}
		t.Fatalf("probe %s.%s: %v", schema, name, err)
	}
	return valid, true
}

// leaveInvalidUnique drops the unique index, inserts a duplicate pair with
// dup, lets a CONCURRENTLY build fail over it (which leaves the index
// INVALID, as an interrupted build does), then removes the duplicate with
// undup. The table is dup-free afterwards; only the index is broken.
func leaveInvalidUnique(ctx context.Context, t *testing.T, store *PostgresStore, schema, name, createSQL string, dup, undup func()) {
	t.Helper()
	mustExecRetry(ctx, t, store, `DROP INDEX IF EXISTS `+schema+`.`+name)
	dup()
	if _, err := store.pool.Exec(ctx, createSQL); err == nil {
		t.Fatalf("the CONCURRENTLY build over a duplicate pair succeeded; the fixture did not break %s", name)
	}
	if valid, ok := indexValid(ctx, t, store, schema, name); !ok || valid {
		t.Fatalf("fixture: %s exists=%v valid=%v, want an INVALID leftover", name, ok, valid)
	}
	undup()
}

func TestDedupGatedUniqueIndexesRebuildAnInvalidLeftover(t *testing.T) {
	store, ctx := v0251Connect(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const slug = "_avinvalididx"
	var repoID int64
	cleanup := func() {
		for _, sql := range []string{
			`DELETE FROM aveloxis_data.commits WHERE repo_id IN (SELECT repo_id FROM aveloxis_data.repos WHERE repo_git ILIKE $1)`,
			`DELETE FROM aveloxis_data.repo_labor WHERE repo_id IN (SELECT repo_id FROM aveloxis_data.repos WHERE repo_git ILIKE $1)`,
			`DELETE FROM aveloxis_data.repos WHERE repo_git ILIKE $1`,
		} {
			cleanupExecRetry(ctx, store, sql, "%"+slug+"%")
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id)
		VALUES ($1, 'org', $2, 1) RETURNING repo_id`, "https://github.com/org/"+slug, slug).Scan(&repoID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, createSQL string
		dup, undup      string
		gate            func()
	}{
		{
			name:      "idx_commits_repo_hash_file",
			createSQL: `CREATE UNIQUE INDEX CONCURRENTLY idx_commits_repo_hash_file ON aveloxis_data.commits (repo_id, cmt_commit_hash, cmt_filename)`,
			dup: `INSERT INTO aveloxis_data.commits (repo_id, cmt_commit_hash, cmt_filename)
			      VALUES ($1, 'h1', 'f'), ($1, 'h1', 'f')`,
			undup: `DELETE FROM aveloxis_data.commits WHERE cmt_id = (SELECT MAX(cmt_id) FROM aveloxis_data.commits WHERE repo_id = $1)`,
			gate:  func() { deduplicateCommits(ctx, store, logger) },
		},
		{
			name:      "uq_repo_labor_natural_key",
			createSQL: `CREATE UNIQUE INDEX CONCURRENTLY uq_repo_labor_natural_key ON aveloxis_data.repo_labor (repo_id, rl_analysis_date, file_path)`,
			dup: `INSERT INTO aveloxis_data.repo_labor (repo_id, rl_analysis_date, file_path)
			      VALUES ($1, '2026-01-01', 'a.go'), ($1, '2026-01-01', 'a.go')`,
			undup: `DELETE FROM aveloxis_data.repo_labor WHERE repo_labor_id = (SELECT MAX(repo_labor_id) FROM aveloxis_data.repo_labor WHERE repo_id = $1)`,
			gate:  func() { ensureRepoLaborNaturalKeyUnique(ctx, store, logger) },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Whatever happens, leave a valid index behind for the rest of
			// the package (other tests insert through these arbiters).
			t.Cleanup(func() {
				if valid, ok := indexValid(ctx, t, store, "aveloxis_data", c.name); !ok || !valid {
					// A fixture that failed partway can leave its duplicate pair
					// behind; remove this repository's rows first so the rebuild
					// below can succeed (review HF-8).
					cleanupExecRetry(ctx, store, `DELETE FROM aveloxis_data.commits WHERE repo_id = $1`, repoID)
					cleanupExecRetry(ctx, store, `DELETE FROM aveloxis_data.repo_labor WHERE repo_id = $1`, repoID)
					cleanupExecRetry(ctx, store, `DROP INDEX IF EXISTS aveloxis_data.`+c.name)
					cleanupExecRetry(ctx, store, strings.Replace(c.createSQL, "CONCURRENTLY", "CONCURRENTLY IF NOT EXISTS", 1))
				}
			})
			leaveInvalidUnique(ctx, t, store, "aveloxis_data", c.name, c.createSQL,
				func() { mustExecRetry(ctx, t, store, c.dup, repoID) },
				func() { mustExecRetry(ctx, t, store, c.undup, repoID) })

			c.gate()

			if valid, ok := indexValid(ctx, t, store, "aveloxis_data", c.name); !ok || !valid {
				t.Fatalf("after the migrate gate: %s exists=%v valid=%v; an INVALID leftover must be rebuilt, not taken as done", c.name, ok, valid)
			}
		})
	}
}
