// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestDeployAckLifecycle (v0.29.0 deploy gate): record → exists →
// idempotent re-record; and FleetHasCollectedData reflects the queue.
func TestDeployAckLifecycle(t *testing.T) {
	store, ctx := emConnect(t)
	t.Cleanup(store.Close)
	const ver = "0.0.0-avdeploytest"
	const repoGit = "https://github.com/_avdeployack/collected"
	cleanup := func() {
		cleanupExecRetry(context.Background(), store,
			`DELETE FROM aveloxis_ops.deploy_ack WHERE tool_version = $1`, ver)
		cleanupExecRetry(context.Background(), store,
			`DELETE FROM aveloxis_ops.collection_queue WHERE repo_id IN (SELECT repo_id FROM aveloxis_data.repos WHERE repo_git = $1)`, repoGit)
		cleanupExecRetry(context.Background(), store, `DELETE FROM aveloxis_data.repos WHERE repo_git = $1`, repoGit)
	}
	cleanup()
	t.Cleanup(cleanup)

	if ok, err := store.DeployAckExists(ctx, ver); err != nil || ok {
		t.Fatalf("unacked version: exists=%v err=%v, want false", ok, err)
	}
	if err := store.RecordDeployAck(ctx, ver, "test"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.DeployAckExists(ctx, ver); err != nil || !ok {
		t.Fatalf("after record: exists=%v err=%v, want true", ok, err)
	}
	// Idempotent re-record (updates the note/time, no error).
	if err := store.RecordDeployAck(ctx, ver, "test again"); err != nil {
		t.Fatalf("re-record: %v", err)
	}

	// FleetHasCollectedData reflects last_collected (round 19). The test
	// seeds one collected queue row instead of hoping the database has one
	// (it skipped on every fresh per-package database until 0.29.93's
	// no-silent-skip rule; the fresh-install false path is covered by the
	// missing-table branch, unit-tested via the fake gate).
	var repoID int64
	if err := store.pool.QueryRow(ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id)
		VALUES ($1, '_avdeployack', 'collected', 1) RETURNING repo_id`, repoGit).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	mustExecRetry(ctx, t, store, `INSERT INTO aveloxis_ops.collection_queue (repo_id, last_collected) VALUES ($1, NOW())`, repoID)
	if ok, err := store.FleetHasCollectedData(ctx); err != nil {
		t.Fatalf("FleetHasCollectedData: %v", err)
	} else if !ok {
		t.Fatal("FleetHasCollectedData = false with a collected queue row present")
	}
}

// TestFleetHasCollectedDataRequiresLastCollected (Copilot round 19 on
// PR #193): the deploy gate's "existing fleet" probe must key on a
// non-NULL last_collected, NOT mere queue presence — a fresh install
// can add-repo and restart before its first pass, and must not then be
// gated for legacy data it does not hold. Mutation-provable: drop the
// predicate and this fails.
func TestFleetHasCollectedDataRequiresLastCollected(t *testing.T) {
	src := srctest.Read(t, "internal/db/deploy_ack.go")
	body := srctest.FuncBody(t, src, "func (s *PostgresStore) FleetHasCollectedData(")
	if !strings.Contains(body, "last_collected IS NOT NULL") {
		t.Error("FleetHasCollectedData must probe `last_collected IS NOT NULL`, not bare queue presence — a queued-but-never-collected repo is a fresh install (Copilot round 19)")
	}
}
