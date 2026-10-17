// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/db"
)

// v0.29.91 whole-PR follow-up: the built-in GUI's /monitor (the whole
// collection queue) and POST /monitor/prioritize/{repoID} (push any
// repository to the top) were gated only by requireAuth — any signed-in
// account could read the queue and boost any repository. The API's
// equivalent is administrator-only. Both routes now are: a non-admin
// session gets 403 and the queue is unchanged; an administrator succeeds.
func TestMonitorRoutesAreAdministratorOnly(t *testing.T) {
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
	owner := fmt.Sprintf("_avmongate%d", time.Now().UnixNano())
	var repoID int64
	if err := store.Pool().QueryRow(ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id, repo_group_id)
		VALUES ($1, $2, 'r', 1, 1) RETURNING repo_id`, "https://github.com/"+owner+"/r", owner).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = store.Pool().Exec(c, `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = $1`, repoID)
		_, _ = store.Pool().Exec(c, `DELETE FROM aveloxis_data.repos WHERE repo_id = $1`, repoID)
	})
	if _, err := store.Pool().Exec(ctx, `INSERT INTO aveloxis_ops.collection_queue (repo_id, status, priority, due_at) VALUES ($1, 'queued', 100, NOW() + interval '7 days')`, repoID); err != nil {
		t.Fatal(err)
	}
	priority := func() int {
		var p int
		if err := store.Pool().QueryRow(ctx, `SELECT priority FROM aveloxis_ops.collection_queue WHERE repo_id = $1`, repoID).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	s := New(store, config.WebConfig{DevMode: true}, nil, "", logger)
	s.sessions["user-tok"] = &Session{UserID: 1 << 30, LoginName: "user", ExpiresAt: time.Now().Add(time.Hour)}
	s.sessions["admin-tok"] = &Session{UserID: 1<<30 + 1, LoginName: "admin", IsAdmin: true, ExpiresAt: time.Now().Add(time.Hour)}
	h := s.Handler()
	call := func(method, path, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: tok})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	boost := fmt.Sprintf("/monitor/prioritize/%d", repoID)
	if w := call(http.MethodGet, "/monitor", "user-tok"); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin's GET /monitor = %d; want 403", w.Code)
	}
	if w := call(http.MethodPost, boost, "user-tok"); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin's prioritize = %d; want 403", w.Code)
	}
	if got := priority(); got != 100 {
		t.Fatalf("a non-admin changed the queue: priority %d, want 100", got)
	}
	if w := call(http.MethodGet, "/monitor", "admin-tok"); w.Code != http.StatusOK {
		t.Errorf("an administrator's GET /monitor = %d; want 200", w.Code)
	}
	if w := call(http.MethodPost, boost, "admin-tok"); w.Code != http.StatusFound {
		t.Errorf("an administrator's prioritize = %d; want 302", w.Code)
	}
	if got := priority(); got != 0 {
		t.Errorf("after an administrator's boost the priority is %d; want 0", got)
	}
}

// The Monitor link is shown to administrators only (a non-admin's link
// would only lead to a 403).
func TestMonitorLinkIsShownToAdministratorsOnly(t *testing.T) {
	s := New(nil, config.WebConfig{}, nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, page := range []string{"dashboard", "group", "repo_detail", "compare"} {
		for _, admin := range []bool{false, true} {
			var b strings.Builder
			_ = s.tmpl.ExecuteTemplate(&b, page, map[string]any{"Session": &Session{LoginName: "u", IsAdmin: admin}})
			if got := strings.Contains(b.String(), `href="/monitor"`); got != admin {
				t.Errorf("%s page, admin=%v: Monitor link shown = %v", page, admin, got)
			}
		}
	}
}
