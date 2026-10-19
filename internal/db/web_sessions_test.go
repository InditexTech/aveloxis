// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

func webSessionFixture(t *testing.T) (*PostgresStore, int) {
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
	login := "_avdb_websession_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	uid, err := store.UpsertOAuthUser(ctx, OAuthUserInfo{Login: login, Provider: "gitlab", AvatarURL: "https://a.example/x.png"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = store.pool.Exec(c, `DELETE FROM aveloxis_ops.user_session_tokens WHERE user_id = $1`, uid)
		_, _ = store.pool.Exec(c, `DELETE FROM aveloxis_ops.email_confirmations WHERE user_id = $1`, uid)
		_, _ = store.pool.Exec(c, `DELETE FROM aveloxis_ops.users WHERE user_id = $1`, uid)
	})
	return store, uid
}

// TestWebSessionIsAUsersRowReadPerRequest (0.29.102, stage 2c of
// summary/59, B2/N12/D-1): a web session is a hashed row of
// user_session_tokens of kind "web"; its lookup is ONE read that carries
// the users row's current fields and privilege, so a promotion or a
// demotion shows at the next request. A web session is not a Bearer
// token and a Bearer token is not a web session.
func TestWebSessionIsAUsersRowReadPerRequest(t *testing.T) {
	store, uid := webSessionFixture(t)
	ctx := context.Background()
	raw, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 32 {
		t.Fatalf("the raw session token is too short: %q", raw)
	}
	var stored string
	if err := store.pool.QueryRow(ctx, `SELECT token FROM aveloxis_ops.user_session_tokens WHERE user_id = $1 AND kind = 'web'`, uid).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != hashToken(raw) || strings.Contains(stored, raw) {
		t.Errorf("the stored web session is %q; want hashToken(raw) and never the raw token", stored)
	}
	sess, err := store.LookupWebSession(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserID != uid || sess.Provider != "gitlab" || sess.AvatarURL != "https://a.example/x.png" || sess.IsAdmin || !strings.HasPrefix(sess.LoginName, "_avdb_websession_") {
		t.Errorf("LookupWebSession = %+v; want the users row's fields, not an administrator", sess)
	}
	if sess.ExpiresAt.Before(time.Now().Add(WebSessionLifetime-time.Minute)) || sess.ExpiresAt.After(time.Now().Add(WebSessionLifetime+time.Minute)) {
		t.Errorf("ExpiresAt %v is not WebSessionLifetime from now", sess.ExpiresAt)
	}
	if err := store.SetUserAdmin(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	if sess, err = store.LookupWebSession(ctx, raw); err != nil || !sess.IsAdmin {
		t.Errorf("after the promotion LookupWebSession = (%+v, %v); want IsAdmin at the next read", sess, err)
	}
	if _, err := store.ValidateSessionToken(ctx, raw); !errors.Is(err, ErrInvalidSessionToken) {
		t.Errorf("a web session validated as a Bearer token: %v", err)
	}
	bearer, err := store.CreateSessionToken(ctx, uid, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupWebSession(ctx, bearer); !errors.Is(err, ErrNoWebSession) {
		t.Errorf("a Bearer token looked up as a web session: %v", err)
	}
	if _, err := store.LookupWebSession(ctx, "nope"); !errors.Is(err, ErrNoWebSession) {
		t.Errorf("an unknown token: %v; want ErrNoWebSession", err)
	}
	if err := store.DeleteWebSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupWebSession(ctx, raw); !errors.Is(err, ErrNoWebSession) {
		t.Errorf("after DeleteWebSession the session still resolves: %v", err)
	}
	if err := store.DeleteWebSession(ctx, raw); err != nil {
		t.Errorf("deleting a gone session is not an error (logout twice): %v", err)
	}
}

// TestLogoutRevokesTheTokensMintedFromTheSession (B4): a Bearer token
// /auth/token minted records the web session it came from; signing the
// browser out deletes them with it. A token minted another way, and a
// token whose session merely EXPIRED (the SPA's 30-day token outlives the
// one-day cookie by design), keep working.
func TestLogoutRevokesTheTokensMintedFromTheSession(t *testing.T) {
	store, uid := webSessionFixture(t)
	ctx := context.Background()
	web, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	minted1, err := store.MintTokenForWebSession(ctx, web)
	if err != nil {
		t.Fatal(err)
	}
	minted2, err := store.MintTokenForWebSession(ctx, web)
	if err != nil {
		t.Fatal(err)
	}
	loose, err := store.CreateSessionToken(ctx, uid, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MintTokenForWebSession(ctx, "not-a-session"); !errors.Is(err, ErrNoWebSession) {
		t.Errorf("minting from an unknown session: %v; want ErrNoWebSession", err)
	}
	if _, err := store.MintTokenForWebSession(ctx, loose); !errors.Is(err, ErrNoWebSession) {
		t.Errorf("minting from a Bearer token: %v; want ErrNoWebSession", err)
	}
	for _, tok := range []string{minted1, minted2, loose} {
		if got, err := store.ValidateSessionToken(ctx, tok); err != nil || got != uid {
			t.Fatalf("before logout: validate = (%d, %v)", got, err)
		}
	}
	if err := store.DeleteWebSession(ctx, web); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{minted1, minted2} {
		if _, err := store.ValidateSessionToken(ctx, tok); !errors.Is(err, ErrInvalidSessionToken) {
			t.Errorf("a token minted from the signed-out session still validates: %v", err)
		}
	}
	if got, err := store.ValidateSessionToken(ctx, loose); err != nil || got != uid {
		t.Errorf("the token minted outside the session died with it: (%d, %v)", got, err)
	}
	// An expired session is swept (every mint sweeps); its minted tokens
	// stay, parentless.
	web2, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	minted3, err := store.MintTokenForWebSession(ctx, web2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE aveloxis_ops.user_session_tokens SET expiration = 1 WHERE token = $1`, hashToken(web2)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSessionToken(ctx, uid, time.Hour); err != nil { // the sweep
		t.Fatal(err)
	}
	if _, err := store.LookupWebSession(ctx, web2); !errors.Is(err, ErrNoWebSession) {
		t.Errorf("the expired session was not swept: %v", err)
	}
	if got, err := store.ValidateSessionToken(ctx, minted3); err != nil || got != uid {
		t.Errorf("a token minted from a session that merely expired died: (%d, %v)", got, err)
	}
}

// TestEmailConfirmationTokensAreHashedAtRest (DBS-14): the confirmation
// link's token is stored as hashToken(raw), matched by hash, and a raw
// row an older binary wrote is deleted by the migrate (a link lives 24 h;
// it is never re-hashed — "never re-hash indistinguishable credentials").
func TestEmailConfirmationTokensAreHashedAtRest(t *testing.T) {
	store, uid := webSessionFixture(t)
	ctx := context.Background()
	raw, err := store.CreateEmailConfirmation(ctx, uid, "someone@example.org")
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := store.pool.QueryRow(ctx, `SELECT token FROM aveloxis_ops.email_confirmations WHERE user_id = $1`, uid).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != hashToken(raw) {
		t.Errorf("stored %q; want hashToken(raw) (%q)", stored, hashToken(raw))
	}
	if email, err := store.ConfirmEmailToken(ctx, raw, uid); err != nil || email != "someone@example.org" {
		t.Errorf("ConfirmEmailToken(raw) = (%q, %v); want the address", email, err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO aveloxis_ops.email_confirmations (token, user_id, email, expires_at) VALUES ('rawtokenfromanolderbinary', $1, 'x@example.org', NOW() + interval '1 day')`, uid); err != nil {
		t.Fatal(err)
	}
	var errs []error
	deleteUnhashedEmailConfirmations(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), &errs)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	var n int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM aveloxis_ops.email_confirmations WHERE user_id = $1`, uid).Scan(&n); err != nil || n != 0 {
		t.Errorf("after the migrate %d unhashed rows remain (%v); want 0", n, err)
	}
}

// TestOneSecretTokenMinting: every secret the store mints (web session,
// Bearer token, API token, confirmation link) comes from newSecretToken;
// a second rand.Read is a second place to get entropy and errors wrong.
func TestOneSecretTokenMinting(t *testing.T) {
	src := srctest.StripGoComments(srctest.PackageSource(t, "internal/db", 50))
	if n := strings.Count(src, "rand.Read("); n != 1 {
		t.Errorf("internal/db calls rand.Read %d times; the one site is newSecretBytes", n)
	}
	if !strings.Contains(srctest.PackageFunc(t, "internal/db", "func newSecretBytes("), "rand.Read(") {
		t.Error("newSecretBytes does not read crypto/rand")
	}
	for _, fn := range []string{"func (s *PostgresStore) CreateEmailConfirmation(", "func (s *PostgresStore) CreateAPIToken(", "func (s *PostgresStore) createSessionToken("} {
		if !strings.Contains(srctest.PackageFunc(t, "internal/db", fn), "newSecretToken()") {
			t.Errorf("%s does not mint through newSecretToken", fn)
		}
	}
}

// TestSignOutAndTheMintSerialiseOnTheSessionRow (whole-branch review of
// PR #238, DB-F4): a sign-out and a /auth/token mint from the same session
// can run at once (two tabs). Whichever goes first, the result is the same:
// the session and every token minted from it are gone, and a mint that
// lost the race answers ErrNoWebSession. Before 0.29.106 the logout was one
// statement whose tree delete saw only the children committed before its
// snapshot; a child committed under a deferred FK check after it was set
// parentless by the FK action and lived on for 30 days.
func TestSignOutAndTheMintSerialiseOnTheSessionRow(t *testing.T) {
	store, uid := webSessionFixture(t)
	ctx := context.Background()
	const wait = 300 * time.Millisecond
	countAPITokens := func() int {
		var n int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM aveloxis_ops.user_session_tokens WHERE user_id = $1 AND kind = 'api'`, uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// A mint in flight (its session row key-shared, its child written, not
	// yet committed): the sign-out waits for it and then sweeps the child.
	web, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a committed transaction's rollback is a no-op
	if _, err := tx.Exec(ctx, `SELECT token FROM aveloxis_ops.user_session_tokens WHERE token = $1 FOR KEY SHARE`, hashToken(web)); err != nil {
		t.Fatal(err)
	}
	child := hashToken("in-flight-child")
	now := time.Now().Unix()
	if _, err := tx.Exec(ctx, `INSERT INTO aveloxis_ops.user_session_tokens (token, user_id, created_at, expiration, token_hashed, kind, parent_token)
		VALUES ($1, $2, $3, $4, TRUE, 'api', $5)`, child, uid, now, now+3600, hashToken(web)); err != nil {
		t.Fatal(err)
	}
	logout := make(chan error, 1)
	go func() { logout <- store.DeleteWebSession(ctx, web) }()
	select {
	case err := <-logout:
		t.Fatalf("the sign-out returned (%v) while a mint from the session was in flight; it must wait for the row", err)
	case <-time.After(wait):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-logout:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sign-out did not finish after the mint committed")
	}
	if n := countAPITokens(); n != 0 {
		t.Errorf("%d API token(s) of the user survive the sign-out; the child committed during the sign-out must go with the session", n)
	}

	// A sign-out in flight (the session row locked, not yet committed): the
	// mint waits and then answers ErrNoWebSession, minting nothing.
	web2, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	tx2, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback(ctx) //nolint:errcheck // see above
	if _, err := tx2.Exec(ctx, `SELECT token FROM aveloxis_ops.user_session_tokens WHERE token = $1 FOR UPDATE`, hashToken(web2)); err != nil {
		t.Fatal(err)
	}
	type minted struct {
		token string
		err   error
	}
	mint := make(chan minted, 1)
	go func() {
		tok, err := store.MintTokenForWebSession(ctx, web2)
		mint <- minted{tok, err}
	}()
	select {
	case m := <-mint:
		t.Fatalf("the mint returned (%q, %v) while the sign-out held the session row; it must wait", m.token, m.err)
	case <-time.After(wait):
	}
	if _, err := tx2.Exec(ctx, deleteSessionTreeSQL("token = $1 AND kind = $2"), hashToken(web2), sessionKindWeb); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-mint:
		if !errors.Is(m.err, ErrNoWebSession) {
			t.Errorf("a mint that lost the race to the sign-out = (%q, %v); want ErrNoWebSession", m.token, m.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the mint did not finish after the sign-out committed")
	}
	if n := countAPITokens(); n != 0 {
		t.Errorf("%d API token(s) of the user exist after a sign-out that beat the mint; want 0", n)
	}
}
