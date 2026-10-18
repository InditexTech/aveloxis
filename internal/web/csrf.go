// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"crypto/rand"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"github.com/aveloxis/aveloxis/internal/db"
)

// csrfCookieName carries the session's form token (0.29.102, stage 2f of
// summary/59, B1/D-10): a double-submit pair — the cookie (HttpOnly, Secure
// unless dev_mode, SameSite Lax, the house attributes) and the same value
// in every server-rendered form's csrf_token field (or the X-CSRF-Token
// header of a fetch). A cross-site page can make the browser send the
// cookie but cannot read it to fill the field.
const csrfCookieName = "aveloxis_csrf"

// csrfExempt lists the POST routes that take no form token: the two
// aveloxis-gui calls from its own origin with credentials (the origin
// check below is their guard — the SPA cannot read the HttpOnly cookie),
// and the API reverse proxy (JSON fetches from the page's own scripts; the
// API's routes decide the rest). Everything else that changes state is a
// server-rendered form.
var csrfExempt = map[string]bool{
	"/auth/token":  true,
	"/logout":      true,
	"/auth/logout": true,
}

// securityHeaders sets the one header set on every answer (D-11): a
// Content-Security-Policy with one script source — the pages carry no
// inline script and load no CDN since stage 2e — inline styles allowed
// (the pages' style attributes and the DOM builder's cssText; not a
// script vector), images from https (the forges' avatars), never framed,
// no base, no plugins; nosniff; the referrer trimmed off-site; the legacy
// frame refusal for old browsers. A handler may tighten it (the sign-up
// refusal page sets 'none').
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' https: data:; connect-src 'self'; font-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// csrfGuard refuses cross-site state changes before any handler (B1).
// For every method but GET, HEAD and OPTIONS:
//
//   - the request's origin — the Origin header, else the Referer's — must
//     be this host or the configured SPA origin (web.spa_url); a browser
//     sends Origin on every cross-site POST, so a request with neither
//     header is not a cross-site browser request and passes to the next
//     check; a Sec-Fetch-Site of cross-site is refused whatever else says;
//   - a request that carries a session cookie and is not an exempt route
//     must carry the session's form token (csrfCookieName's value) in the
//     csrf_token field or the X-CSRF-Token header. An anonymous request
//     forges nothing: the route's gate answers it.
//
// Refusals are 403 and logged at WARN with the path and the origin.
func (s *Server) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if origin, ok := s.requestOrigin(r); !ok {
			s.logger.Warn("cross-site request refused", "method", r.Method, "path", r.URL.Path, "origin", logURL(origin))
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		if _, err := r.Cookie("aveloxis_session"); err == nil && !csrfExempt[r.URL.Path] && !strings.HasPrefix(r.URL.Path, "/api/") {
			cookie, err := r.Cookie(csrfCookieName)
			given := r.Header.Get("X-CSRF-Token")
			if given == "" {
				given = r.PostFormValue("csrf_token")
			}
			if err != nil || cookie.Value == "" || given == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(given)) != 1 {
				s.logger.Warn("form token missing or stale; the form was refused", "method", r.Method, "path", r.URL.Path)
				http.Error(w, "the form has expired — reload the page and try again", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requestOrigin reports whether the request's origin is this deployment's
// (the Host, or web.spa_url's origin). The origin string comes back for the
// log.
func (s *Server) requestOrigin(r *http.Request) (string, bool) {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") && !s.isSPAOrigin(r.Header.Get("Origin")) {
		return r.Header.Get("Origin"), false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return "", true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return origin, false
	}
	return origin, strings.EqualFold(u.Host, r.Host) || s.isSPAOrigin(origin)
}

// isSPAOrigin: the origin is web.spa_url's (scheme and host).
func (s *Server) isSPAOrigin(origin string) bool {
	if s.cfg.SPAURL == "" || origin == "" {
		return false
	}
	spa, err := url.Parse(s.cfg.SPAURL)
	if err != nil {
		return false
	}
	o, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(o.Scheme, spa.Scheme) && strings.EqualFold(o.Host, spa.Host)
}

// csrfToken returns the request's form token, minting the cookie when the
// browser has none (a session from before this release, or the login
// page's own sign-out form): the pages render it into their forms.
func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	token := rand.Text()
	http.SetCookie(w, s.cookie(csrfCookieName, token, int(db.WebSessionLifetime.Seconds())))
	return token
}
