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
}

// CreateWebSession records a browser session for the user and returns the
// raw token the cookie carries; only its hash is stored (B2: sessions
// survive a restart of the web process, and a database read never shows a
// usable cookie). Expired rows are swept on the way.
func (s *PostgresStore) CreateWebSession(ctx context.Context, userID int) (string, error) {
	return s.createSessionToken(ctx, userID, WebSessionLifetime, sessionKindWeb, "")
}

// LookupWebSession resolves a cookie's raw token to the session and the
// users row behind it in ONE read; ErrNoWebSession when the token names
// no live web session. Any other error is the database's (SR-5: never
// "no session").
func (s *PostgresStore) LookupWebSession(ctx context.Context, raw string) (WebSession, error) {
	var ws WebSession
	var exp int64
	err := s.pool.QueryRow(ctx, `
		SELECT t.user_id, u.login_name, COALESCE(u.avatar_url, ''), COALESCE(u.oauth_provider, ''), u.admin, t.expiration
		FROM aveloxis_ops.user_session_tokens t
		JOIN aveloxis_ops.users u ON u.user_id = t.user_id
		WHERE t.token = $1 AND t.kind = $2 AND t.expiration > $3`,
		hashToken(raw), sessionKindWeb, time.Now().Unix()).Scan(&ws.UserID, &ws.LoginName, &ws.AvatarURL, &ws.Provider, &ws.IsAdmin, &exp)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebSession{}, ErrNoWebSession
	}
	if err != nil {
		return WebSession{}, fmt.Errorf("look up web session: %w", err)
	}
	ws.ExpiresAt = time.Unix(exp, 0)
	return ws, nil
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

// MintTokenForWebSession mints the Bearer token /auth/token answers for a
// live web session (the default Bearer lifetime), recording the session
// as its parent; ErrNoWebSession when the raw token names none — read and
// written in one statement that holds the session row KEY SHARE, so a
// concurrent sign-out either waits for the mint and sweeps the token, or
// finishes first and the mint answers ErrNoWebSession (see
// DeleteWebSession).
func (s *PostgresStore) MintTokenForWebSession(ctx context.Context, webRaw string) (string, error) {
	return s.createSessionToken(ctx, 0, DefaultSessionTokenLifetime, sessionKindAPI, hashToken(webRaw))
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
