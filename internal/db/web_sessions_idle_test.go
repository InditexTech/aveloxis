// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 0.29.113 (OWASP ASVS 5.0 V7.3.1, summary/62): a web session unused for
// longer than the inactivity bound is no session; a session in use is
// re-stamped at most once per SessionTouchInterval, so use keeps it alive.
func TestWebSessionIdlesOutAndUseKeepsItAlive(t *testing.T) {
	store, uid := webSessionFixture(t)
	ctx := context.Background()
	store.SetSessionIdle(2 * time.Second)
	t.Cleanup(func() { store.SetSessionIdle(0) })
	raw, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	// Fresh: live, stamped by its first use (the sign-in until then).
	sess, err := store.LookupWebSession(ctx, raw)
	if err != nil || sess.CreatedAt.IsZero() || sess.LastUsedAt.Before(sess.CreatedAt) {
		t.Fatalf("fresh session = %+v, %v", sess, err)
	}
	var used *int64
	if err := store.pool.QueryRow(ctx, `SELECT last_used_at FROM aveloxis_ops.user_session_tokens WHERE token = $1`, hashToken(raw)).Scan(&used); err != nil || used == nil {
		t.Fatalf("the first use must stamp last_used_at: %v %v", used, err)
	}
	// Backdate the stamp past the bound: the session is gone to the gate,
	// though its row is still there (the expiry sweep takes it later).
	if _, err := store.pool.Exec(ctx, `UPDATE aveloxis_ops.user_session_tokens SET last_used_at = $2 WHERE token = $1`, hashToken(raw), time.Now().Add(-3*time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupWebSession(ctx, raw); !errors.Is(err, ErrNoWebSession) {
		t.Errorf("an idle session resolves: %v; want ErrNoWebSession", err)
	}
	// A stamp inside the bound but past the touch interval is re-stamped
	// by the lookup (the touch), so continued use never idles out.
	store.SetSessionIdle(10 * time.Minute)
	old := time.Now().Add(-2 * time.Minute).Unix()
	if _, err := store.pool.Exec(ctx, `UPDATE aveloxis_ops.user_session_tokens SET last_used_at = $2 WHERE token = $1`, hashToken(raw), old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupWebSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := store.pool.QueryRow(ctx, `SELECT last_used_at FROM aveloxis_ops.user_session_tokens WHERE token = $1`, hashToken(raw)).Scan(&after); err != nil || after <= old {
		t.Errorf("a use past the touch interval must re-stamp last_used_at: %d (was %d), %v", after, old, err)
	}
	// A stamp younger than the touch interval is left alone (one write a minute).
	if _, err := store.LookupWebSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	var again int64
	if err := store.pool.QueryRow(ctx, `SELECT last_used_at FROM aveloxis_ops.user_session_tokens WHERE token = $1`, hashToken(raw)).Scan(&again); err != nil || again != after {
		t.Errorf("a use within the touch interval must not write: %d then %d, %v", after, again, err)
	}
	// With no bound set (a store nobody configured) an old stamp is no bar.
	store.SetSessionIdle(0)
	if _, err := store.pool.Exec(ctx, `UPDATE aveloxis_ops.user_session_tokens SET last_used_at = $2 WHERE token = $1`, hashToken(raw), time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupWebSession(ctx, raw); err != nil {
		t.Errorf("without a bound an old stamp must not refuse: %v", err)
	}
}

// An account's sessions are listed (web sessions and the tokens minted
// for the account, with their origin) and ended at once, children
// included (ASVS V7.5.2, V7.4.5).
func TestUserSessionsAreListedAndEndedTogether(t *testing.T) {
	store, uid := webSessionFixture(t)
	ctx := context.Background()
	web1, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	web2, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.MintTokenForWebSession(ctx, web1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	loose, err := store.CreateSessionToken(ctx, uid, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.ListUserSessions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	fromWeb := 0
	seen := map[string]bool{}
	for _, s := range list {
		kinds[s.Kind]++
		if s.FromWebSession {
			fromWeb++
		}
		seen[s.TokenHash] = true
		if s.CreatedAt.IsZero() || s.ExpiresAt.Before(time.Now()) {
			t.Errorf("session %+v has no created time or is expired", s)
		}
	}
	if len(list) != 4 || kinds["web"] != 2 || kinds["api"] != 2 || fromWeb != 1 || !seen[hashToken(web1)] || !seen[hashToken(child)] || !seen[hashToken(loose)] {
		t.Errorf("list = %+v; want two web sessions, the child (from a web session) and the loose token", list)
	}
	n, err := store.DeleteUserSessions(ctx, uid)
	if err != nil || n != 4 {
		t.Fatalf("DeleteUserSessions = %d, %v; want 4 rows", n, err)
	}
	for name, probe := range map[string]func() error{
		"web1":  func() error { _, e := store.LookupWebSession(ctx, web1); return e },
		"web2":  func() error { _, e := store.LookupWebSession(ctx, web2); return e },
		"child": func() error { _, e := store.ValidateSessionToken(ctx, child); return e },
		"loose": func() error { _, e := store.ValidateSessionToken(ctx, loose); return e },
	} {
		if err := probe(); !errors.Is(err, ErrNoWebSession) && !errors.Is(err, ErrInvalidSessionToken) {
			t.Errorf("%s still resolves after the account's sessions were ended: %v", name, err)
		}
	}
	if list, err := store.ListUserSessions(ctx, uid); err != nil || len(list) != 0 {
		t.Errorf("after ending: %d sessions listed, %v", len(list), err)
	}
}

// Review round 1 of 0.29.113 (finding 4, the config-range sweep): at the
// smallest legal bound the touch must still land inside it. With a fixed
// one-minute touch, a one-minute bound refused a session 60 s after its
// first use however busy it was; the touch interval is now at most half
// the bound.
func TestTheTouchLandsInsideTheSmallestIdleBound(t *testing.T) {
	store, uid := webSessionFixture(t)
	ctx := context.Background()
	store.SetSessionIdle(time.Minute)
	t.Cleanup(func() { store.SetSessionIdle(0) })
	raw, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * time.Second).Unix() // inside the bound, past half of it
	if _, err := store.pool.Exec(ctx, `UPDATE aveloxis_ops.user_session_tokens SET last_used_at = $2 WHERE token = $1`, hashToken(raw), old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupWebSession(ctx, raw); err != nil {
		t.Fatalf("a session used 40 s ago under a 60 s bound must resolve: %v", err)
	}
	var after int64
	if err := store.pool.QueryRow(ctx, `SELECT last_used_at FROM aveloxis_ops.user_session_tokens WHERE token = $1`, hashToken(raw)).Scan(&after); err != nil || after <= old {
		t.Errorf("under a 60 s bound a use 40 s after the last must re-stamp (stamp %d, was %d, %v); the session would idle out while in use", after, old, err)
	}
	for _, c := range []struct{ idle, want time.Duration }{
		{0, SessionTouchInterval},
		{time.Minute, 30 * time.Second},
		{2 * time.Minute, SessionTouchInterval},
		{30 * time.Minute, SessionTouchInterval},
	} {
		if got := touchInterval(c.idle); got != c.want {
			t.Errorf("touchInterval(%v) = %v, want %v", c.idle, got, c.want)
		}
	}
}

// Review round 1 of 0.29.113 (finding 7): the account page lists live
// sessions; a web session idle past the bound is no longer one.
func TestIdledOutWebSessionsAreNotListed(t *testing.T) {
	store, uid := webSessionFixture(t)
	ctx := context.Background()
	store.SetSessionIdle(10 * time.Minute)
	t.Cleanup(func() { store.SetSessionIdle(0) })
	idle, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	live, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE aveloxis_ops.user_session_tokens SET last_used_at = $2, created_at = $2 WHERE token = $1`, hashToken(idle), time.Now().Add(-11*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListUserSessions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range list {
		seen[s.TokenHash] = true
	}
	if seen[hashToken(idle)] || !seen[hashToken(live)] {
		t.Errorf("listed %+v; want the live session and not the idled-out one", list)
	}
}

// Review round 1 of 0.29.113 (finding 5): a session token whose parent web
// session is gone (the expiry sweep set parent_token NULL) carries no
// sign-in time the re-authentication rule can trust; it answers the zero
// time, never its own mint (a token minted 23 h 50 min after the sign-in
// read as "signed in just now" once its parent was swept).
func TestAParentlessSessionTokenHasNoRecentSignIn(t *testing.T) {
	store, uid := webSessionFixture(t)
	ctx := context.Background()
	web, err := store.CreateWebSession(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.MintTokenForWebSession(ctx, web, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if at, err := store.SessionTokenCreatedAt(ctx, child); err != nil || at.IsZero() {
		t.Fatalf("a child of a live session = %v, %v; want its parent's sign-in", at, err)
	}
	if _, err := store.pool.Exec(ctx, deleteSessionsSQL("token = $1"), hashToken(web)); err != nil { // the expiry sweep's shape: the parent only
		t.Fatal(err)
	}
	at, err := store.SessionTokenCreatedAt(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	if !at.IsZero() || SignInIsRecent(at, time.Hour) {
		t.Errorf("a parentless token's sign-in = %v (recent=%v); want the zero time, never recent", at, SignInIsRecent(at, time.Hour))
	}
	if SignInIsRecent(time.Now().Add(-2*time.Minute), time.Minute) || !SignInIsRecent(time.Now().Add(-30*time.Second), time.Minute) {
		t.Error("SignInIsRecent must accept a sign-in inside the bound and refuse one past it")
	}
}

// Review round 1 of 0.29.113 (finding 2, the DB-F4 race of 0.29.106 on the
// account-wide delete): a mint in flight from one of the account's
// sessions when "sign out everywhere" runs must not leave its child alive.
func TestEndingAllSessionsWaitsForAMintInFlight(t *testing.T) {
	store, uid := webSessionFixture(t)
	ctx := context.Background()
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
	child := hashToken("in-flight-child-all")
	now := time.Now().Unix()
	if _, err := tx.Exec(ctx, `INSERT INTO aveloxis_ops.user_session_tokens (token, user_id, created_at, expiration, token_hashed, kind, parent_token)
		VALUES ($1, $2, $3, $4, TRUE, 'api', $5)`, child, uid, now, now+3600, hashToken(web)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := store.DeleteUserSessions(ctx, uid); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("ending the account's sessions returned (%v) while a mint was in flight; it must wait for the session row", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ending the account's sessions did not finish after the mint committed")
	}
	var n int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM aveloxis_ops.user_session_tokens WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d row(s) of the account survive ending its sessions; the child committed meanwhile must go too", n)
	}
}
