// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// Stage 1b item 8 (summary/58 §7 #16, eval-4): the batch is the one stats
// reader. Before 1b the batch omitted archived, forked_from and
// last_activity_at (so /repos/stats?ids= said archived: false for an
// archived repository) and the single reader was its hand-kept twin. Now
// the single answer IS the batch answer for that id, field for field.
func TestRepoStatsBatchIsTheOneReader(t *testing.T) {
	store, ctx := openSR5Store(t)
	suffix := time.Now().UnixNano()
	owner := fmt.Sprintf("avx-onereader-%d", suffix)
	var repoID int64
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id, forked_from)
		VALUES ($1, $2, 'r', 1, 'upstream/r') RETURNING repo_id`, "https://github.com/"+owner+"/r", owner).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.issues WHERE repo_id = $1`, repoID)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.repo_info WHERE repo_id = $1`, repoID)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = $1`, repoID)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.repos WHERE repo_id = $1`, repoID)
	})
	activity := time.Date(2025, 5, 6, 7, 8, 9, 0, time.UTC)
	mustExecRetry(ctx, t, store, `INSERT INTO aveloxis_ops.collection_queue (repo_id, status, priority, due_at, last_collected, last_issues, last_prs, last_commits)
		VALUES ($1, 'queued', 100, NOW(), NOW(), 3, 2, 1)`, repoID)
	mustExecRetry(ctx, t, store, `INSERT INTO aveloxis_data.repo_info (repo_id, data_collection_date, status, pr_count, issues_count, commit_count)
		VALUES ($1, NOW(), 'Archived', 20, 30, 10)`, repoID)
	mustExecRetry(ctx, t, store, `INSERT INTO aveloxis_data.issues (repo_id, platform_issue_id, issue_number, issue_title, created_at)
		VALUES ($1, 1, 1, 'i', $2)`, repoID, activity)

	batch, err := store.GetRepoStatsBatch(ctx, []int64{repoID})
	if err != nil {
		t.Fatal(err)
	}
	b := batch[repoID]
	if b == nil {
		t.Fatal("the batch answered nothing for the repository")
	}
	if !b.Archived || b.ForkedFrom != "upstream/r" || b.LastActivityAt == nil || !b.LastActivityAt.Equal(activity) {
		t.Errorf("batch = archived %v, forked_from %q, last_activity_at %v; want true, upstream/r, %v", b.Archived, b.ForkedFrom, b.LastActivityAt, activity)
	}
	if b.GatheredIssues != 3 || b.MetadataIssues != 30 || !b.HasMetadataSnapshot {
		t.Errorf("batch counts = %+v", b)
	}
	single, err := store.GetRepoStats(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(single, b) {
		t.Errorf("the single answer differs from the batch's:\nsingle %+v\nbatch  %+v", single, b)
	}
}

// Stage 1b item 9: a search term's % and _ match themselves, at every
// search (SearchRepos, SearchOrgs, the queue page), and the org expansion
// tests the host without LIKE.
func TestSearchTermsMatchLikeMetacharactersLiterally(t *testing.T) {
	store, ctx := openSR5Store(t)
	suffix := time.Now().UnixNano()
	owner := fmt.Sprintf("avx-like%d", suffix)
	names := []string{"a_b", "axb", "p%q", "pxq"}
	var ids []int64
	for _, n := range names {
		var id int64
		if err := store.pool.QueryRow(ctx, `
			INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id)
			VALUES ($1, $2, $3, 1) RETURNING repo_id`, "https://github.com/"+owner+"/"+n, owner, n).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		mustExecRetry(ctx, t, store, `INSERT INTO aveloxis_ops.collection_queue (repo_id, status, priority, due_at) VALUES ($1, 'queued', 100, NOW())`, id)
	}
	t.Cleanup(func() {
		bg := context.Background()
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = ANY($1)`, ids)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.repos WHERE repo_owner = $1`, owner)
	})
	for term, want := range map[string]string{"a_b": "a_b", "p%q": "p%q"} {
		rows, err := store.SearchRepos(ctx, owner+"/"+term, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Name != want {
			t.Errorf("SearchRepos(%q) = %+v; want only %q (a metacharacter matches itself)", term, rows, want)
		}
		jobs, _, err := store.ListQueuePage(ctx, 50, 0, owner+"/"+term, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != 1 {
			t.Errorf("ListQueuePage(%q) = %d rows; want 1", term, len(jobs))
		}
	}
	// The owner search: an underscore in the term is literal too.
	if orgs, err := store.SearchOrgs(ctx, "avx-like_", 20); err != nil || len(orgs) != 0 {
		t.Errorf("SearchOrgs(%q) = %+v, %v; want nothing (the _ is literal; the owners have digits there)", "avx-like_", orgs, err)
	}
	if orgs, err := store.SearchOrgs(ctx, owner, 20); err != nil || len(orgs) != 1 || orgs[0].Host != "github.com" {
		t.Errorf("SearchOrgs(%q) = %+v, %v; want the one owner on github.com", owner, orgs, err)
	}
	// The org expansion tests the host exactly, not as a LIKE pattern.
	if got, err := store.ResolveOrgRepos(ctx, "github.com", owner); err != nil || len(got) != len(names) {
		t.Errorf("ResolveOrgRepos(github.com) = %v, %v; want all %d", got, err, len(names))
	}
	if got, err := store.ResolveOrgRepos(ctx, "github.c_m", owner); err != nil || len(got) != 0 {
		t.Errorf("ResolveOrgRepos(github.c_m) = %v, %v; want nothing (the host is compared exactly)", got, err)
	}
}

// Review round 1 (store reviewer, L9): the batch pre-filled a zero record
// for every requested id, so an id no repository has was answered as a
// repository with zero everything, while the single route is a 404 (D5).
// One reader, one answer: an id neither the queue read nor the queueless
// fallback returned is absent, and GetRepoStats says ErrRepoNotFound.
func TestRepoStatsBatchOmitsAnUnknownRepository(t *testing.T) {
	store, ctx := openSR5Store(t)
	var maxID int64
	if err := store.pool.QueryRow(ctx, `SELECT COALESCE(MAX(repo_id), 0) FROM aveloxis_data.repos`).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	unknown := maxID + 1000000
	batch, err := store.GetRepoStatsBatch(ctx, []int64{unknown})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := batch[unknown]; present {
		t.Errorf("the batch answered %+v for a repository nobody has; want it absent", batch[unknown])
	}
	if st, err := store.GetRepoStats(ctx, unknown); !errors.Is(err, ErrRepoNotFound) || st != nil {
		t.Errorf("GetRepoStats of an unknown id = %v, %v; want ErrRepoNotFound", st, err)
	}
}

// Review round 1 (L11 class sweep): the web GUI's group-page search is the
// fourth search; its term's % and _ are literal too.
func TestGroupPageSearchMatchesMetacharactersLiterally(t *testing.T) {
	store, ctx := openSR5Store(t)
	suffix := time.Now().UnixNano()
	owner := fmt.Sprintf("avx-glike%d", suffix)
	var userID int
	if err := store.pool.QueryRow(ctx, `INSERT INTO aveloxis_ops.users (login_name, oauth_provider) VALUES ($1, 'github') RETURNING user_id`, owner).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var groupID int64
	if err := store.pool.QueryRow(ctx, `INSERT INTO aveloxis_ops.user_groups (user_id, name, status) VALUES ($1, $2, 'approved') RETURNING group_id`, userID, owner).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, n := range []string{"a_b", "axb"} {
		var id int64
		if err := store.pool.QueryRow(ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id) VALUES ($1, $2, $3, 1) RETURNING repo_id`, "https://github.com/"+owner+"/"+n, owner, n).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		mustExecRetry(ctx, t, store, `INSERT INTO aveloxis_ops.user_repos (group_id, repo_id) VALUES ($1, $2)`, groupID, id)
	}
	t.Cleanup(func() {
		bg := context.Background()
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_ops.user_repos WHERE group_id = $1`, groupID)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_ops.user_groups WHERE group_id = $1`, groupID)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_ops.users WHERE user_id = $1`, userID)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.repos WHERE repo_id = ANY($1)`, ids)
	})
	detail, total, err := store.GetGroupDetail(ctx, userID, groupID, 1, 50, "a_b")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(detail.Repos) != 1 || detail.Repos[0].RepoName != "a_b" {
		t.Errorf("group search a_b = total %d, repos %+v; want only a_b", total, detail.Repos)
	}
}
