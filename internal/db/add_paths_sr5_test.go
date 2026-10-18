// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/model"
)

func openSR5Store(t *testing.T) (*PostgresStore, context.Context) {
	t.Helper()
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	store, err := NewPostgresStore(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	testMigrate(ctx, t, store)
	return store, ctx
}

// TestOwnershipLookupErrorsAreNotNotYours pins worklist follow-up 12 at the
// store: the group access primitive (GroupOwner, behind
// admin.Service.GroupAccess since stage 1 of summary/59) read ANY
// ownership-lookup error as "group N is not yours", and
// CopyCollectionToGroup turned any verifyGroupOwned error into
// ErrNotGroupOwner. Only "no such group" is ErrGroupNotFound and only
// another owner is "not yours"; a failed lookup is returned as itself
// (SR-5).
func TestOwnershipLookupErrorsAreNotNotYours(t *testing.T) {
	store, ctx := openSR5Store(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := store.GroupOwner(canceled, 1); err == nil || errors.Is(err, ErrGroupNotFound) || errors.Is(err, ErrGroupNotOwned) {
		t.Errorf("GroupOwner on a failed lookup = %v; want the store's error, not \"no such group\"", err)
	}
	if _, err := store.RepoInGroup(canceled, 1, 1); err == nil {
		t.Error("RepoInGroup on a failed lookup = nil; want the store's error, never \"not a member\" (SR-16)")
	}
	if _, err := store.CopyCollectionToGroup(canceled, 1, 1, 1); err == nil || errors.Is(err, ErrNotGroupOwner) {
		t.Errorf("CopyCollectionToGroup on a failed lookup = %v; want the store's error, not ErrNotGroupOwner", err)
	}
	// The real answer is unchanged: a group nobody owns.
	if _, _, err := store.GroupOwner(ctx, 1<<40); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("an unknown group = %v; want ErrGroupNotFound", err)
	}
	if _, err := store.CopyCollectionToGroup(ctx, 1, 1, 1<<40); !errors.Is(err, ErrNotGroupOwner) {
		t.Errorf("an unowned target group = %v; want ErrNotGroupOwner", err)
	}
}

// TestOverLongURLsAreRefusedAtEveryWriter pins worklist follow-up 14: a
// pending org request whose URL is too long for the registration's unique
// index failed at approval forever (DecideAddRequest applied no limit), and
// `aveloxis add-repo` and every other UpsertRepo path had no limit at all.
// The owning writers refuse: registerApprovedOrg (approval and
// auto-approval alike) and UpsertRepo answer ErrURLTooLong, so the admin is
// told to reject the request and the CLI reports the URL.
func TestOverLongURLsAreRefusedAtEveryWriter(t *testing.T) {
	store, ctx := openSR5Store(t)
	const login = "_avoverlong_probe"
	// Past the INDEX bound, not merely the entry limit: a pending request of
	// 1,343–2,684 bytes has always approved and still must.
	tooLong := "https://github.com/_avoverlong-" + strings.Repeat("x", maxIndexedURLBytes)
	clean := func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_org_requests WHERE org_url LIKE 'https://github.com/_avoverlong-%'`)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_data.repos WHERE repo_git LIKE 'https://github.com/_avoverlong-%'`)
	}
	clean()
	t.Cleanup(clean)
	uid, err := store.UpsertOAuthUser(ctx, OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	gid, err := store.CreateUserGroup(ctx, uid, "over-long probe")
	if err != nil {
		t.Fatal(err)
	}
	// A pending org request created before v0.29.54's limit existed.
	reqID, err := store.createAddRequest(ctx, uid, gid, "org", tooLong, nil, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.DecideAddRequest(ctx, reqID, uid, true, "", ""); !errors.Is(err, ErrURLTooLong) {
		t.Errorf("approving an over-long org URL = %v; want ErrURLTooLong (the admin is told to reject it)", err)
	}
	// A legacy request between the entry limit and the index bound approves.
	legacy := "https://github.com/_avoverlong-" + strings.Repeat("y", MaxAddURLBytes+100)
	legacyID, err := store.createAddRequest(ctx, uid, gid, "org", legacy, nil, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.DecideAddRequest(ctx, legacyID, uid, true, "", ""); err != nil {
		t.Errorf("approving a legacy org URL between the entry limit and the index bound = %v; want approved", err)
	}
	var status string
	if err := store.pool.QueryRow(ctx, `SELECT status FROM aveloxis_ops.collection_add_requests WHERE request_id = $1`, reqID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Errorf("the refused approval left the request %q; want pending (nothing flipped)", status)
	}
	if _, err := store.UpsertRepo(ctx, &model.Repo{GitURL: tooLong, Owner: "_avoverlong", Name: "x", Platform: model.PlatformGitHub}); !errors.Is(err, ErrURLTooLong) {
		t.Errorf("UpsertRepo with an over-long URL = %v; want ErrURLTooLong", err)
	}
}

// TestOrgRegistrationIsCaseInsensitive pins worklist follow-up 3: the
// "already registered" check compares LOWER(org_url) but the unique key is
// case-sensitive, so re-adding `github.com/CHAOSS` beside `github.com/chaoss`
// made a second registration in the same group. The org URL is stored in
// one canonical spelling (lowercase host and path — GitHub and GitLab paths
// are case-insensitive), so the unique key and the check agree.
func TestOrgRegistrationIsCaseInsensitive(t *testing.T) {
	store, ctx := openSR5Store(t)
	const login = "_avorgcase_probe"
	clean := func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_org_requests WHERE LOWER(org_url) LIKE 'https://github.com/_avorgcase-%'`)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	}
	clean()
	t.Cleanup(clean)
	uid, err := store.UpsertOAuthUser(ctx, OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserAdmin(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	gid, err := store.CreateUserGroup(ctx, uid, "org case probe")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://github.com/_AVORGCASE-Org", "github.com/_avorgcase-org/", "HTTPS://GitHub.com/_avorgcase-ORG"} {
		if _, err := store.AddOrgToGroup(ctx, uid, gid, u, "", ""); err != nil {
			t.Fatalf("AddOrgToGroup(%q): %v", u, err)
		}
	}
	var n int
	var stored string
	if err := store.pool.QueryRow(ctx, `SELECT count(*), min(org_url) FROM aveloxis_ops.user_org_requests WHERE group_id = $1`, gid).Scan(&n, &stored); err != nil {
		t.Fatal(err)
	}
	if n != 1 || stored != "https://github.com/_avorgcase-org" {
		t.Errorf("%d registrations, first %q; want 1 registration stored as https://github.com/_avorgcase-org", n, stored)
	}
}

// TestLegacyMixedCaseOrgRowIsNotDuplicated pins the write side of follow-up
// 3 (batch 5 review round 1): CanonicalOrgURL lowercases a NEW spelling, but
// the registration's arbiter is the exact key, so a row stored mixed-case
// before v0.29.68 plus an identical re-paste — the most common input —
// inserted a second, lowercase row beside it, where the old exact-key
// conflict inserted nothing. Both writers (the admin path and the
// auto-approved non-admin path) go through registerApprovedOrg, which now
// asks the group for the org case-insensitively first. The bridge
// GetUserGroupIDsForOrgURL compares case-insensitively too, so a legacy
// row and a canonical row are both found from a typed spelling.
func TestLegacyMixedCaseOrgRowIsNotDuplicated(t *testing.T) {
	store, ctx := openSR5Store(t)
	const login = "_avorglegacy_probe"
	clean := func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_org_requests WHERE LOWER(org_url) LIKE 'https://github.com/_avorglegacy-%'`)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	}
	clean()
	t.Cleanup(clean)
	uid, err := store.UpsertOAuthUser(ctx, OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	gid, err := store.CreateUserGroup(ctx, uid, "org legacy probe")
	if err != nil {
		t.Fatal(err)
	}
	const legacy = "https://github.com/_AVORGLEGACY-Org"
	// How AddOrgToGroup stored a pasted URL before v0.29.68.
	if _, err := store.pool.Exec(ctx, `INSERT INTO aveloxis_ops.user_org_requests (user_id, group_id, org_url, org_name, platform) VALUES ($1, $2, $3, '_AVORGLEGACY-Org', 'github')`, uid, gid, legacy); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var n int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM aveloxis_ops.user_org_requests WHERE group_id = $1`, gid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The non-admin owner re-pastes it: registered anywhere → auto-approved
	// → registerApprovedOrg.
	if _, err := store.AddOrgToGroup(ctx, uid, gid, legacy, "", ""); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("after a non-admin's identical re-add: %d registrations in the group; want the legacy row alone", n)
	}
	if err := store.SetUserAdmin(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{legacy, "github.com/_avorglegacy-org/"} {
		if _, err := store.AddOrgToGroup(ctx, uid, gid, u, "", ""); err != nil {
			t.Fatalf("AddOrgToGroup(%q): %v", u, err)
		}
	}
	if n := count(); n != 1 {
		t.Errorf("after an admin's re-adds in two spellings: %d registrations in the group; want the legacy row alone", n)
	}
	// The bridge finds the legacy row from the typed spelling, and a
	// canonical row from a mixed-case spelling.
	if ids, err := store.GetUserGroupIDsForOrgURL(ctx, legacy); err != nil || !slices.Contains(ids, gid) {
		t.Errorf("GetUserGroupIDsForOrgURL(legacy spelling) = %v, %v; want the group", ids, err)
	}
	gid2, err := store.CreateUserGroup(ctx, uid, "org canonical probe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddOrgToGroup(ctx, uid, gid2, "https://github.com/_AVORGLEGACY-Other", "", ""); err != nil {
		t.Fatal(err)
	}
	if ids, err := store.GetUserGroupIDsForOrgURL(ctx, "https://github.com/_AVORGLEGACY-Other/"); err != nil || !slices.Contains(ids, gid2) {
		t.Errorf("GetUserGroupIDsForOrgURL(a typed spelling with a trailing slash) = %v, %v; want the group (review round 5)", ids, err)
	}
	if ids, err := store.GetUserGroupIDsForOrgURL(ctx, "https://github.com/_AVORGLEGACY-Other"); err != nil || !slices.Contains(ids, gid2) {
		t.Errorf("GetUserGroupIDsForOrgURL(mixed-case spelling of a canonical row) = %v, %v; want the group", ids, err)
	}
}

// TestAddOrgToGroupRegistersAGroupOnTheConfiguredInstance (round-2 review
// of 0.29.100): an administrator's organisation URL on the configured
// GitLab instance registers as a GitLab group by its full path whatever
// the host name says, and spelled with www. and the default port it is the
// same instance. Without a configured instance the same URL is classified
// by its host and, not being this deployment's GitHub, refused off-host —
// as before.
func TestAddOrgToGroupRegistersAGroupOnTheConfiguredInstance(t *testing.T) {
	store, ctx := openSR5Store(t)
	const login = "_avorginst_probe"
	clean := func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_org_requests WHERE org_url LIKE '%code.example.invalid%/_avorginst-%'`)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	}
	clean()
	t.Cleanup(clean)
	uid, err := store.UpsertOAuthUser(ctx, OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserAdmin(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	gid, err := store.CreateUserGroup(ctx, uid, "org instance probe")
	if err != nil {
		t.Fatal(err)
	}
	const inst = "https://code.example.invalid"
	if _, err := store.AddOrgToGroup(ctx, uid, gid, "https://code.example.invalid/_avorginst-group/sub", "", ""); !errors.Is(err, ErrOrgOffGitHubHost) {
		t.Fatalf("without a configured instance = %v; want ErrOrgOffGitHubHost (the host decides, as before)", err)
	}
	out, err := store.AddOrgToGroup(ctx, uid, gid, "https://www.code.example.invalid:443/_avorginst-group/sub", "", inst)
	if err != nil || !out.Registered {
		t.Fatalf("on the configured instance = %+v, %v; want registered", out, err)
	}
	var plat, name string
	if err := store.pool.QueryRow(ctx, `SELECT platform, org_name FROM aveloxis_ops.user_org_requests WHERE group_id = $1`, gid).Scan(&plat, &name); err != nil {
		t.Fatal(err)
	}
	if plat != "gitlab" || name != "_avorginst-group/sub" {
		t.Errorf("registered as %s %q; want gitlab \"_avorginst-group/sub\" (the full path, N14)", plat, name)
	}
	// The GitHub twin (round-3 review): a GitLab group on ANOTHER instance,
	// or any GitLab group when no instance is configured, can never be
	// enumerated, so it is refused at the add instead of registering and
	// being skipped by every scan.
	before := 0
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM aveloxis_ops.user_org_requests WHERE group_id = $1`, gid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url, base string
		want      error
	}{
		{"https://gitlab.com/_avorginst-elsewhere", inst, ErrOrgOffGitLabInstance},
		{"https://gitlab.com/_avorginst-elsewhere", "", ErrOrgOffGitLabInstance},
		// Without an instance a host that does not say gitlab is labelled
		// github, as before, and refused off the GitHub host.
		{"https://code.example.invalid/_avorginst-group/other", "", ErrOrgOffGitHubHost},
	} {
		if _, err := store.AddOrgToGroup(ctx, uid, gid, tc.url, "", tc.base); !errors.Is(err, tc.want) {
			t.Errorf("AddOrgToGroup(%q, instance %q) = %v; want %v", tc.url, tc.base, err, tc.want)
		}
	}
	var after int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM aveloxis_ops.user_org_requests WHERE group_id = $1`, gid).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("an off-instance GitLab group registered a row (%d → %d)", before, after)
	}
}

// TestAddReposToGroupClassifiesTheConfiguredInstance (round-3 review of
// 0.29.100): an administrator's paste of a repository URL under the
// configured GitLab instance's web base creates the repository as GitLab
// (API collection) whatever the host name says — the org scan of that
// instance records its projects so — while without an instance the same
// URL is a git-only row, as before.
func TestAddReposToGroupClassifiesTheConfiguredInstance(t *testing.T) {
	store, ctx := openSR5Store(t)
	const login = "_avrepoinst_probe"
	clean := func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_repos WHERE repo_id IN (SELECT repo_id FROM aveloxis_data.repos WHERE repo_git LIKE 'https://code.example.invalid/_avrepoinst-%')`)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id IN (SELECT repo_id FROM aveloxis_data.repos WHERE repo_git LIKE 'https://code.example.invalid/_avrepoinst-%')`)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_data.repos WHERE repo_git LIKE 'https://code.example.invalid/_avrepoinst-%'`)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_request_items WHERE request_id IN (SELECT request_id FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1))`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	}
	clean()
	t.Cleanup(clean)
	uid, err := store.UpsertOAuthUser(ctx, OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserAdmin(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	gid, err := store.CreateUserGroup(ctx, uid, "repo instance probe")
	if err != nil {
		t.Fatal(err)
	}
	stored := func(url string) (platform int16, owner, name string) {
		t.Helper()
		if err := store.pool.QueryRow(ctx, `SELECT platform_id, repo_owner, repo_name FROM aveloxis_data.repos WHERE repo_git = $1`, url).Scan(&platform, &owner, &name); err != nil {
			t.Fatalf("repos row for %s: %v", url, err)
		}
		return platform, owner, name
	}
	const inst = "https://code.example.invalid"
	hinted := "https://code.example.invalid/_avrepoinst-g/p"
	if out, err := store.AddReposToGroup(ctx, uid, gid, []string{hinted}, 0, inst); err != nil || out.Enqueued != 1 {
		t.Fatalf("add on the configured instance = %+v, %v; want one enqueued", out, err)
	}
	if p, o, n := stored(hinted); p != int16(model.PlatformGitLab) || o != "_avrepoinst-g" || n != "p" {
		t.Errorf("stored as platform %d %q/%q; want GitLab %q/%q", p, o, n, "_avrepoinst-g", "p")
	}
	plain := "https://code.example.invalid/_avrepoinst-g/q"
	if out, err := store.AddReposToGroup(ctx, uid, gid, []string{plain}, 0, ""); err != nil || out.Enqueued != 1 {
		t.Fatalf("add without an instance = %+v, %v; want one enqueued", out, err)
	}
	if p, _, _ := stored(plain); p != int16(model.PlatformGenericGit) {
		t.Errorf("without an instance the row is platform %d; want generic git (as before)", p)
	}
}
