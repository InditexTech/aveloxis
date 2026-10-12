// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/db"
)

// v0.29.91 (whole-PR review, Med): behind nginx with api.trusted_proxy
// empty, every client is 127.0.0.1, which the default exempt_cidrs
// exempts — the per-IP limits and require_auth then apply to no one, and
// nothing said so. The api now WARNs once (the shared detector the web
// uses), naming api.trusted_proxy; it refuses nothing (SR-7).
func TestAPIWarnsWhenAProxyIsInFrontWithoutTrustedProxy(t *testing.T) {
	serve := func(trusted, peer, xff string) (string, int) {
		logs := &lockedBuffer{}
		s, err := NewWithOptions(nil, slog.New(slog.NewTextHandler(logs, nil)), Options{ExemptCIDRs: DefaultExemptCIDRs, TrustedProxy: trusted})
		if err != nil {
			t.Fatal(err)
		}
		code := 0
		for i := 0; i < 2; i++ {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
			r.RemoteAddr = peer
			if xff != "" {
				r.Header.Set("X-Forwarded-For", xff)
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			code = w.Code
		}
		return logs.String(), code
	}
	out, code := serve("", "127.0.0.1:1", "198.51.100.1")
	if n := strings.Count(out, "api.trusted_proxy is empty"); n != 1 || !strings.Contains(out, "level=WARN") {
		t.Errorf("a loopback peer with X-Forwarded-For and the key empty logged %d WARNs, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "require_auth") {
		t.Errorf("the WARN must say what goes wrong (require_auth, the per-IP limits):\n%s", out)
	}
	if code == http.StatusTooManyRequests || code == http.StatusForbidden || code == http.StatusUnauthorized {
		t.Errorf("observation only: the request was refused (%d)", code)
	}
	for _, c := range []struct{ trusted, peer, xff string }{
		{"127.0.0.1", "127.0.0.1:1", "198.51.100.1"}, // configured
		{"", "203.0.113.9:1", "198.51.100.1"},        // a direct client
		{"", "127.0.0.1:1", ""},                      // local, no proxy header
	} {
		if out, _ := serve(c.trusted, c.peer, c.xff); strings.Contains(out, "trusted_proxy is empty") {
			t.Errorf("%+v: warned without the misconfiguration:\n%s", c, out)
		}
	}
}

// v0.29.91 review round 2 (R2-2): sign-out is exempt from the quotas for a
// SESSION only. An API token on the logout route is answered 400 and ends
// nothing, so it is counted (and refused past its allowance) like any
// other request.
func TestLogoutExemptionIsForSessionsOnly(t *testing.T) {
	tok := db.APITokenPrefix + "logout"
	store := &fakeSessionStore{userID: 7, apiValid: map[string]db.APITokenIdentity{tok: {TokenID: 9, UserID: 7, RateLimitPerHour: 1, RateLimitPerDay: 100}}}
	h, _ := tokenChain(t, store, Options{RateLimitRPS: 1000, RateLimitBurst: 1000, RateLimitDaily: 100000})
	codes := []int{}
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		r.RemoteAddr = "203.0.113.70:1"
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		codes = append(codes, w.Code)
	}
	if codes[1] != http.StatusTooManyRequests {
		t.Errorf("an API token's second logout call past an hourly allowance of 1 = %v; want the second refused (counted)", codes)
	}
}
