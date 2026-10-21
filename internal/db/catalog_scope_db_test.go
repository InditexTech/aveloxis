// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// catalogFixture seeds two legacy repository groups and three repositories:
// a GitHub repository and its GitLab twin (same owner and name, different
// forge) in the first group, and a third repository in the second group.
type catalogFixture struct {
	group1, group2      int64
	gh, gl, other       int64
	owner, name, other2 string
	rg1, rg2            string
}

func seedCatalogFixture(t *testing.T, ctx context.Context, store *PostgresStore) catalogFixture {
	t.Helper()
	suffix := time.Now().UnixNano()
	fx := catalogFixture{
		owner:  fmt.Sprintf("avx-cat-owner-%d", suffix),
		name:   fmt.Sprintf("Twin-%d", suffix), // mixed case: the lookups match without regard to case
		other2: fmt.Sprintf("other-%d", suffix),
		rg1:    fmt.Sprintf("avx-cat-rg1-%d", suffix),
		rg2:    fmt.Sprintf("avx-cat-rg2-%d", suffix),
	}
	for _, g := range []struct {
		name string
		id   *int64
	}{{fx.rg1, &fx.group1}, {fx.rg2, &fx.group2}} {
		if err := store.pool.QueryRow(ctx, `INSERT INTO aveloxis_data.repo_groups (rg_name) VALUES ($1) RETURNING repo_group_id`, g.name).Scan(g.id); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []struct {
		git      string
		name     string
		platform int
		group    int64
		id       *int64
	}{
		{"https://github.com/" + fx.owner + "/" + fx.name, fx.name, 1, fx.group1, &fx.gh},
		{"https://gitlab.com/" + fx.owner + "/" + fx.name, fx.name, 2, fx.group1, &fx.gl},
		{"https://github.com/" + fx.owner + "/" + fx.other2, fx.other2, 1, fx.group2, &fx.other},
	} {
		if err := store.pool.QueryRow(ctx, `
			INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id, repo_group_id)
			VALUES ($1, $2, $3, $4, $5) RETURNING repo_id`, r.git, fx.owner, r.name, r.platform, r.group).Scan(r.id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		bg := context.Background()
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.repos WHERE repo_owner = $1`, fx.owner)
		cleanupExecRetry(bg, store, `DELETE FROM aveloxis_data.repo_groups WHERE rg_name IN ($1, $2)`, fx.rg1, fx.rg2)
	})
	return fx
}

func repoIDs(rows []RepoResult) []int64 {
	out := []int64{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func groupIDs(rows []RepoGroupResult) []int64 {
	out := []int64{}
	for _, g := range rows {
		out = append(out, g.ID)
	}
	return out
}

func has(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// Stage 1b item 5 (D1, N20, N21): the Augur catalog readers take a scope.
// An unscoped read (an administrator's session, a caller without an
// identity) sees the fleet; a scoped read sees exactly the ids in the
// scope — and an EMPTY scope that is not "all" sees nothing (a nil slice
// must never mean "all"). Groups are listed when they hold at least one
// in-scope repository (N20). The owner/name lookup picks within the scope
// by platform then id; when only an out-of-scope twin exists it says so
// with that twin's id (403 upstream), and an absent name is not found
// (N21). An empty list is [] (D6).
func TestCatalogReadersTakeAScope(t *testing.T) {
	store, ctx := openSR5Store(t)
	fx := seedCatalogFixture(t, ctx, store)
	all := RepoScope{All: true}
	onlyGL := RepoScope{IDs: []int64{fx.gl}}
	onlyOther := RepoScope{IDs: []int64{fx.other}}
	nothing := RepoScope{}

	// ListRepos
	if rows, err := store.ListRepos(ctx, all); err != nil || !has(repoIDs(rows), fx.gh) || !has(repoIDs(rows), fx.gl) || !has(repoIDs(rows), fx.other) {
		t.Errorf("unscoped ListRepos: err %v, has gh %v gl %v other %v; want all three", err, has(repoIDs(rows), fx.gh), has(repoIDs(rows), fx.gl), has(repoIDs(rows), fx.other))
	}
	if rows, err := store.ListRepos(ctx, onlyGL); err != nil || len(rows) != 1 || rows[0].ID != fx.gl {
		t.Errorf("scoped ListRepos: err %v, ids %v; want exactly [%d]", err, repoIDs(rows), fx.gl)
	}
	if rows, err := store.ListRepos(ctx, nothing); err != nil || rows == nil || len(rows) != 0 {
		t.Errorf("empty scope ListRepos: err %v, rows %#v; want an empty, non-nil list (an empty scope is NOTHING, never all)", err, rows)
	}

	// ListRepoGroups (N20)
	if groups, err := store.ListRepoGroups(ctx, all); err != nil || !has(groupIDs(groups), fx.group1) || !has(groupIDs(groups), fx.group2) {
		t.Errorf("unscoped ListRepoGroups: err %v, ids %v; want both groups", err, groupIDs(groups))
	}
	if groups, err := store.ListRepoGroups(ctx, onlyGL); err != nil || len(groups) != 1 || groups[0].ID != fx.group1 {
		t.Errorf("scoped ListRepoGroups: err %v, ids %v; want only the group holding the in-scope repository", err, groupIDs(groups))
	}
	if groups, err := store.ListRepoGroups(ctx, nothing); err != nil || groups == nil || len(groups) != 0 {
		t.Errorf("empty scope ListRepoGroups: err %v, %#v; want an empty, non-nil list", err, groups)
	}

	// GetReposByGroup
	if rows, err := store.GetReposByGroup(ctx, fx.group1, all); err != nil || len(rows) != 2 {
		t.Errorf("unscoped GetReposByGroup: err %v, ids %v; want the twins", err, repoIDs(rows))
	}
	if rows, err := store.GetReposByGroup(ctx, fx.group1, onlyGL); err != nil || len(rows) != 1 || rows[0].ID != fx.gl {
		t.Errorf("scoped GetReposByGroup: err %v, ids %v; want [%d]", err, repoIDs(rows), fx.gl)
	}
	if rows, err := store.GetReposByGroup(ctx, fx.group1, onlyOther); err != nil || rows == nil || len(rows) != 0 {
		t.Errorf("GetReposByGroup with nothing in scope in the group: err %v, %#v; want an empty, non-nil list", err, rows)
	}

	// GetRepoGroupByName (N20: a group with no in-scope repository is not found)
	if g, err := store.GetRepoGroupByName(ctx, fx.rg1, all); err != nil || g.ID != fx.group1 {
		t.Errorf("unscoped GetRepoGroupByName: %v, %v", g, err)
	}
	if _, err := store.GetRepoGroupByName(ctx, fx.rg1, onlyOther); !errors.Is(err, ErrRepoGroupNotFound) {
		t.Errorf("GetRepoGroupByName outside the scope = %v; want ErrRepoGroupNotFound", err)
	}

	// GetRepoByOwnerName (N21): the pick is by platform then id, within the scope.
	if r, err := store.GetRepoByOwnerName(ctx, fx.owner, fx.name, all); err != nil || r.ID != fx.gh {
		t.Errorf("unscoped owner/name with twins: %v, %v; want the GitHub one (platform 1 first)", r, err)
	}
	if r, err := store.GetRepoByOwnerName(ctx, fx.owner, fx.name, onlyGL); err != nil || r.ID != fx.gl {
		t.Errorf("scoped owner/name: %v, %v; want the in-scope twin", r, err)
	}
	var oos *RepoOutOfScopeError
	if _, err := store.GetRepoByOwnerName(ctx, fx.owner, fx.name, onlyOther); !errors.As(err, &oos) || oos.RepoID != fx.gh {
		t.Errorf("owner/name with only out-of-scope twins = %v; want RepoOutOfScopeError naming %d", err, fx.gh)
	}
	if _, err := store.GetRepoByOwnerName(ctx, fx.owner, "no-such-"+fx.name, onlyOther); !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("owner/name absent = %v; want ErrRepoNotFound", err)
	}
	// Case does not matter, as before.
	if r, err := store.GetRepoByOwnerName(ctx, fx.owner, "twin-"+fx.name[5:], all); err != nil || r.ID != fx.gh {
		t.Errorf("owner/name case-folded: %v, %v", r, err)
	}

	// GetRepoInGroupByName: the group's repository by name, case-folded like the owner/name lookup.
	if r, err := store.GetRepoInGroupByName(ctx, fx.group1, "twin-"+fx.name[5:], all); err != nil || r.ID != fx.gh {
		t.Errorf("group+name unscoped: %v, %v; want the GitHub twin", r, err)
	}
	if r, err := store.GetRepoInGroupByName(ctx, fx.group1, fx.name, onlyGL); err != nil || r.ID != fx.gl {
		t.Errorf("group+name scoped: %v, %v; want the GitLab twin", r, err)
	}
	if _, err := store.GetRepoInGroupByName(ctx, fx.group1, fx.name, onlyOther); !errors.As(err, &oos) || oos.RepoID != fx.gh {
		t.Errorf("group+name with only out-of-scope twins = %v; want RepoOutOfScopeError naming %d", err, fx.gh)
	}
	if _, err := store.GetRepoInGroupByName(ctx, fx.group2, fx.name, all); !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("group+name in the wrong group = %v; want ErrRepoNotFound", err)
	}
}
