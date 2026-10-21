// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

// v0.27.1 — DB-backed session tokens + per-user repo scope
// (plan: summary/api-analytics-plan-2026-07-10.md §2/§2b).
//
// The aveloxis_ops.user_session_tokens table (present in the schema
// since the Augur-compat era, previously unused) becomes the shared
// session store: the web process minted a token at OAuth login, the
// api process validated the same token from the SPA's Authorization
// Bearer header. Since 0.29.102 (stage 2c of summary/59) the row the
// login mints is the browser session itself (kind "web", web_sessions.go)
// and /auth/token mints the Bearer token (kind "api") from it, recording
// it as the parent. DB-backed also fixed the long-standing "restart web
// and everyone is logged out" limitation of in-memory sessions.
//
// v0.29.82: the table holds only the SHA-256 hex of each token
// (hashToken); the raw token exists only in the caller's hands. Every
// lookup hashes what it is given.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrInvalidSessionToken is returned for unknown or expired tokens.
var ErrInvalidSessionToken = errors.New("invalid or expired session token")

// DefaultSessionTokenLifetime is how long an SPA session token lives.
const DefaultSessionTokenLifetime = 30 * 24 * time.Hour

// CreateSessionToken mints a Bearer token for the user with the given
// lifetime, bound to no web session. No production path mints one this
// way since 0.29.102 (the web process mints through MintTokenForWebSession
// and an operator-issued API token is CreateAPIToken); it stays as the
// one minter the tests of OTHER packages (internal/api) use for a loose
// Bearer, which an in-package test helper cannot give them (0.29.106,
// whole-branch review DB-F3). Expired tokens are purged on the way.
func (s *PostgresStore) CreateSessionToken(ctx context.Context, userID int, lifetime time.Duration) (string, error) {
	return s.createSessionToken(ctx, userID, lifetime, sessionKindAPI, "")
}

// newSecretToken is the ONE minting of a secret the store hands out (a
// Bearer token, a web session, an API token's body, a confirmation link):
// 32 bytes of crypto/rand, hex (0.29.102, DBS-14 — four sites each read
// the generator themselves).
func newSecretToken() (string, error) {
	raw, err := newSecretBytes()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// newSecretBytes is the one crypto/rand read of the store: 32 bytes, for a
// token (newSecretToken) or a key (the sign-up quota's daily secret).
func newSecretBytes() ([]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("secret entropy: %w", err)
	}
	return raw, nil
}

// createSessionToken is the one writer of aveloxis_ops.user_session_tokens:
// a row of the given kind, hashed, with the parent session's hash when
// /auth/token minted it from a web session ("" otherwise, NULL). The
// expired rows are swept after every mint.
func (s *PostgresStore) createSessionToken(ctx context.Context, userID int, lifetime time.Duration, kind, parentHash string) (string, error) {
	return s.createSessionTokenUntil(ctx, userID, time.Now().Add(lifetime), kind, parentHash)
}

// createSessionTokenUntil is createSessionToken with the expiry absolute:
// the row expires at expiresAt as given, never re-anchored at this clock
// (round-1 review of 0.29.112, F1: a lifetime re-anchored at the store's
// clock could land a second past the parent). The web proxy passes its
// session's own expiry — the value of that row's column — so its child
// expires at the parent's instant by construction; /auth/token passes
// now + the default lifetime, and that token keeps its own life past the
// session on purpose (the documented SPA behaviour), so no cap at the
// parent is applied here.
func (s *PostgresStore) createSessionTokenUntil(ctx context.Context, userID int, expiresAt time.Time, kind, parentHash string) (string, error) {
	token, err := newSecretToken()
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	expiration := expiresAt.Unix()
	if parentHash != "" {
		// Minted FROM a web session: the user is the session's, and the
		// row is written only while that session is live, under KEY SHARE
		// on it so a concurrent sign-out serialises (DeleteWebSession).
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO aveloxis_ops.user_session_tokens (token, user_id, created_at, expiration, token_hashed, kind, parent_token)
			SELECT $1, p.user_id, $2, $3, TRUE, $4, p.token
			FROM aveloxis_ops.user_session_tokens p
			WHERE p.token = $5 AND p.kind = $6 AND p.expiration > $2
			FOR KEY SHARE`, hashToken(token), now, expiration, kind, parentHash, sessionKindWeb)
		if err != nil {
			return "", fmt.Errorf("create %s session token: %w", kind, err)
		}
		if tag.RowsAffected() == 0 {
			return "", ErrNoWebSession
		}
	} else {
		_, err = s.pool.Exec(ctx, `
		INSERT INTO aveloxis_ops.user_session_tokens (token, user_id, created_at, expiration, token_hashed, kind, parent_token)
		VALUES ($1, $2, $3, $4, TRUE, $5, NULL)`, hashToken(token), userID, now, expiration, kind)
		if err != nil {
			return "", fmt.Errorf("create %s session token: %w", kind, err)
		}
	}
	// Opportunistic hygiene — keeps the table from accumulating
	// expired rows without a dedicated ticker.
	if _, err := s.pool.Exec(ctx, deleteSessionsSQL("expiration < $1"), now); err != nil && !errors.Is(err, context.Canceled) {
		// The token was created; only the hygiene failed (old problem O2).
		s.logger.Warn("expired session tokens could not be deleted — they are removed with the next token", "error", err)
	}
	return token, nil
}

// ValidateSessionToken resolves a token to its user id, or
// ErrInvalidSessionToken for unknown/expired tokens.
func (s *PostgresStore) ValidateSessionToken(ctx context.Context, token string) (int, error) {
	var userID int
	err := s.pool.QueryRow(ctx, `
		SELECT user_id FROM aveloxis_ops.user_session_tokens
		WHERE token = $1 AND kind = $3 AND expiration > $2`, hashToken(token), time.Now().Unix(), sessionKindAPI).Scan(&userID)
	if err != nil {
		// Only "no such row" is the sentinel (SR-5; worklist follow-up 6,
		// review round 1): a lost connection or a cancelled context is the
		// store's failure, and the API answers it 503, not "sign in again".
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrInvalidSessionToken
		}
		return 0, fmt.Errorf("validate session token: %w", err)
	}
	return userID, nil
}

// DeleteSessionToken revokes one token (logout).
func (s *PostgresStore) DeleteSessionToken(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, deleteSessionsSQL("token = $1"), hashToken(token))
	return err
}

// deleteSessionsSQL deletes the sessions matching where together with the
// aveloxis_ops.refresh_tokens rows that reference them, in one statement.
// refresh_tokens is an Augur leftover Aveloxis never writes, but a fleet
// carried over from Augur may hold rows; its foreign key is checked at
// commit, so deleting a session alone failed (logout answered 500 and the
// token stayed valid; Copilot review 5476192626 on PR #228). where is a
// constant predicate over user_session_tokens, never caller input.
func deleteSessionsSQL(where string) string {
	// It answers the number of sessions deleted (one row).
	return `WITH gone AS (
		DELETE FROM aveloxis_ops.user_session_tokens WHERE ` + where + ` RETURNING token
	), refreshed AS (
		DELETE FROM aveloxis_ops.refresh_tokens WHERE user_session_token IN (SELECT token FROM gone) RETURNING 1
	)
	SELECT count(*) FROM gone`
}

// GetUserRepoScope returns every repo in ANY of the user's groups —
// pending groups included. Operator clarification (2026-07-14):
// approval exists to gate NEW COLLECTION (so nobody bulk-adds 50,000
// uncollected repos), never to gate visibility of already-collected
// data. A pending group's uncollected repos being "in scope" grants
// nothing (there is no data until an admin approves the collection),
// so the status filter was pure friction. Admins are unscoped —
// callers check IsUserAdmin and skip this entirely (§2b).
func (s *PostgresStore) GetUserRepoScope(ctx context.Context, userID int) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ur.repo_id
		FROM aveloxis_ops.user_repos ur
		JOIN aveloxis_ops.user_groups g USING (group_id)
		WHERE g.user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
