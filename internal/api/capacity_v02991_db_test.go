// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/db"
)

// setQuotaForTest sets one quota and restores it at cleanup.
func setQuotaForTest(t *testing.T, store *db.PostgresStore, name string, allowed int, mode capacity.Mode) {
	t.Helper()
	ctx := context.Background()
	before, err := store.GetCapacityQuotas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if q, ok := before[name]; ok {
			_ = store.SetCapacityQuota(context.Background(), name, q.Allowed, q.Mode, 0)
		}
	})
	if err := store.SetCapacityQuota(ctx, name, allowed, mode, 0); err != nil {
		t.Fatal(err)
	}
}

// v0.29.91 (GUI review, Med): sign-out went through the rate quotas, so a
// session whose enforced requests_per_hour was spent got a 429, the token
// was never deleted, and it stayed valid for up to 30 days. Sign-out is
// never refused (and not counted), end to end through the middleware.
func TestLogoutIsNeverRefusedBySpentQuota(t *testing.T) {
	s, store, _, owner := apiTokenAdminServer(t)
	ctx := context.Background()
	setQuotaForTest(t, store, capacity.QuotaRequestsPerHour, 1, capacity.Enforce)
	fresh, err := NewWithOptions(store, s.logger, Options{RateLimitRPS: 1000, RateLimitBurst: 1000, RateLimitDaily: 100000})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); fresh.limiter.policy.sessionQuotas(owner, false)[0].Mode != capacity.Enforce; {
		if time.Now().After(deadline) {
			t.Fatal("the process never read the policy")
		}
		time.Sleep(5 * time.Millisecond)
	}
	tok, err := store.CreateSessionToken(ctx, owner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteSessionToken(context.Background(), tok) })
	call := func(method, path string) int {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = "203.0.113.91:1"
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		fresh.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if code := call(http.MethodGet, "/api/v1/me"); code != http.StatusOK {
		t.Fatalf("request 1 of 1 = %d", code)
	}
	if code := call(http.MethodGet, "/api/v1/me"); code != http.StatusTooManyRequests {
		t.Fatalf("request 2 of 1 = %d; want 429 (the quota is spent)", code)
	}
	if code := call(http.MethodPost, "/api/v1/auth/logout"); code != http.StatusNoContent {
		t.Fatalf("sign-out with a spent quota = %d; want 204", code)
	}
	if _, err := store.ValidateSessionToken(ctx, tok); !errors.Is(err, db.ErrInvalidSessionToken) {
		t.Fatalf("after sign-out the token resolves: %v; want ErrInvalidSessionToken", err)
	}
}

// v0.29.91 (whole-PR review, Med): the Comparisons auto-add linked an
// organization's repositories one at a time, so a refusal left the ones
// before it linked. One decision now: an organization with more new
// repositories than room links none.
func TestComparisonsAutoAddOfAnOrganizationIsAllOrNothing(t *testing.T) {
	s, store, _, owner := apiTokenAdminServer(t)
	ctx := context.Background()
	setQuotaForTest(t, store, capacity.QuotaReposPerAccount, 1000, capacity.Enforce)
	two := 2
	if err := store.SetCapacityOverride(ctx, db.CapacityOverride{UserID: owner, ReposAllowed: &two}, 0); err != nil {
		t.Fatal(err)
	}
	login := fmt.Sprintf("_avcmporg%d", time.Now().UnixNano())
	t.Cleanup(func() {
		c := context.Background()
		_, _ = store.Pool().Exec(c, `DELETE FROM aveloxis_ops.user_repos WHERE group_id IN (SELECT group_id FROM aveloxis_ops.user_groups WHERE user_id = $1)`, owner)
		_, _ = store.Pool().Exec(c, `DELETE FROM aveloxis_ops.user_groups WHERE user_id = $1`, owner)
		_, _ = store.Pool().Exec(c, `DELETE FROM aveloxis_ops.user_capacity WHERE user_id = $1`, owner)
		_, _ = store.Pool().Exec(c, `DELETE FROM aveloxis_data.repos WHERE repo_owner = $1`, login)
	})
	for i := 0; i < 3; i++ {
		if _, err := store.Pool().Exec(ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_name, repo_owner, platform_id)
			VALUES ($1, $2, $3, 1)`, fmt.Sprintf("https://github.com/%s/r%d", login, i), fmt.Sprintf("r%d", i), login); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/compare", nil)
	r = r.WithContext(context.WithValue(r.Context(), authCtxKey{}, authInfo{UserID: owner, Scope: map[int64]bool{}}))
	w := httptest.NewRecorder()
	if ids, _, ok := s.resolveEntityRepos(w, r, entity{Kind: "org", Host: "github.com", Login: login, Label: login}); ok {
		t.Fatalf("an organization of 3 into room for 2 = %v, ok; want the refusal", ids)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusForbidden || body["quota"] != capacity.QuotaReposPerAccount {
		t.Fatalf("= %d %s; want the repos_per_account refusal", w.Code, w.Body.String())
	}
	if reach, err := store.AccountReach(ctx, owner); err != nil || reach != 0 {
		t.Fatalf("a refused organization linked %d repositories (%v); want 0 (all or nothing)", reach, err)
	}
	// The slot comes back: nothing was linked (settleAutoAdd).
	if used := s.autoAdds.quotaMeter().Peek(capacity.Subject{Kind: capacity.KindAccount, ID: strconv.Itoa(owner)}, autoAddQuota).Used; used != 0 {
		t.Errorf("a refused auto-add spent %d slot(s); want 0", used)
	}
}

// v0.29.91 (whole-PR review): removing a repository that is not in the
// group answered "removed": true. Now 404 "not in that group" (the GUI's
// lib/group.js matches that text and refreshes its list).
func TestGroupRemoveOfAnAbsentRepositoryIs404(t *testing.T) {
	s, store, _, owner := apiTokenAdminServer(t)
	ctx := context.Background()
	gid, err := store.CreateUserGroup(ctx, owner, "remove-404")
	if err != nil {
		t.Fatal(err)
	}
	var repoID int64
	if err := store.Pool().QueryRow(ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_name, repo_owner, platform_id)
		VALUES ($1, 'r', '_avrm404', 1) RETURNING repo_id`, fmt.Sprintf("https://github.com/_avrm404/r%d", time.Now().UnixNano())).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = store.Pool().Exec(c, `DELETE FROM aveloxis_ops.user_repos WHERE group_id = $1`, gid)
		_, _ = store.Pool().Exec(c, `DELETE FROM aveloxis_ops.user_groups WHERE group_id = $1`, gid)
		_, _ = store.Pool().Exec(c, `DELETE FROM aveloxis_data.repos WHERE repo_id = $1`, repoID)
	})
	if _, err := store.Pool().Exec(ctx, `INSERT INTO aveloxis_ops.user_repos (group_id, repo_id) VALUES ($1, $2)`, gid, repoID); err != nil {
		t.Fatal(err)
	}
	remove := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.SetPathValue("groupID", strconv.FormatInt(gid, 10))
		r.SetPathValue("repoID", strconv.FormatInt(repoID, 10))
		r = r.WithContext(context.WithValue(r.Context(), authCtxKey{}, authInfo{UserID: owner, Scope: map[int64]bool{}}))
		w := httptest.NewRecorder()
		s.handleGroupRemoveRepo(w, r)
		return w
	}
	if w := remove(); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"removed":true`) {
		t.Fatalf("the first remove = %d %s", w.Code, w.Body.String())
	}
	if w := remove(); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "not in that group") {
		t.Fatalf("removing it again = %d %s; want 404 not in that group", w.Code, w.Body.String())
	}
}
