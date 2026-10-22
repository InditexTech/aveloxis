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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/db"
)

// The proxy's Bearer is the store's: minted from the real web session
// (kind "api", parent = the session), valid to the api's validator for
// the session's user, and revoked by sign-out (0.29.110, stage 4).
func TestAPIProxyBearerIsTheSessionsOwnToken(t *testing.T) {
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
	login := fmt.Sprintf("_avproxy%d", time.Now().UnixNano())
	userID, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.user_session_tokens WHERE user_id = $1`, userID)
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.users WHERE user_id = $1`, userID)
	})
	api := newRecordedAPI(t)
	s := New(store, config.WebConfig{DevMode: true, APIInternalURL: api.srv.URL}, "", logger)
	raw, err := store.CreateWebSession(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if w := proxied(s, raw, nil); w.Code != http.StatusOK {
		t.Fatalf("proxied request = %d\n%s", w.Code, w.Body.String())
	}
	auth, _ := api.calls()
	bearer := strings.TrimPrefix(auth[0], "Bearer ")
	if bearer == "" || bearer == auth[0] {
		t.Fatalf("the api saw %q; want a Bearer", auth[0])
	}
	if uid, err := store.ValidateSessionToken(ctx, bearer); err != nil || uid != userID {
		t.Errorf("the api's validator answers %d, %v for the proxy's Bearer; want the session's user %d", uid, err, userID)
	}
	var parentIsSession, expiresWithParent bool
	if err := store.Pool().QueryRow(ctx, `SELECT c.parent_token = p.token, c.expiration <= p.expiration
		FROM aveloxis_ops.user_session_tokens c JOIN aveloxis_ops.user_session_tokens p ON p.token = $2
		WHERE c.token = $1`, db.HashToken(bearer), db.HashToken(raw)).Scan(&parentIsSession, &expiresWithParent); err != nil || !parentIsSession || !expiresWithParent {
		t.Errorf("the minted token must be the web session's child and expire no later than it (parent %v, expires-with %v, %v)", parentIsSession, expiresWithParent, err)
	}
	// The SPA's exchange is not capped at the session: a /auth/token token
	// keeps its own 30-day life past the session on purpose (the documented
	// contract; a first cut of 0.29.112 capped every child and was withdrawn).
	exchanged, err := store.MintTokenForWebSession(ctx, raw, time.Now().Add(db.DefaultSessionTokenLifetime))
	if err != nil {
		t.Fatal(err)
	}
	var outlives bool
	if err := store.Pool().QueryRow(ctx, `SELECT c.expiration > p.expiration FROM aveloxis_ops.user_session_tokens c JOIN aveloxis_ops.user_session_tokens p ON p.token = $2 WHERE c.token = $1`, db.HashToken(exchanged), db.HashToken(raw)).Scan(&outlives); err != nil || !outlives {
		t.Errorf("the /auth/token exchange must keep its 30-day life past the 24-hour session (outlives %v, %v)", outlives, err)
	}
	// Sign-out: the store revokes the child and the process forgets it.
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r.AddCookie(&http.Cookie{Name: s.cookieName("aveloxis_session"), Value: raw})
	csrf(r)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("logout = %d", w.Code)
	}
	if _, err := store.ValidateSessionToken(ctx, bearer); !errors.Is(err, db.ErrInvalidSessionToken) {
		t.Errorf("after sign-out the proxy's Bearer validates as %v; want revoked", err)
	}
	if s.proxyTokens.size() != 0 {
		t.Errorf("the process still holds %d token(s) after sign-out", s.proxyTokens.size())
	}
	if w := proxied(s, raw, nil); w.Code != http.StatusFound {
		t.Errorf("a proxied request after sign-out = %d; want the login redirect", w.Code)
	}
}
