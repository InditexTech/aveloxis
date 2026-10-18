// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/httpserver"
)

// cookie is the ONE cookie constructor (0.29.102, stage 2a of summary/59,
// WEB-13): every Set-Cookie the web process emits — the session, the OAuth
// state, the stashed ?next=, and every deletion (maxAge -1) — carries the
// house attributes: HttpOnly always, Secure unless web.dev_mode (HTTP
// development), Path=/, SameSite=Lax (the OAuth callback is a top-level
// navigation, which Lax admits). The rule used to be restated at five
// sites, and the deletions had no SameSite.
func (s *Server) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		MaxAge:   maxAge,
		Path:     "/",
		HttpOnly: true,
		Secure:   !s.cfg.DevMode,
		SameSite: http.SameSiteLaxMode,
	}
}

// sessionCookie is the web session cookie; it lives as long as the row
// (db.WebSessionLifetime — one value, SR-10).
func (s *Server) sessionCookie(token string) *http.Cookie {
	return s.cookie("aveloxis_session", token, int(db.WebSessionLifetime.Seconds()))
}

// oauthStateCookie is the short-lived OAuth CSRF state cookie.
func (s *Server) oauthStateCookie(state string) *http.Cookie {
	return s.cookie("oauth_state", state, 300)
}

// expireCookie clears (expires) a named cookie with the same attributes
// it was set with (the house rule applies to deletions too, v0.27.10).
func (s *Server) expireCookie(name string) *http.Cookie {
	return s.cookie(name, "", -1)
}

// pathID reads a numeric path value of a method-and-path route (0.29.102,
// WEB-8). ok is false for anything that is not a positive integer; the
// caller answers 404 (an id that is not an id names nothing).
func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

// createSession records a signed-in user as a row of
// aveloxis_ops.user_session_tokens (0.29.102, stage 2c of summary/59,
// B2/N12: sessions survive a restart of the web process and a sign-out is
// one delete) and returns the raw token the cookie carries. No privilege
// is stored with it (D-1): the gate reads the users row on every request.
func (s *Server) createSession(ctx context.Context, userID int) (string, error) {
	return s.store.CreateWebSession(ctx, userID)
}

// getSession resolves the request's session cookie through the one lookup
// (Server.lookupSession): nil, nil without a cookie or for a token that
// names no live session; a database failure is returned, never read as
// "no session" (SR-5).
func (s *Server) getSession(r *http.Request) (*Session, error) {
	cookie, err := r.Cookie("aveloxis_session")
	if err != nil {
		return nil, nil
	}
	sess, err := s.lookupSession(r.Context(), cookie.Value)
	if errors.Is(err, db.ErrNoWebSession) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.cookie = cookie.Value
	return sess, nil
}

// storeSession is Server.lookupSession's production value: the store's
// one read of the session row joined to the users row (name, avatar,
// provider, privilege — D-1).
func (s *Server) storeSession(ctx context.Context, raw string) (*Session, error) {
	ws, err := s.store.LookupWebSession(ctx, raw)
	if err != nil {
		return nil, err
	}
	return &Session{UserID: ws.UserID, LoginName: ws.LoginName, AvatarURL: ws.AvatarURL, Provider: ws.Provider, IsAdmin: ws.IsAdmin, ExpiresAt: ws.ExpiresAt}, nil
}

// authedHandler is a handler behind requireAuth or requireAdmin: it takes
// the session its gate resolved (0.29.102, stage 2b of summary/59, WEB-7).
// The gate looks the session up ONCE; a handler that looked it up again
// could find it gone (logged out from another tab between the two reads)
// and dereference nil. The compiler keeps every gated handler on this
// shape.
type authedHandler func(http.ResponseWriter, *http.Request, *Session)

// resolveSession is the one session lookup of a request: the session row
// joined to the users row, privilege decided NOW (D-1: a demoted
// administrator loses /admin and /monitor at the next request, not at the
// next login). No session → 302 /login, false. A lookup error is a 500
// (logged), never read as "no session" or "not an administrator" (SR-5);
// a request the client abandoned is neither.
func (s *Server) resolveSession(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, err := s.getSession(r)
	if err != nil {
		if !httpserver.RequestEnded(r.Context(), err) {
			s.serverError(w, r, "resolveSession", fmt.Errorf("session lookup: %w", err))
		}
		return nil, false
	}
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return nil, false
	}
	return sess, true
}

// requireAuth gates a route on a signed-in session and hands it to the
// handler.
func (s *Server) requireAuth(handler authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.resolveSession(w, r)
		if !ok {
			return
		}
		handler(w, r, sess)
	}
}

// requireAdmin gates a route on administrator privilege decided for this
// request. 403 for a signed-in non-administrator, so they don't even see
// what's behind the route; no session redirects to /login as requireAuth
// does.
func (s *Server) requireAdmin(handler authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.resolveSession(w, r)
		if !ok {
			return
		}
		if !sess.IsAdmin {
			// Logged like the service's refusal (requireAdmin there): a
			// refused administrative route is a signal, not a server error.
			s.logger.Warn("admin route refused: not an administrator", "user_id", sess.UserID, "path", r.URL.Path)
			http.Error(w, "Administrator access required.", http.StatusForbidden)
			return
		}
		handler(w, r, sess)
	}
}

// generateToken is the OAuth state nonce (crypto/rand's text form; a
// session token is minted by the store).
func generateToken() string {
	return rand.Text()
}

// handleAuthToken (v0.27.1) exchanges the caller's web session for a
// DB-backed session token the api process validates. JSON response so
// the SPA can store it (localStorage) and attach it as a Bearer.
func (s *Server) handleAuthToken(w http.ResponseWriter, r *http.Request, sess *Session) {
	// Minted FROM the web session (B4): signing the browser out deletes it.
	token, err := s.store.MintTokenForWebSession(r.Context(), sess.cookie)
	if errors.Is(err, db.ErrNoWebSession) {
		// Signed out in another tab between the gate's read and the mint:
		// a definitive answer, not a failure (review A4).
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelError, err, "failed to mint session token", "user_id", sess.UserID, "error", err)
		http.Error(w, "token creation failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      token,
		"expires_at": time.Now().Add(db.DefaultSessionTokenLifetime).UTC().Format(time.RFC3339),
		"user_id":    sess.UserID,
		"login":      sess.LoginName,
	})
}
