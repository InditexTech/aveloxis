// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestEveryAPIAnswerCarriesTheSecurityHeaders (0.29.113, ASVS V3.4.1 and
// V3.4.4): the api process's answers say nosniff and HSTS, on a public route,
// a refusal and an unknown path alike — the header middleware is the
// outermost layer of the request chain, so no earlier refusal skips it.
func TestEveryAPIAnswerCarriesTheSecurityHeaders(t *testing.T) {
	s := newTestServer()
	h := s.Handler()
	for _, path := range []string{"/api/v1/health", "/api/v1/me", "/no/such/route"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "203.0.113.9:1"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q (status %d)", path, got, w.Code)
		}
		if got := w.Header().Get("Strict-Transport-Security"); !strings.HasPrefix(got, "max-age=31536000") {
			t.Errorf("%s: Strict-Transport-Security = %q (status %d)", path, got, w.Code)
		}
	}
	// The wiring: secureHeaders wraps the whole chain (a later layer would
	// let the CORS preflight and the limiter's refusals out bare).
	body := srctest.StripGoComments(srctest.PackageFunc(t, "internal/api", "func requestChain("))
	if !strings.Contains(body, "return secureHeaders(rl.cors(") {
		t.Errorf("requestChain must return secureHeaders(rl.cors(...)); got:\n%s", body)
	}
}

// TestPerIPRefusalIsLoggedOncePerAddress (0.29.113, ASVS V16.3.3): the
// per-IP limiter's 429 is logged — once per address per badTokenMemory, so a
// hammering client cannot fill the log — with the address and the path.
func TestPerIPRefusalIsLoggedOncePerAddress(t *testing.T) {
	logs := &lockedBuffer{}
	rl, err := newRateLimiter(Options{RateLimitRPS: 1, RateLimitBurst: 1, RateLimitDaily: 1000})
	if err != nil {
		t.Fatal(err)
	}
	rl.logger = slog.New(slog.NewTextHandler(logs, nil))
	h := rl.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	hit := func(addr string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/repos/1/stats", nil)
		r.RemoteAddr = addr + ":1"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	codes := []int{hit("203.0.113.5"), hit("203.0.113.5"), hit("203.0.113.5"), hit("203.0.113.5")}
	if codes[0] != http.StatusOK || codes[1] != http.StatusTooManyRequests || codes[3] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v; want the first 200 (burst 1) and every later one refused", codes)
	}
	const line = "per-IP rate limit reached"
	if n := strings.Count(logs.String(), line); n != 1 {
		t.Fatalf("one address refused three times logged %d lines, want 1:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "address=203.0.113.5") || !strings.Contains(logs.String(), "path=/api/v1/repos/1/stats") {
		t.Errorf("the log line names neither the address nor the path:\n%s", logs.String())
	}
	hit("203.0.113.6")
	hit("203.0.113.6")
	if n := strings.Count(logs.String(), line); n != 2 {
		t.Errorf("a second address refused logged %d lines in all, want 2", n)
	}
}
