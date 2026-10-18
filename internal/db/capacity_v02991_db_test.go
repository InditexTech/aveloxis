// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/model"
)

// registerOrgForTest registers f's organization (https://github.com/<tag>,
// which covers f.repos) in groupID with an approved kind 'org' add request
// decided by decidedBy (0: an auto-approval).
func registerOrgForTest(t *testing.T, f *allocFixture, groupID int64, decidedBy int) {
	t.Helper()
	org := "https://github.com/" + f.tag
	mustExecRetry(f.ctx, t, f.store, `INSERT INTO aveloxis_ops.user_org_requests (user_id, group_id, org_url, org_name, platform)
		VALUES ($1, $2, $3, $4, 'github')`, f.user, groupID, org, f.tag)
	mustExecRetry(f.ctx, t, f.store, `INSERT INTO aveloxis_ops.collection_add_requests
		(user_id, group_id, kind, org_url, status, item_count, decided_by, decided_at)
		VALUES ($1, $2, 'org', $3, 'approved', 0, $4, NOW())`, f.user, groupID, org, decidedBy)
}

func (f *allocFixture) addedToday(t *testing.T) int {
	t.Helper()
	st, err := f.store.AccountDailyAdditions(f.ctx, f.user)
	if err != nil {
		t.Fatal(err)
	}
	return st.Used
}

// v0.29.91 (whole-PR review, finding 1): a non-administrator who adds an
// organization already registered elsewhere is auto-approved (decided_by
// 0), and the scans then linked up to 1,000 repositories through the
// organization door without counting them — repeatably, by removing and
// re-adding. Those links now count toward repo_links_per_day; the links of
// a registration a real administrator decided still do not.
func TestOrgLinksCountUnlessAnAdministratorDecided(t *testing.T) {
	f := newAllocFixture(t, false, 100, 4, capacity.Enforce)
	setLinksQuota(t, f, 100, capacity.Enforce)
	var admin int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT user_id FROM aveloxis_ops.users WHERE admin ORDER BY user_id LIMIT 1`).Scan(&admin); err != nil {
		t.Fatalf("the harness's bootstrap administrator: %v", err)
	}
	registerOrgForTest(t, f, f.group, 0) // auto-approved
	if ok, err := f.store.AddOrgRepoToGroupByID(f.ctx, f.group, f.repos[0]); err != nil || !ok {
		t.Fatalf("auto-approved org link = %v, %v", ok, err)
	}
	if got := f.addedToday(t); got != 1 {
		t.Errorf("an auto-approved registration's link counted %d additions today; want 1", got)
	}
	approved, err := f.store.CreateUserGroup(f.ctx, f.user, "org-admin-approved")
	if err != nil {
		t.Fatal(err)
	}
	registerOrgForTest(t, f, approved, admin)
	if ok, err := f.store.AddOrgRepoToGroupByID(f.ctx, approved, f.repos[1]); err != nil || !ok {
		t.Fatalf("admin-approved org link = %v, %v", ok, err)
	}
	if got := f.addedToday(t); got != 1 {
		t.Errorf("an administrator-approved registration's link counted: %d additions today; want still 1", got)
	}
	// No registration covering the repository: counted (nothing proves an
	// administrator decided it).
	bare, err := f.store.CreateUserGroup(f.ctx, f.user, "org-none")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AddOrgRepoToGroupByID(f.ctx, bare, f.repos[2]); err != nil {
		t.Fatal(err)
	}
	if got := f.addedToday(t); got != 2 {
		t.Errorf("an uncovered organization link counted %d additions today; want 2", got)
	}
	// The operator's own loader door is never counted.
	if _, err := f.store.AddOperatorRepoToGroupByID(f.ctx, bare, f.repos[3]); err != nil {
		t.Fatal(err)
	}
	if got := f.addedToday(t); got != 2 {
		t.Errorf("the operator's link counted: %d additions today; want 2", got)
	}
	// Enforced: the auto-approved scan is refused at the daily value.
	setLinksQuota(t, f, 2, capacity.Enforce)
	if _, err := f.store.RemoveRepoFromGroup(f.ctx, f.user, f.group, f.repos[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AddOrgRepoToGroupByID(f.ctx, f.group, f.repos[0]); err == nil {
		t.Error("an auto-approved org scan past the enforced daily value linked; want the refusal (remove and repeat is bounded)")
	} else if ex, ok := capacity.AsExceeded(err); !ok || ex.Quota != capacity.QuotaRepoLinksPerDay {
		t.Errorf("= %v; want the repo_links_per_day refusal", err)
	}
}

// v0.29.91 (finding 3): the day an addition is counted on is the day it
// was decided against — countAdditions read its own clock, so a decision
// at 23:59:59.999 counted on the next day.
func TestAdditionsAreCountedOnTheDayDecided(t *testing.T) {
	f := newAllocFixture(t, false, 100, 1, capacity.Enforce)
	setLinksQuota(t, f, 100, capacity.Enforce)
	decided := time.Date(2020, 1, 2, 23, 59, 59, 999e6, time.UTC)
	f.store.capacityClock = func() time.Time { return decided }
	t.Cleanup(func() { f.store.capacityClock = nil })
	if _, err := f.store.AddRepoToGroupByID(f.ctx, f.group, f.repos[0]); err != nil {
		t.Fatal(err)
	}
	var day time.Time
	if err := f.store.pool.QueryRow(f.ctx, `SELECT day FROM aveloxis_ops.request_counts WHERE subject = $1`, linksTodayKey(f.user)).Scan(&day); err != nil {
		t.Fatal(err)
	}
	if got := day.Format(time.DateOnly); got != "2020-01-02" {
		t.Errorf("an addition decided at %v was counted on %s; want 2020-01-02", decided, got)
	}
}

// v0.29.91 (finding 8): linkHeld reads no policy and opens no
// transaction — the place is already held. Before, a failed policy read
// (four pool reads it never used) failed the link, and the approval pass
// stamped an auto-approved item -1 for good.
func TestLinkHeldReadsNoPolicy(t *testing.T) {
	f := newAllocFixture(t, false, 100, 2, capacity.Enforce)
	f.store.linkPolicyRead = func() error { return errors.New("injected policy read failure") }
	t.Cleanup(func() { f.store.linkPolicyRead = nil })
	if n, err := f.store.linkWithinCap(f.ctx, f.group, []int64{f.repos[0]}, linkHeld); err != nil || n != 1 {
		t.Fatalf("linkHeld with a failing policy read = %d, %v; want 1 link (no policy read)", n, err)
	}
	// The seam is live: a decided link does read the policy.
	if _, err := f.store.linkWithinCap(f.ctx, f.group, []int64{f.repos[1]}, linkAll); err == nil {
		t.Fatal("the seam did not fire for linkAll: the test above proves nothing")
	}
}

// v0.29.91 (finding 9): a legacy pending group's items are repositories its
// owner already links; holding places for them double-counted, and an
// enforced repos_per_account that fits refused the conversion on every
// migrate (stopping the remaining groups' conversion too).
func TestLegacyPendingGroupConvertsUnderAnAllowanceThatFits(t *testing.T) {
	f := newAllocFixture(t, false, 2, 2, capacity.Enforce)
	for _, id := range f.repos {
		if _, err := f.store.AddRepoToGroupByID(f.ctx, f.group, id); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy shape: the group pends, its repositories are linked but not
	// collected.
	mustExecRetry(f.ctx, t, f.store, `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = ANY($1)`, f.repos)
	mustExecRetry(f.ctx, t, f.store, `UPDATE aveloxis_ops.user_groups SET status = 'pending' WHERE group_id = $1`, f.group)
	var errs []error
	migrateLegacyPendingGroups(f.ctx, f.store, slog.New(slog.NewTextHandler(io.Discard, nil)), &errs)
	if len(errs) != 0 {
		t.Fatalf("the conversion failed under an allowance its repositories fit: %v", errs)
	}
	status, err := f.store.GetGroupStatus(f.ctx, f.group)
	if err != nil || status != "approved" {
		t.Errorf("group status = %q, %v; want approved", status, err)
	}
	if got := f.linked(t); got != 2 {
		t.Errorf("reach after the conversion = %d; want 2 (each repository once)", got)
	}
}

// v0.29.91 (finding 9, the live path): a pending URL that became tracked
// through another approval is linked by a later paste; the item still
// pending named the same repository and was counted again.
func TestPendingItemForALinkedRepositoryIsCountedOnce(t *testing.T) {
	f := newAllocFixture(t, false, 1, 1, capacity.Enforce)
	mustExecRetry(f.ctx, t, f.store, `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = $1`, f.repos[0])
	out, err := f.store.AddReposToGroup(f.ctx, f.user, f.group, f.urls[:1], 0, "")
	if err != nil || out.Pending != 1 {
		t.Fatalf("the untracked URL = %+v, %v; want pending", out, err)
	}
	// Someone else's approval collects it.
	mustExecRetry(f.ctx, t, f.store, `INSERT INTO aveloxis_ops.collection_queue (repo_id, status, priority, due_at) VALUES ($1, 'queued', 100, NOW())`, f.repos[0])
	if _, err := f.store.AddReposToGroup(f.ctx, f.user, f.group, f.urls[:1], 0, ""); err != nil {
		t.Fatalf("pasting the now-tracked repository whose place the item holds = %v; want linked (allowance 1)", err)
	}
	if got := f.linked(t); got != 1 {
		t.Errorf("reach = %d; want 1 (the linked repository and its pending item are one)", got)
	}
}

// v0.29.91 (finding 10): a missing capacity_settings row is a deployment
// that has not migrated, said so — not a bare "no rows in result set" on
// every link, and not an empty contact (SR-5).
func TestMissingCapacitySettingsRowSaysMigrate(t *testing.T) {
	store, ctx := openSR5Store(t)
	var contact string
	var by *int
	var at time.Time
	if err := store.pool.QueryRow(ctx, `SELECT contact_email, updated_by, updated_at FROM aveloxis_ops.capacity_settings WHERE id = 1`).Scan(&contact, &by, &at); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupExecRetry(context.Background(), store, `INSERT INTO aveloxis_ops.capacity_settings (id, contact_email, updated_by, updated_at) VALUES (1, $1, $2, $3) ON CONFLICT (id) DO NOTHING`, contact, by, at)
	})
	mustExecRetry(ctx, t, store, `DELETE FROM aveloxis_ops.capacity_settings WHERE id = 1`)
	_, err := store.GetCapacityContact(ctx)
	if !errors.Is(err, ErrCapacitySettingsMissing) {
		t.Fatalf("GetCapacityContact without the row = %v; want ErrCapacitySettingsMissing", err)
	}
	if err := store.SetCapacityContact(ctx, "x@example.org", 0); !errors.Is(err, ErrCapacitySettingsMissing) {
		t.Errorf("SetCapacityContact without the row = %v; want the same error", err)
	}
}

// insertUntrackedLinkedRepo makes a repository row (no queue row: not
// tracked) that f's account links in its group.
func insertUntrackedLinkedRepo(t *testing.T, f *allocFixture, url string) int64 {
	t.Helper()
	var id int64
	if err := f.store.pool.QueryRow(f.ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id, repo_group_id)
		VALUES ($1, $2, 'x', 1, 1) RETURNING repo_id`, url, f.tag).Scan(&id); err != nil {
		t.Fatal(err)
	}
	mustExecRetry(f.ctx, t, f.store, `INSERT INTO aveloxis_ops.user_repos (group_id, repo_id) VALUES ($1, $2)`, f.group, id)
	return id
}

// v0.29.91 review round 2 (R2-3): an item URL is matched to the repository
// it will actually link the way FindRepoByURL resolves it (normalized, the
// case rule, the exact spelling first). Raw text matching under-counted: a
// legacy stored ".git" row the owner links matched a paste of the same
// text (no place asked), but approval resolves that paste to a NEW
// repository and links it.
func TestHeldItemMatchesTheRepositoryApprovalWillLink(t *testing.T) {
	t.Run("legacy suffixed row", func(t *testing.T) {
		f := newAllocFixture(t, false, 1, 0, capacity.Enforce)
		legacy := "https://github.com/" + f.tag + "/legacy.git"
		insertUntrackedLinkedRepo(t, f, legacy)
		if got := f.linked(t); got != 1 {
			t.Fatalf("reach = %d, want 1", got)
		}
		// Approval would create .../legacy (FindRepoByURL normalizes the
		// suffix away and finds no row): a new place, refused at 1 of 1.
		_, err := f.store.AddReposToGroup(f.ctx, f.user, f.group, []string{legacy}, 0, "")
		if ex, ok := capacity.AsExceeded(err); !ok || ex.Quota != capacity.QuotaReposPerAccount {
			t.Fatalf("pasting a legacy linked row's text = %v; want the allocation refusal (approval links a new repository)", err)
		}
		if err := f.store.SetCapacityOverride(f.ctx, CapacityOverride{UserID: f.user, ReposAllowed: intPtr(2)}, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.AddReposToGroup(f.ctx, f.user, f.group, []string{legacy}, 0, ""); err != nil {
			t.Fatal(err)
		}
		if got := f.linked(t); got != 2 {
			t.Errorf("reach = %d; want 2 (the linked legacy row and the place the pending item holds)", got)
		}
	})
	t.Run("case-variant duplicate", func(t *testing.T) {
		// Two rows apart from case exist only where dedup-repos has not run
		// (uq_repos_repo_git_ci absent): built inside a transaction that
		// drops the index and rolls back.
		f := newAllocFixture(t, false, 10, 0, capacity.Enforce)
		tx, err := f.store.pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(f.ctx, `DROP INDEX IF EXISTS aveloxis_data.uq_repos_repo_git_ci`); err != nil {
			t.Fatal(err)
		}
		var upper, lower int64
		for _, row := range []struct {
			url string
			id  *int64
		}{{"https://github.com/" + f.tag + "/Dup", &upper}, {"https://github.com/" + f.tag + "/dup", &lower}} {
			if err := tx.QueryRow(f.ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id, repo_group_id)
				VALUES ($1, $2, 'dup', 1, 1) RETURNING repo_id`, row.url, f.tag).Scan(row.id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(f.ctx, `INSERT INTO aveloxis_ops.user_repos (group_id, repo_id) VALUES ($1, $2)`, f.group, upper); err != nil {
			t.Fatal(err)
		}
		var req int64
		if err := tx.QueryRow(f.ctx, `INSERT INTO aveloxis_ops.collection_add_requests (user_id, group_id, kind, org_url, status, item_count)
			VALUES ($1, $2, 'repos', '', 'pending', 1) RETURNING request_id`, f.user, f.group).Scan(&req); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `INSERT INTO aveloxis_ops.collection_add_request_items (request_id, repo_url) VALUES ($1, $2)`,
			req, "https://github.com/"+f.tag+"/dup"); err != nil {
			t.Fatal(err)
		}
		// FindRepoByURL resolves the item to the exact-case row, which the
		// owner does not link: two repositories reached, not one.
		var reach int
		if err := tx.QueryRow(f.ctx, reachSQL, f.user).Scan(&reach); err != nil {
			t.Fatal(err)
		}
		if reach != 2 {
			t.Errorf("reach with a linked 'Dup' and a pending 'dup' (a separate row) = %d; want 2", reach)
		}
		_ = lower
	})
}

// The SQL normalization of an item URL is model.NormalizeRepoGitURL (the Go
// half FindRepoByURL applies), on a corpus of the suffix shapes.
func TestItemURLNormalizationIsTheModelRule(t *testing.T) {
	store, ctx := openSR5Store(t)
	for _, u := range []string{
		"https://github.com/a/b", "https://github.com/a/b.git", "https://github.com/a/b/", "https://github.com/a/b.git/",
		"https://github.com/a/b.git/.git/", "https://github.com/a/b/.git", "https://github.com/a/b//", "https://github.com/a/bgit",
		"https://github.com/a/b..git", "https://gitlab.com/g/s/p.git", "https://github.com/a/.git", "",
	} {
		var got string
		if err := store.pool.QueryRow(ctx, `SELECT `+normalizedRepoURLSQL("$1::text"), u).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := model.NormalizeRepoGitURL(u); got != want {
			t.Errorf("SQL normalizes %q to %q; model.NormalizeRepoGitURL says %q", u, got, want)
		}
	}
}

// v0.29.91 review round 2 (R2-4): repositories an auto-approved paste brings
// (web.auto_approve_add_limit; the items are written 'approved' with
// decided_by 0 and linked by linkHeld) are the account's own additions
// (finding 1's rule) and count toward repo_links_per_day, decided with the
// paste, all or nothing — otherwise add, remove and repeat ran unbounded
// with new collection load.
func TestAutoApprovedPasteCountsTowardDailyAdditions(t *testing.T) {
	f := newAllocFixture(t, false, 100, 4, capacity.Enforce)
	setLinksQuota(t, f, 2, capacity.Enforce)
	mustExecRetry(f.ctx, t, f.store, `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = ANY($1)`, f.repos)
	out, err := f.store.AddReposToGroup(f.ctx, f.user, f.group, f.urls[:2], 2, "")
	if err != nil || out.Enqueued != 2 {
		t.Fatalf("an auto-approved paste of 2 = %+v, %v; want both added", out, err)
	}
	if got := f.addedToday(t); got != 2 {
		t.Errorf("an auto-approved paste of 2 counted %d additions today; want 2", got)
	}
	for _, id := range f.repos[:2] {
		if _, err := f.store.RemoveRepoFromGroup(f.ctx, f.user, f.group, id); err != nil {
			t.Fatal(err)
		}
	}
	var requestsBefore int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM aveloxis_ops.collection_add_requests WHERE user_id = $1`, f.user).Scan(&requestsBefore); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.AddReposToGroup(f.ctx, f.user, f.group, f.urls[2:4], 2, "")
	if ex, ok := capacity.AsExceeded(err); !ok || ex.Quota != capacity.QuotaRepoLinksPerDay {
		t.Fatalf("a second auto-approved paste of 2 past 2 a day = %v; want the repo_links_per_day refusal", err)
	}
	var requestsAfter int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM aveloxis_ops.collection_add_requests WHERE user_id = $1`, f.user).Scan(&requestsAfter); err != nil {
		t.Fatal(err)
	}
	if got := f.linked(t); got != 0 || requestsAfter != requestsBefore {
		t.Errorf("a refused paste wrote something: reach %d, requests %d → %d", got, requestsBefore, requestsAfter)
	}
	// Round 4 R4-2: the refused decision counts what it admitted (nothing),
	// not the held URLs it refused.
	pol, err := f.store.readOwnerPolicy(f.ctx, f.user, true)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	d, err := f.store.decideWithinCap(f.ctx, tx, f.user, pol, nil, f.urls[2:4], true, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.refused == nil || d.counted != 0 || d.admit != 0 {
		t.Errorf("a refused all-or-none decision = refused %v, admit %d, counted %d; want refused, 0, 0", d.refused, d.admit, d.counted)
	}
}

// v0.29.91 review round 3 (R3-1): an auto-approved re-paste of URLs a
// pending request already holds asked no new PLACE (correct: the place is
// held) and so counted no daily addition either — paste many as pending,
// then re-paste them a few at a time under web.auto_approve_add_limit, and
// all were created, queued and linked with no approval and no daily
// count. Daily additions are counted apart from places: every held URL
// the account does not link yet.
func TestAutoApprovedRepasteOfPendingURLsCounts(t *testing.T) {
	f := newAllocFixture(t, false, 100, 2, capacity.Enforce)
	setLinksQuota(t, f, 1, capacity.Enforce)
	mustExecRetry(f.ctx, t, f.store, `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = ANY($1)`, f.repos)
	out, err := f.store.AddReposToGroup(f.ctx, f.user, f.group, f.urls, 0, "")
	if err != nil || out.Pending != 2 {
		t.Fatalf("the pending paste = %+v, %v", out, err)
	}
	var requestsBefore int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM aveloxis_ops.collection_add_requests WHERE user_id = $1`, f.user).Scan(&requestsBefore); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.AddReposToGroup(f.ctx, f.user, f.group, f.urls, 2, "")
	if ex, ok := capacity.AsExceeded(err); !ok || ex.Quota != capacity.QuotaRepoLinksPerDay {
		t.Fatalf("an auto-approved re-paste of 2 pending URLs past 1 a day = %v; want the repo_links_per_day refusal", err)
	}
	var requestsAfter, linked int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM aveloxis_ops.collection_add_requests WHERE user_id = $1`, f.user).Scan(&requestsAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.store.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM aveloxis_ops.user_repos WHERE group_id = $1`, f.group).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if requestsAfter != requestsBefore || linked != 0 {
		t.Errorf("a refused re-paste wrote something: requests %d → %d, links %d", requestsBefore, requestsAfter, linked)
	}
	// The PLACE count has no such hole: with room for the additions, the
	// re-paste is accepted, counted, and each repository holds one place
	// although a pending item and the auto-approved one both named it.
	setLinksQuota(t, f, 10, capacity.Enforce)
	if _, err := f.store.AddReposToGroup(f.ctx, f.user, f.group, f.urls, 2, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.addedToday(t); got != 2 {
		t.Errorf("the accepted re-paste counted %d additions; want 2", got)
	}
	if got := f.linked(t); got != 2 {
		t.Errorf("reach = %d; want 2 (the pending items name the linked repositories)", got)
	}
}

// v0.29.91 review round 3 (R3-2): when repos_per_account refuses an
// all-or-none addition, that refusal stands: the daily decision is not
// made (it replaced the refusal, and under shadow logged a "would refuse"
// for an addition that was never going to happen).
func TestAllocationRefusalTakesPrecedenceOverDailyAdditions(t *testing.T) {
	f := newAllocFixture(t, false, 1, 2, capacity.Enforce)
	setLinksQuota(t, f, 1, capacity.Enforce)
	var logs strings.Builder
	f.store.logger = slog.New(slog.NewTextHandler(&lockedWriter{w: &logs}, nil))
	if _, err := f.store.AddRepoToGroupByID(f.ctx, f.group, f.repos[0]); err != nil {
		t.Fatal(err)
	}
	mustExecRetry(f.ctx, t, f.store, `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = $1`, f.repos[1])
	// Both spent: an auto-approved paste of one new URL.
	_, err := f.store.AddReposToGroup(f.ctx, f.user, f.group, f.urls[1:2], 2, "")
	if ex, ok := capacity.AsExceeded(err); !ok || ex.Quota != capacity.QuotaReposPerAccount {
		t.Fatalf("with both spent = %v; want the repos_per_account refusal", err)
	}
	if strings.Contains(logs.String(), "daily additions quota") {
		t.Errorf("the daily decision ran after the allocation refused:\n%s", logs.String())
	}
	// Shadow links: no spurious "would refuse" either.
	setLinksQuota(t, f, 1, capacity.Shadow)
	logs.Reset()
	if _, err := f.store.AddReposToGroup(f.ctx, f.user, f.group, f.urls[1:2], 2, ""); err == nil {
		t.Fatal("the allocation must still refuse")
	}
	if strings.Contains(logs.String(), "daily additions quota (shadow)") {
		t.Errorf("shadow daily additions logged a would-refuse for a refused addition:\n%s", logs.String())
	}
}

// lockedWriter serializes writes to w (a store logs from pool goroutines).
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// v0.29.91 review round 4 (R4-1): a link mode is an identity, so a value
// not named as one of the modes — the zero value, or a fresh definition
// spelled like linkHeld — is none of them and is decided and counted like
// linkAll (never a held place, never uncounted).
func TestMintedLinkModeIsDecidedLikeLinkAll(t *testing.T) {
	f := newAllocFixture(t, false, 100, 3, capacity.Enforce)
	setLinksQuota(t, f, 1, capacity.Enforce)
	if _, err := f.store.AddRepoToGroupByID(f.ctx, f.group, f.repos[0]); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]linkMode{"zero value": {}, "a second held": {&linkModeDef{"held"}}} {
		_, err := f.store.linkWithinCap(f.ctx, f.group, f.repos[1:3], mode)
		if ex, ok := capacity.AsExceeded(err); !ok || ex.Quota != capacity.QuotaRepoLinksPerDay {
			t.Errorf("%s: = %v; want the repo_links_per_day refusal (decided and counted, all or none)", name, err)
		}
	}
	if got := f.linked(t); got != 1 {
		t.Errorf("reach = %d; want 1 (nothing linked by a minted mode)", got)
	}
}
