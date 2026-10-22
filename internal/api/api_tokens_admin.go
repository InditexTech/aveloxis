// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

// v0.29.82 — the admin endpoints behind aveloxis-gui's API-token page
// (operator decisions 2026-10-08): grant a token to an existing account,
// list the granted tokens (never the token itself), revoke one, and read or
// change the defaults the grant form offers (30 days; since v0.29.89 the
// allowances are the token_requests_per_hour/_per_day quotas, 1,000 and
// 10,000, summary/53). Since 0.29.109 (stage 3b of summary/59) the rules
// are the admin service's: these handlers decode, call it and answer.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/aveloxis/aveloxis/internal/admin"
	"github.com/aveloxis/aveloxis/internal/db"
)

// GET /api/v1/admin/api-tokens
func (s *Server) handleAdminAPITokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	tokens, err := s.store.ListAPITokens(r.Context())
	if err != nil {
		s.serverError(w, r, "handleAdminAPITokens", err)
		return
	}
	settings, err := s.store.GetAPITokenSettings(r.Context())
	if err != nil {
		s.serverError(w, r, "handleAdminAPITokens", err)
		return
	}
	if tokens == nil {
		tokens = []db.APIToken{}
	}
	s.writeJSON(w, r, map[string]any{"tokens": tokens, "settings": settings})
}

// POST /api/v1/admin/api-tokens {user_id, label, lifetime_days?, rate_limit_per_hour?, rate_limit_per_day?}
// Omitted values take the current defaults. The answer carries the token,
// the only time it is ever available.
func (s *Server) handleAdminAPITokenGrant(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		UserID           int    `json:"user_id"`
		Label            string `json:"label"`
		LifetimeDays     *int64 `json:"lifetime_days"`
		RateLimitPerHour *int64 `json:"rate_limit_per_hour"`
		RateLimitPerDay  *int64 `json:"rate_limit_per_day"`
	}
	if err := decodeJSONBody(w, r, adminJSONBodyLimit, &body); err != nil {
		writeAuthError(w, http.StatusBadRequest, "body must be JSON: {user_id, label, lifetime_days?, rate_limit_per_hour?, rate_limit_per_day?}")
		return
	}
	raw, tok, err := s.admin.GrantAPIToken(r.Context(), s.actor(info), admin.TokenGrant{
		UserID: body.UserID, Label: body.Label,
		LifetimeDays: body.LifetimeDays, RateLimitPerHour: body.RateLimitPerHour, RateLimitPerDay: body.RateLimitPerDay,
	})
	if err != nil {
		// The route's own words for an unknown account (a 400 since
		// v0.29.82, kept byte-identical); every other refusal from the
		// one table.
		if errors.Is(err, db.ErrUserNotFound) {
			writeAuthError(w, http.StatusBadRequest, "no account with that user_id")
			return
		}
		if !s.refuse(w, info, err) {
			s.serverError(w, r, "handleAdminAPITokenGrant", err)
		}
		return
	}
	setNoStoreHeaders(w.Header()) // the one answer that carries the token
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token": raw, "token_id": tok.TokenID, "user_id": tok.UserID, "label": tok.Label,
		"created_at": tok.CreatedAt, "expires_at": tok.ExpiresAt, "rate_limit_per_hour": tok.RateLimitPerHour,
		"rate_limit_per_day": tok.RateLimitPerDay,
	})
}

// POST /api/v1/admin/api-tokens/{tokenID}/revoke
func (s *Server) handleAdminAPITokenRevoke(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("tokenID"), 10, 64)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "token id must be a positive integer")
		return
	}
	// The token stops at its next call in every api process (the per-call
	// row recheck); the service's TokensChanged hook drops this process's
	// cached validations for it.
	if err := s.admin.RevokeAPIToken(r.Context(), s.actor(info), id); err != nil {
		if !s.refuse(w, info, err) {
			s.serverError(w, r, "handleAdminAPITokenRevoke", err)
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"token_id": id, "revoked": true})
}

// GET /api/v1/admin/api-token-settings
func (s *Server) handleAdminAPITokenSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	settings, err := s.store.GetAPITokenSettings(r.Context())
	if err != nil {
		s.serverError(w, r, "handleAdminAPITokenSettings", err)
		return
	}
	s.writeJSON(w, r, settings)
}

// POST /api/v1/admin/api-token-settings {default_rate_limit_per_hour, default_rate_limit_per_day?, default_lifetime_days}
// The allowances are the token quotas (the Capacity page edits the same
// rows); one aveloxis.json sets is refused with 409. An omitted daily value
// leaves it unchanged (a GUI older than v0.29.89 sends none).
func (s *Server) handleAdminAPITokenSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		DefaultRateLimitPerHour *int64 `json:"default_rate_limit_per_hour"`
		DefaultRateLimitPerDay  *int64 `json:"default_rate_limit_per_day"`
		DefaultLifetimeDays     *int64 `json:"default_lifetime_days"`
	}
	if err := decodeJSONBody(w, r, adminJSONBodyLimit, &body); err != nil {
		writeAuthError(w, http.StatusBadRequest, "body must be JSON: {default_rate_limit_per_hour, default_rate_limit_per_day?, default_lifetime_days}")
		return
	}
	saved, err := s.admin.SetAPITokenDefaults(r.Context(), s.actor(info), admin.TokenDefaults{
		RateLimitPerHour: body.DefaultRateLimitPerHour, RateLimitPerDay: body.DefaultRateLimitPerDay, LifetimeDays: body.DefaultLifetimeDays,
	})
	if err != nil {
		if !s.refuse(w, info, err) {
			s.serverError(w, r, "handleAdminAPITokenSettingsUpdate", err)
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"default_rate_limit_per_hour": saved.DefaultRateLimitPerHour,
		"default_rate_limit_per_day": saved.DefaultRateLimitPerDay, "default_lifetime_days": saved.DefaultLifetimeDays})
}

// POST /api/v1/auth/logout — ends the session token it is called with
// (OWASP ASVS V7.4.1; the 0.29.82 review: sign-out ended only the web
// process's cookie session, and a copied Bearer token stayed valid for its
// 30 days). Deleting a token needs only holding it; with api.require_auth
// on, an already-invalid token is refused (401) by the auth layer before
// this runs, otherwise it answers 204. An API token is revoked by an
// administrator.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w.Header())
	tok := bearerToken(r)
	if tok == "" {
		writeAuthError(w, http.StatusUnauthorized, "sign-out needs the session token (Bearer)")
		return
	}
	if strings.HasPrefix(tok, db.APITokenPrefix) {
		writeAuthError(w, http.StatusBadRequest, "an API token is revoked by an administrator, not signed out")
		return
	}
	if err := s.store.DeleteSessionToken(r.Context(), tok); err != nil {
		s.serverError(w, r, "handleLogout", err)
		return
	}
	if s.auth != nil {
		s.auth.forget(tok)
	}
	w.WriteHeader(http.StatusNoContent)
}
