// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"errors"
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

// TestGateWithFailedAdminLookupIs500NotARefusal is the RUNTIME contract of
// the per-request privilege decision (0.29.102, D-1; it replaces the login-
// time version of batch-2 round 13, whose fault — the users row's admin
// column NULL, so IsUserAdmin's Scan fails while the row persists — now
// lands on the gate, since the login reads no privilege any more). A
// signed-in user whose row cannot answer gets a 500 and an ERROR log line
// on every gated route: never the handler, never a 403 (SR-5: a lookup
// error is not "no").
func TestGateWithFailedAdminLookupIs500NotARefusal(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	store, err := db.NewPostgresStore(ctx, dsn, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	pool := store.Pool()
	const login = "_avweb_admin_lookup_fault"
	clean := func() {
		if _, err := pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login); err != nil {
			t.Logf("cleanup of the probe user: %v", err)
		}
		if _, err := pool.Exec(ctx, `ALTER TABLE aveloxis_ops.users ALTER COLUMN admin SET NOT NULL`); err != nil {
			t.Logf("restoring users.admin NOT NULL: %v", err)
		}
	}
	clean()
	t.Cleanup(clean)
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE aveloxis_ops.users ALTER COLUMN admin DROP NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE aveloxis_ops.users SET admin = NULL WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	s := New(store, config.WebConfig{DevMode: true}, "", logger)
	plantSession(t, s, "fault", &Session{UserID: uid, LoginName: login, ExpiresAt: time.Now().Add(time.Hour)})
	h := s.Handler()
	for _, path := range []string{"/dashboard", "/monitor"} {
		logs.Reset()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: s.cookieName("aveloxis_session"), Value: "fault"})
		csrf(r) // the session's form token (stage 2f)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("GET %s with an unreadable admin flag = %d %q; want 500", path, w.Code, strings.TrimSpace(w.Body.String()))
		}
		if strings.Contains(w.Body.String(), "admin") || strings.Contains(w.Body.String(), "NULL") {
			t.Errorf("GET %s: the body carries the store's text: %q", path, w.Body.String())
		}
		if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "session lookup") {
			t.Errorf("GET %s: the failed lookup was not logged at ERROR:\n%s", path, logs.String())
		}
	}
}

// TestDemotionTakesEffectOnTheNextRequest (D-1, the production wiring):
// an administrator's open session loses /monitor at the request after the
// user-management page demotes the account, with no new login; the real
// store answers the gate.
func TestDemotionTakesEffectOnTheNextRequest(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	store, err := db.NewPostgresStore(ctx, dsn, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	const login = "_avweb_demotion_next_request"
	_, _ = store.Pool().Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.users WHERE user_id = $1`, uid)
	})
	if err := store.SetUserAdmin(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	s := New(store, config.WebConfig{DevMode: true}, "", logger)
	token, err := s.createSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func() int {
		r := httptest.NewRequest(http.MethodGet, "/monitor", nil)
		r.AddCookie(&http.Cookie{Name: s.cookieName("aveloxis_session"), Value: token})
		csrf(r) // the session's form token (stage 2f)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("an administrator's GET /monitor = %d; want 200", code)
	}
	if err := store.SetUserAdmin(ctx, uid, false); err != nil {
		t.Fatal(err)
	}
	if code := get(); code != http.StatusForbidden {
		t.Errorf("after the demotion the same session's GET /monitor = %d; want 403 at the next request", code)
	}
}

// TestAuthTokenDiesWithTheWebSession (B4 at the web layer, 0.29.102): the
// Bearer token POST /auth/token answers is minted FROM the cookie's
// session, so POST /logout ends it with the session — sign-out means
// signed out everywhere, the SPA's token included.
func TestAuthTokenDiesWithTheWebSession(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	store, err := db.NewPostgresStore(ctx, dsn, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	const login = "_avweb_token_dies_with_session"
	_, _ = store.Pool().Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.users WHERE user_id = $1`, uid)
	})
	s := New(store, config.WebConfig{DevMode: true}, "", logger)
	cookieValue, err := s.createSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	post := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.AddCookie(&http.Cookie{Name: s.cookieName("aveloxis_session"), Value: cookieValue})
		csrf(r) // the session's form token (stage 2f)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := post("/auth/token")
	if w.Code != http.StatusOK {
		t.Fatalf("POST /auth/token = %d %q", w.Code, w.Body.String())
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &minted); err != nil || minted.Token == "" {
		t.Fatalf("token body %q: %v", w.Body.String(), err)
	}
	if got, err := store.ValidateSessionToken(ctx, minted.Token); err != nil || got != uid {
		t.Fatalf("the minted token does not validate: (%d, %v)", got, err)
	}
	if w := post("/logout"); w.Code != http.StatusFound {
		t.Fatalf("POST /logout = %d", w.Code)
	}
	if _, err := store.ValidateSessionToken(ctx, minted.Token); !errors.Is(err, db.ErrInvalidSessionToken) {
		t.Errorf("after sign-out the token minted from the session still validates: %v", err)
	}
	if _, err := store.LookupWebSession(ctx, cookieValue); !errors.Is(err, db.ErrNoWebSession) {
		t.Errorf("after sign-out the session still resolves: %v", err)
	}
}
