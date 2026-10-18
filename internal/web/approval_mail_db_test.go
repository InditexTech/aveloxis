// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/mailer"
)

// TestAdminAddRequestReapproveResumesProcessing (AVELOXIS_TEST_DB): through
// the admin route, re-approving a repos request whose first approval never
// finished processing resumes it (round-11 review).
func TestAdminAddRequestReapproveResumesProcessing(t *testing.T) {
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
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const login = "_avweb_reapprove_probe"
	const repoURL = "https://github.com/_avweb-reapprove-owner/_avweb-reapprove-repo"
	clean := func() {
		p := store.Pool()
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.user_repos WHERE group_id IN (SELECT group_id FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1))`, login)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id IN (SELECT repo_id FROM aveloxis_data.repos WHERE repo_git LIKE $1 || '%')`, repoURL)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_data.repos WHERE repo_git LIKE $1 || '%'`, repoURL)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.user_org_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_request_items WHERE request_id IN (SELECT request_id FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1))`, login)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	}
	clean()
	t.Cleanup(clean)
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE aveloxis_ops.users SET email = 'requester@example.com' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	gid, err := store.CreateUserGroup(ctx, uid, "web reapprove probe")
	if err != nil {
		t.Fatal(err)
	}
	out, err := store.AddReposToGroup(ctx, uid, gid, []string{repoURL}, 0, "")
	if err != nil || out.RequestID == 0 {
		t.Fatalf("AddReposToGroup = %+v, %v; want a pending request", out, err)
	}
	if _, changed, err := store.DecideAddRequest(ctx, out.RequestID, uid, true, "", ""); err != nil || !changed {
		t.Fatalf("first approval: changed=%v err=%v", changed, err)
	}
	unprocessed := func() int {
		var n int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM aveloxis_ops.collection_add_request_items WHERE request_id = $1 AND repo_id IS NULL`, out.RequestID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if unprocessed() != 1 {
		t.Fatalf("fixture: %d unprocessed items, want 1", unprocessed())
	}

	sent := make(chan string, 4)
	m := mailer.New(mailer.Config{GmailUser: "ops@example.com", GmailAppPassword: "abcdefghijklmnop"}, nil).
		WithSendFunc(func(_ string, _ smtp.Auth, _ string, to []string, _ []byte) error {
			sent <- strings.Join(to, ",")
			return nil
		})
	logs := &lockedBuffer{}
	s := New(store, config.WebConfig{}, "", slog.New(slog.NewTextHandler(logs, nil))).WithMailer(m)
	// A second account decides: the admin service reads the administrator's
	// row for every decision (stage 1 of summary/59), and the requester
	// must stay a non-admin for its adds to pend.
	adminUID := seedAdminAccount(t, store, login+"-admin")
	s.sessions["admin"] = &Session{UserID: adminUID, LoginName: login + "-admin", IsAdmin: true, ExpiresAt: time.Now().Add(time.Hour)}
	r := httptest.NewRequest(http.MethodPost, "/admin/add-requests/"+strconv.FormatInt(out.RequestID, 10)+"/approve", nil)
	r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: "admin"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("re-approve = %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for unprocessed() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("re-approving did not resume processing within 10s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Resuming is not a new decision: the requester is not mailed again.
	select {
	case to := <-sent:
		t.Errorf("re-approving mailed the decision to %q again", to)
	case <-time.After(500 * time.Millisecond):
	}

	// Approving a REJECTED request starts no processing pass (which would
	// log a false "processing … failed" WARN).
	rejected, err := store.AddReposToGroup(ctx, uid, gid, []string{repoURL + "-rejected"}, 0, "")
	if err != nil || rejected.RequestID == 0 {
		t.Fatalf("AddReposToGroup = %+v, %v", rejected, err)
	}
	if _, changed, err := store.DecideAddRequest(ctx, rejected.RequestID, uid, false, "", ""); err != nil || !changed {
		t.Fatalf("reject: changed=%v err=%v", changed, err)
	}
	rr := httptest.NewRequest(http.MethodPost, "/admin/add-requests/"+strconv.FormatInt(rejected.RequestID, 10)+"/approve", nil)
	rr.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: "admin"})
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, rr)
	if rw.Code != http.StatusFound {
		t.Fatalf("approving a rejected request = %d %s", rw.Code, rw.Body.String())
	}
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(logs.String(), "processing approved add-request failed") {
		t.Errorf("approving a rejected request started a processing pass; log:\n%s", logs.String())
	}
}

// TestAdminOrgReapproveDoesNotRescan (AVELOXIS_TEST_DB): an org approval
// registers the org once, for serve to list (stage 1 of summary/59, C2:
// the web process scans nothing — it holds no forge key); re-approving it
// (registration present, nothing to complete) neither registers again nor
// mails again (round-12 review: nothing pinned "act only on a real
// decision"). v0.29.71: it used a rejected group, which no longer takes an
// approval.
func TestAdminOrgReapproveDoesNotRescan(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := db.NewPostgresStore(ctx, dsn, discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const login = "_avweb_org_rescan_probe"
	const orgURL = "https://github.com/_avweb-org-rescan-probe"
	p := store.Pool()
	clean := func() {
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.user_org_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_request_items WHERE request_id IN (SELECT request_id FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1))`, login)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_requests WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = p.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	}
	clean()
	t.Cleanup(clean)
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE aveloxis_ops.users SET email = 'requester@example.com' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	gid, err := store.CreateUserGroup(ctx, uid, "web org rescan probe")
	if err != nil {
		t.Fatal(err)
	}
	out, err := store.AddOrgToGroup(ctx, uid, gid, orgURL, "", "")
	if err != nil || out.RequestID == 0 {
		t.Fatalf("AddOrgToGroup = %+v, %v; want a pending request", out, err)
	}
	sent := make(chan string, 8)
	m := mailer.New(mailer.Config{GmailUser: "ops@example.com", GmailAppPassword: "abcdefghijklmnop"}, nil).
		WithSendFunc(func(_ string, _ smtp.Auth, _ string, to []string, _ []byte) error {
			sent <- strings.Join(to, ",")
			return nil
		})
	logs := &lockedBuffer{}
	s := New(store, config.WebConfig{}, "", slog.New(slog.NewTextHandler(logs, nil))).WithMailer(m)
	// A second account decides: the admin service reads the administrator's
	// row for every decision (stage 1 of summary/59), and the requester
	// must stay a non-admin for its adds to pend.
	adminUID := seedAdminAccount(t, store, login+"-admin")
	s.sessions["admin"] = &Session{UserID: adminUID, LoginName: login + "-admin", IsAdmin: true, ExpiresAt: time.Now().Add(time.Hour)}
	approve := func() {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/admin/add-requests/"+strconv.FormatInt(out.RequestID, 10)+"/approve", nil)
		r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: "admin"})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusFound {
			t.Fatalf("approve = %d %s", w.Code, w.Body.String())
		}
	}
	registrations := func() (n int, unscanned bool) {
		t.Helper()
		if err := p.QueryRow(ctx, `SELECT COUNT(*), bool_and(last_scanned IS NULL) FROM aveloxis_ops.user_org_requests WHERE group_id = $1`, gid).Scan(&n, &unscanned); err != nil {
			t.Fatal(err)
		}
		return n, unscanned
	}

	approve()
	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatal("the approval mailed nobody")
	}
	if n, unscanned := registrations(); n != 1 || !unscanned {
		t.Fatalf("after the approval: %d registrations (unscanned %v); want one, never scanned, for serve's demand probe", n, unscanned)
	}
	approve()
	select {
	case to := <-sent:
		t.Errorf("re-approving mailed %q again", to)
	case <-time.After(500 * time.Millisecond):
	}
	if n, unscanned := registrations(); n != 1 || !unscanned {
		t.Errorf("after re-approving: %d registrations (unscanned %v); want the one, still never scanned", n, unscanned)
	}
	if strings.Contains(logs.String(), "org scan") || strings.Contains(logs.String(), "scanning repos") {
		t.Errorf("the web process scanned or tried to: the org is listed by serve; log:\n%s", logs.String())
	}
}

// seedAdminAccount creates an administrator account for a decision and
// removes it at the end (stage 1 of summary/59: the admin service reads
// the row, so a session's IsAdmin flag alone decides nothing).
func seedAdminAccount(t *testing.T, store *db.PostgresStore, login string) int {
	t.Helper()
	ctx := context.Background()
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE aveloxis_ops.users SET admin = TRUE WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.users WHERE user_id = $1`, uid)
	})
	return uid
}
