// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// WebSessionLifetime is how long a browser session lives from sign-in
// (not refreshed by use): the cookie's MaxAge and the row's expiration are
// this one value.
const WebSessionLifetime = 24 * time.Hour

// ErrNoWebSession is the answer when a token names no live web session
// (unknown, expired, signed out, or a Bearer token).
var ErrNoWebSession = errors.New("no such web session")

// Session kinds in aveloxis_ops.user_session_tokens (0.29.102, stage 2c of
// summary/59, N12): the browser session the web process sets as a cookie,
// and the Bearer token the API process validates (/auth/token mints one
// from a web session; it records that session as its parent).
const (
	sessionKindWeb = "web"
	sessionKindAPI = "api"
)

// WebSession is a signed-in browser session as the web process's gate
// reads it on every request: the users row's current name, avatar,
// provider and privilege (D-1: a demotion shows at the next request), and
// when the session ends.
type WebSession struct {
	UserID    int
	LoginName string
	AvatarURL string
	Provider  string
	IsAdmin   bool
	ExpiresAt time.Time
	// CreatedAt is the sign-in; LastUsedAt the latest request before this
	// one (the sign-in until the first touch). A sensitive change asks for
	// a recent sign-in (ASVS V7.5.1) and the inactivity rule reads the
	// latter (0.29.113).
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// UserSession is one live session of an account as the account page and
// the Users page list it (ASVS V7.5.2, V7.4.5; 0.29.113): a web session
// (a browser), or a token minted from one (/auth/token — the SPA's — or
// the web proxy's child, FromWebSession) or minted loose.
type UserSession struct {
	TokenHash      string // the stored hash; a page marks "this one" by comparing HashToken(cookie)
	Kind           string // "web" or "api"
	FromWebSession bool   // kind api minted from a web session (parent_token set)
	CreatedAt      time.Time
	LastUsedAt     time.Time // web sessions; a token's created_at otherwise
	ExpiresAt      time.Time
}

// CreateWebSession records a browser session for the user and returns the
// raw token the cookie carries; only its hash is stored (B2: sessions
// survive a restart of the web process, and a database read never shows a
// usable cookie). Expired rows are swept on the way.
func (s *PostgresStore) CreateWebSession(ctx context.Context, userID int) (string, error) {
	return s.createSessionToken(ctx, userID, WebSessionLifetime, sessionKindWeb, "")
}

// LookupWebSession resolves a cookie's raw token to the session and the
// users row behind it in ONE statement; ErrNoWebSession when the token
// names no live web session — expired, or (SetSessionIdle, ASVS V7.3.1)
// unused for longer than the inactivity bound, read from last_used_at
// (the sign-in until the first touch). The same statement re-stamps
// last_used_at when the stamp is older than touchInterval (a minute, or half
// the bound when that is shorter), so a session in use never idles out and a busy one costs one write per
// touch interval. Any other error is the database's (SR-5: never "no session").
func (s *PostgresStore) LookupWebSession(ctx context.Context, raw string) (WebSession, error) {
	var ws WebSession
	var exp, created, used int64
	now := time.Now().Unix()
	err := s.pool.QueryRow(ctx, `
		WITH live AS (
			SELECT t.token, t.user_id, t.created_at, t.expiration, t.last_used_at
			FROM aveloxis_ops.user_session_tokens t
			WHERE t.token = $1 AND t.kind = $2 AND t.expiration > $3
			  AND ($4 = 0 OR COALESCE(t.last_used_at, t.created_at) > $3 - $4)
		), touched AS (
			UPDATE aveloxis_ops.user_session_tokens x SET last_used_at = $3
			FROM live WHERE x.token = live.token AND (x.last_used_at IS NULL OR x.last_used_at <= $3 - $5)
		)
		SELECT live.user_id, u.login_name, COALESCE(u.avatar_url, ''), COALESCE(u.oauth_provider, ''), u.admin,
		       live.expiration, COALESCE(live.created_at, 0), COALESCE(live.last_used_at, live.created_at, 0)
		FROM live JOIN aveloxis_ops.users u ON u.user_id = live.user_id`,
		hashToken(raw), sessionKindWeb, now, int64(s.sessionIdle.Seconds()), int64(touchInterval(s.sessionIdle).Seconds())).
		Scan(&ws.UserID, &ws.LoginName, &ws.AvatarURL, &ws.Provider, &ws.IsAdmin, &exp, &created, &used)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebSession{}, ErrNoWebSession
	}
	if err != nil {
		return WebSession{}, fmt.Errorf("look up web session: %w", err)
	}
	ws.ExpiresAt = time.Unix(exp, 0)
	ws.CreatedAt = time.Unix(created, 0)
	ws.LastUsedAt = time.Unix(used, 0)
	return ws, nil
}

// ListUserSessions lists an account's live sessions and the tokens minted
// for it (every row of user_session_tokens for the user that has not
// expired, a web session also not idle past the bound — the lookup's own
// predicate), newest first; the page marks the caller's own by hash.
func (s *PostgresStore) ListUserSessions(ctx context.Context, userID int) ([]UserSession, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT token, kind, parent_token IS NOT NULL, COALESCE(created_at, 0), COALESCE(last_used_at, created_at, 0), expiration
		FROM aveloxis_ops.user_session_tokens
		WHERE user_id = $1 AND expiration > $2
		  AND (kind <> $4 OR $3 = 0 OR COALESCE(last_used_at, created_at) > $2 - $3)
		ORDER BY created_at DESC, token`, userID, time.Now().Unix(), int64(s.sessionIdle.Seconds()), sessionKindWeb)
	if err != nil {
		return nil, fmt.Errorf("list user sessions: %w", err)
	}
	defer rows.Close()
	var out []UserSession
	for rows.Next() {
		var u UserSession
		var created, used, exp int64
		if err := rows.Scan(&u.TokenHash, &u.Kind, &u.FromWebSession, &created, &used, &exp); err != nil {
			return nil, fmt.Errorf("list user sessions: %w", err)
		}
		u.CreatedAt, u.LastUsedAt, u.ExpiresAt = time.Unix(created, 0), time.Unix(used, 0), time.Unix(exp, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUserSessions ends every session and session-minted token of an
// account at once (ASVS V7.4.5 an administrator's, V7.5.2 the account's
// own; 0.29.113): the tree delete over user_id, with the refresh rows.
// Operator-issued API tokens (api_tokens) are not sessions and stay.
// Answers the number of rows deleted. DeleteWebSession's shape, for the
// same reason (review round 1 of 0.29.113, the DB-F4 race): one
// transaction that first locks the account's web sessions FOR UPDATE,
// so a mint in flight from one of them (KEY SHARE until it commits) is
// waited for and its child is in the tree delete's snapshot.
func (s *PostgresStore) DeleteUserSessions(ctx context.Context, userID int) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("delete user sessions: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a committed transaction's rollback is a no-op
	// The live rows only (a mint can only come from one), in one order, so
	// two lockers of overlapping rows queue rather than deadlock (L10 round 2).
	if _, err := tx.Exec(ctx, `SELECT token FROM aveloxis_ops.user_session_tokens WHERE user_id = $1 AND kind = $2 AND expiration > $3 ORDER BY token FOR UPDATE`, userID, sessionKindWeb, time.Now().Unix()); err != nil {
		return 0, fmt.Errorf("delete user sessions: %w", err)
	}
	var n int64
	if err := tx.QueryRow(ctx, deleteSessionTreeSQL("user_id = $1"), userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("delete user sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("delete user sessions: %w", err)
	}
	return n, nil
}

// DeleteWebSession signs the browser session out: the row and every
// Bearer token /auth/token minted from it go together (B4: sign-out means
// signed out everywhere), with their refresh rows. A session already gone
// is not an error.
//
// It is one transaction that first locks the session row (FOR UPDATE):
// a mint in flight from the same session holds the row KEY SHARE until
// it commits (createSessionToken), so the sign-out waits for it and the
// tree delete, a second statement with its own snapshot, sees the child;
// a mint that arrives later waits on the lock and then finds no live
// session (0.29.106, whole-branch review DB-F4: as one statement the tree
// delete missed a child committed after its snapshot, and the FK action
// set it parentless — a token that outlived the sign-out by 30 days).
func (s *PostgresStore) DeleteWebSession(ctx context.Context, raw string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete web session: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a committed transaction's rollback is a no-op
	var token string
	err = tx.QueryRow(ctx, `SELECT token FROM aveloxis_ops.user_session_tokens WHERE token = $1 AND kind = $2 FOR UPDATE`,
		hashToken(raw), sessionKindWeb).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already signed out
	}
	if err != nil {
		return fmt.Errorf("delete web session: %w", err)
	}
	if _, err := tx.Exec(ctx, deleteSessionTreeSQL("token = $1 AND kind = $2"), token, sessionKindWeb); err != nil {
		return fmt.Errorf("delete web session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("delete web session: %w", err)
	}
	return nil
}

// MintTokenForWebSession mints a Bearer token for a live web session,
// recording the session as its parent: /auth/token's, for the SPA, with
// the default Bearer lifetime; the web proxy's (0.29.110), with the
// session's own expiry, so the child expires with its parent and no
// orphaned row outlives the session (ASVS V7.4.1, the 0.29.112 review G1);
// expiresAt is absolute (no lifetime re-anchored at a second clock), and
// /auth/token's token keeps its own life past the session on purpose; ErrNoWebSession when the raw token names none — read and
// written in one statement that holds the session row KEY SHARE, so a
// concurrent sign-out either waits for the mint and sweeps the token, or
// finishes first and the mint answers ErrNoWebSession (see
// DeleteWebSession).
func (s *PostgresStore) MintTokenForWebSession(ctx context.Context, webRaw string, expiresAt time.Time) (string, error) {
	return s.createSessionTokenUntil(ctx, 0, expiresAt, sessionKindAPI, hashToken(webRaw))
}

// deleteSessionTreeSQL deletes the sessions `where` selects AND every
// token minted from them (parent_token), with the refresh rows of all of
// them: the logout shape. deleteSessionsSQL (the expiry sweep) deletes
// the selected rows only — a token minted from a session that merely
// expired keeps working, parentless (the FK sets parent_token NULL).
func deleteSessionTreeSQL(where string) string {
	return `WITH parents AS (
		SELECT token FROM aveloxis_ops.user_session_tokens WHERE ` + where + `
	), gone AS (
		DELETE FROM aveloxis_ops.user_session_tokens
		WHERE token IN (SELECT token FROM parents) OR parent_token IN (SELECT token FROM parents)
		RETURNING token
	), refreshed AS (
		DELETE FROM aveloxis_ops.refresh_tokens WHERE user_session_token IN (SELECT token FROM gone) RETURNING 1
	)
	SELECT count(*) FROM gone`
}
