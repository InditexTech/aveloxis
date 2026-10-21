// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/httpserver"
)

// apiSessionLogoutPath is the api's own sign-out: it deletes the Bearer
// presented. Through the proxy that Bearer is the session's held token,
// which the browser never had — the web session would live on with a dead
// token behind it (round-1 review of 0.29.110, M-1), so the proxy refuses
// the route; signing out of the built-in GUI is the web's own POST /logout,
// which revokes the held token with the session.
const apiSessionLogoutPath = "/api/v1/auth/logout"

// proxyKey is the context key under which a proxied request carries its
// session's cache key, for the response hook that forgets a refused token.
type proxyKey struct{}

// handleAPIProxy forwards a signed-in browser's /api/ request to the api
// process with the session's own Bearer attached (0.29.110, stage 4 of
// summary/59): the token is minted once per session through the store
// (kind "api", parent = the web session — sign-out revokes it) and held in
// this process. A session signed out between the gate's read and the mint
// is a 401 (the store's ErrNoWebSession); a store failure is a logged 500,
// and the request never reaches the api without a Bearer.
func (s *Server) handleAPIProxy(w http.ResponseWriter, r *http.Request, sess *Session) {
	if r.URL.Path == apiSessionLogoutPath {
		http.Error(w, "sign out of the web GUI through POST /logout", http.StatusForbidden)
		return
	}
	now := time.Now()
	expires := now.Add(db.DefaultSessionTokenLifetime)
	if !sess.ExpiresAt.IsZero() && sess.ExpiresAt.Before(expires) {
		expires = sess.ExpiresAt // the entry ends with the session
	}
	key := db.HashToken(sess.cookie)
	bearer, err := s.proxyTokens.bearer(r.Context(), key, now, expires, func(ctx context.Context) (string, error) {
		return s.mintProxyToken(ctx, sess.cookie)
	})
	if errors.Is(err, db.ErrNoWebSession) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelError, err, "api proxy: the session's token could not be minted", "user_id", sess.UserID, "error", err)
		http.Error(w, "API session could not be established", http.StatusInternalServerError)
		return
	}
	out := r.Clone(context.WithValue(r.Context(), proxyKey{}, key))
	out.Header.Set("Authorization", "Bearer "+bearer)
	s.apiProxy.ServeHTTP(w, out)
}

// forgetRefusedProxyToken is the proxy's response hook: a 401 for a
// proxied request means the api refused the held Bearer (revoked by
// another path, or swept), so the entry is dropped and the next request
// mints a new one instead of presenting a dead token for the rest of the
// session (round-1 review of 0.29.110, M-1).
func (s *Server) forgetRefusedProxyToken(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized && resp.Request != nil {
		if key, ok := resp.Request.Context().Value(proxyKey{}).(string); ok {
			s.proxyTokens.forget(key)
		}
	}
	return nil
}
