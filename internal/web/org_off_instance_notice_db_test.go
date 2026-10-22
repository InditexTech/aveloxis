// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/db"
)

// TestOrgErrorFlagNamesTheForge (round-4 review of 0.29.100): the group
// page's notice for an organization off the host names the forge the
// refusal came from — the GitLab twin is "gitlab_host", never "host".
func TestOrgErrorFlagNamesTheForge(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{db.ErrOrgOffGitHubHost, "host"},
		{fmt.Errorf("add: %w", db.ErrOrgOffGitHubHost), "host"},
		{db.ErrOrgOffGitLabInstance, "gitlab_host"},
		{fmt.Errorf("add: %w", db.ErrOrgOffGitLabInstance), "gitlab_host"},
		{db.ErrGroupRejected, "rejected"},
		{errors.New("closed pool"), "1"},
	} {
		if got := orgErrorFlag(tc.err); got != tc.want {
			t.Errorf("orgErrorFlag(%v) = %q; want %q", tc.err, got, tc.want)
		}
	}
}

// TestOrgOffTheGitLabInstanceNoticeNamesTheInstance (AVELOXIS_TEST_DB;
// round-4 review of 0.29.100): through the real route, a GitLab group not
// on the configured instance is refused with a notice naming that
// instance — the page said "not on github.com, this deployment's GitHub
// host" — and the GitHub notice no longer claims GitLab groups are
// recognized by their host name.
func TestOrgOffTheGitLabInstanceNoticeNamesTheInstance(t *testing.T) {
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
	const login = "_avweb_glinst_probe"
	pool := store.Pool()
	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_org_requests WHERE org_url LIKE '%_avweb-glinst-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	}
	clean()
	t.Cleanup(clean)
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	gid, err := store.CreateUserGroup(ctx, uid, "gitlab instance notice probe")
	if err != nil {
		t.Fatal(err)
	}
	s := New(store, config.WebConfig{}, "", logger).WithGitLabAPIBase("https://code.example.invalid/api/v4")
	plantSession(t, s, "probe-token", &Session{UserID: uid, LoginName: login, ExpiresAt: time.Now().Add(time.Hour)})
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		r.AddCookie(&http.Cookie{Name: s.cookieName("aveloxis_session"), Value: "probe-token"})
		csrf(r) // the session's form token (stage 2f)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	addOrg := func(orgURL string) *httptest.ResponseRecorder {
		form := url.Values{"group_id": {fmt.Sprint(gid)}, "org_url": {orgURL}}
		r := httptest.NewRequest(http.MethodPost, "/groups/add-org", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return serve(r)
	}
	page := func(location string) string {
		t.Helper()
		w := serve(httptest.NewRequest(http.MethodGet, location, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", location, w.Code)
		}
		return w.Body.String()
	}
	// A gitlab.com group when the instance is code.example.invalid.
	w := addOrg("https://gitlab.com/_avweb-glinst-elsewhere")
	wantLoc := fmt.Sprintf("/groups/%d?org_error=gitlab_host", gid)
	if w.Code != http.StatusFound || w.Header().Get("Location") != wantLoc {
		t.Fatalf("off-instance GitLab group = %d Location %q; want 302 to %q", w.Code, w.Header().Get("Location"), wantLoc)
	}
	body := page(wantLoc)
	if !strings.Contains(body, "this deployment&#39;s GitLab instance (https://code.example.invalid)") || strings.Contains(body, "GitHub host") {
		t.Errorf("the notice must name the GitLab instance, not the GitHub host; page:\n%s", excerpt(body, "That organization"))
	}
	// A GitHub organization off github.com: the GitHub notice, without the
	// old claim about GitLab host names.
	w = addOrg("https://ghe.example.invalid/_avweb-glinst-acme")
	wantLoc = fmt.Sprintf("/groups/%d?org_error=host", gid)
	if w.Code != http.StatusFound || w.Header().Get("Location") != wantLoc {
		t.Fatalf("off-host GitHub org = %d Location %q; want 302 to %q", w.Code, w.Header().Get("Location"), wantLoc)
	}
	body = page(wantLoc)
	if !strings.Contains(body, "this deployment&#39;s GitHub host") || strings.Contains(body, "containing") || strings.Contains(body, "GitLab instance") {
		t.Errorf("the GitHub notice must name the GitHub host only; page:\n%s", excerpt(body, "That organization"))
	}
}

// excerpt returns the line of body holding needle, for a failure message.
func excerpt(body, needle string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return "(no line holds " + needle + ")"
}
