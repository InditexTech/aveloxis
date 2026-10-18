// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/db"
)

// TestDemotingTheLastAdminIsRefusedNotA500 (AVELOXIS_TEST_DB; PR #218 review
// follow-up to C6; stage 1 of summary/59): the store's last-admin refusal
// is a 409 naming the reason, never a 500. Since the admin service reads
// the administrator's row for every operation, a stale session — it says
// admin, the database no longer does — is refused as a non-administrator
// (403) before any write: a demoted administrator's open session loses
// its power at once (eval-2's SR-18 crux), where it used to reach the
// store. The 409 itself is reachable only by a real administrator (the
// concurrent-demotion race, TestSetUserAdminIsAtomic in internal/admin);
// its rendering is pinned on the web table here.
func TestDemotingTheLastAdminIsRefusedNotA500(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	store, err := db.NewPostgresStore(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	pool := store.Pool()

	var target int
	if err := pool.QueryRow(ctx, `INSERT INTO aveloxis_ops.users (login_name, admin) VALUES ('_avweb_last_admin', TRUE) RETURNING user_id`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM aveloxis_ops.users WHERE user_id = $1`, target); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})

	s := New(store, config.WebConfig{}, "", logger)
	// A stale session: it says admin, but the database has no such row.
	s.sessions["stale-admin"] = &Session{UserID: target + 100000, LoginName: "_avweb_stale", IsAdmin: true, ExpiresAt: time.Now().Add(time.Hour)}
	r := httptest.NewRequest(http.MethodPost, "/admin/users/"+strconv.Itoa(target)+"/admin", strings.NewReader(url.Values{"admin": {"false"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: "stale-admin"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("a stale admin session's demotion answered %d %q; want 403", w.Code, strings.TrimSpace(w.Body.String()))
	}
	var still bool
	if err := pool.QueryRow(ctx, `SELECT admin FROM aveloxis_ops.users WHERE user_id = $1`, target).Scan(&still); err != nil || !still {
		t.Errorf("the target was demoted by a stale session (admin=%v, %v)", still, err)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a refusal was logged as a server error:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "not an administrator") {
		t.Errorf("the refusal must be logged:\n%s", logs.String())
	}

	// The last-admin refusal renders as a 409 with its reason, not a 500.
	w = httptest.NewRecorder()
	if !s.refusePage(w, r, db.ErrLastAdmin) {
		t.Fatal("refusePage did not classify db.ErrLastAdmin")
	}
	if w.Code != http.StatusConflict || !strings.Contains(strings.ToLower(w.Body.String()), "last admin") {
		t.Errorf("the last-admin refusal = %d %q; want 409 with the reason", w.Code, strings.TrimSpace(w.Body.String()))
	}
}
