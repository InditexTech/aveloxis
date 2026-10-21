// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/httpserver"
	"github.com/aveloxis/aveloxis/internal/platform"
)

// logURL is the one spelling of a caller's URL in a log attribute: userinfo
// redacted FIRST, then truncated (batch 5b review round 1: the other order
// let a userinfo longer than the truncation window survive into the WARN
// unredacted — 187 bytes of a credential). Every URL-keyed attribute in
// this package goes through it.
func logURL(u string) string {
	return platform.TruncateForLog([]byte(platform.RedactURLUserinfo(u)), 200)
}

// serverError answers a store or generation failure on a page: the cause
// goes to the log at ERROR with the handler's name and the body is generic
// (worklist follow-up 12: the group, repository and SBOM pages turned every
// lookup error into a 404 or 403, and the SBOM download wrote the error's
// text into its body). A client that left mid-request (context.Canceled) is
// Debug, as in the API's serverError.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, handler string, err error) {
	if httpserver.RequestEnded(r.Context(), err) { // the client left, or http_timeout_seconds fired (its WARN reports it)
		s.logger.Debug("request ended before its handler finished", "handler", handler, "error", err)
		return
	}
	s.logger.Error("request failed", "handler", handler, "error", err)
	http.Error(w, "internal error; try again", http.StatusInternalServerError)
}

// capacityNotice is the group page's text after a paste the repository
// allocation refused (add_error=capacity): the one wording every surface
// shows, with the account's current numbers. Empty otherwise, or when the
// numbers cannot be read (logged; the generic notice is shown instead).
func (s *Server) capacityNotice(r *http.Request, userID int) string {
	switch r.URL.Query().Get("add_error") {
	case "capacity":
	case "capacity_links":
		st, err := s.store.AccountDailyAdditions(r.Context(), userID)
		if err != nil {
			httpserver.LogFailure(r.Context(), s.logger, slog.LevelWarn, err, "could not read the daily additions for the group page", "user_id", userID, "error", err)
			return ""
		}
		return st.Refusal(time.Now()).Message()
	default:
		return ""
	}
	st, err := s.store.AccountRepoAllocation(r.Context(), userID)
	if err != nil {
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelWarn, err, "could not read the repository allocation for the group page", "user_id", userID, "error", err)
		return ""
	}
	return st.Refusal().Message()
}

// SignupAddress is the browser's address on an OAuth callback, for
// capacity.signups_per_address_per_day (v0.29.89): web.trusted_proxy
// decides whether X-Forwarded-For is believed (httpserver.ClientIP, the
// rule the api uses). Zero when it cannot be read: not counted.
func (s *Server) SignupAddress(r *http.Request) netip.Addr {
	s.warnMissingTrustedProxy(r)
	if ip := httpserver.ClientIP(r, s.cfg.TrustedProxy); ip != nil {
		if addr, ok := netip.AddrFromSlice(ip); ok {
			return addr.Unmap()
		}
	}
	return netip.Addr{}
}

// warnMissingTrustedProxy logs once per process when a sign-in or an /api
// request (v0.29.91) comes from a loopback peer that sent X-Forwarded-For while web.trusted_proxy is empty:
// nginx in front, unconfigured, so every sign-up looks like nginx's own
// address and an enforced sign-up quota would refuse every new user after
// the third of the day (ASVS review I1/I3). The web has no exempt networks,
// so only a loopback peer is the proxy here.
func (s *Server) warnMissingTrustedProxy(r *http.Request) {
	s.proxyCheck.Observe(s.logger, r, "web.trusted_proxy", s.cfg.TrustedProxy, nil,
		"every new account counts as the proxy's own address for the sign-up quota, and the api, through this process's /api proxy, sees every client as the proxy")
}

// writeSignupRefusal answers a sign-up the quota refused: 429, Retry-After
// until 00:00 UTC, never stored, and a page with the one wording every
// surface shows (capacity.Exceeded.Message), HTML-escaped.
func writeSignupRefusal(w http.ResponseWriter, ex *capacity.Exceeded) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "text/html; charset=utf-8")
	// A static, escaped page: nothing to load, never framed (ASVS V3.4).
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Retry-After", strconv.Itoa(int(ex.RetryAfter(time.Now()).Seconds())))
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Sign-up limit reached</title></head>
<body style="font-family:system-ui,sans-serif;max-width:40rem;margin:3rem auto;padding:0 16px;line-height:1.5">
<h1 style="font-size:1.4rem">Sign-up limit reached</h1>
<p>%s</p>
<p><a href="/">Back to Aveloxis</a></p>
</body></html>`, html.EscapeString(ex.Message()))
}
