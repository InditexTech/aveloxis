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
