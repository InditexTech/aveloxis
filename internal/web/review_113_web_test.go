// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/httpserver"
	"github.com/aveloxis/aveloxis/internal/srctest"
)

// Review round 1 of 0.29.113 (High): outside dev_mode the page renderer
// must read the form token from the __Host- cookie the guard checks. A
// browser still carrying the pre-release bare aveloxis_csrf rendered that
// value into every form while the guard compared __Host-aveloxis_csrf, so
// every POST was refused for up to a day after the deploy.
func TestProductionFormsRenderTheTokenTheGuardChecks(t *testing.T) {
	s := New(nil, config.WebConfig{DevMode: false}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := httptest.NewRequest(http.MethodGet, "/account", nil)
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "OLD"})
	r.AddCookie(&http.Cookie{Name: s.cookieName(csrfCookieName), Value: "NEW"})
	w := httptest.NewRecorder()
	if got := s.csrfToken(w, r); got != "NEW" {
		t.Errorf("the form token rendered = %q; want the __Host- cookie's NEW (the guard reads that one)", got)
	}
	if sc := w.Header().Values("Set-Cookie"); len(sc) != 0 {
		t.Errorf("a browser with a form token was given another: %v (a second tab's form would then be refused)", sc)
	}
	// With only the bare one (the old release's), a new prefixed token is minted.
	r2 := httptest.NewRequest(http.MethodGet, "/account", nil)
	r2.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "OLD"})
	w2 := httptest.NewRecorder()
	if got := s.csrfToken(w2, r2); got == "OLD" || !strings.Contains(strings.Join(w2.Header().Values("Set-Cookie"), ";"), s.cookieName(csrfCookieName)+"=") {
		t.Errorf("only a bare cookie: token %q, Set-Cookie %v; want a fresh __Host- token", got, w2.Header().Values("Set-Cookie"))
	}
}

// Review round 1 of 0.29.113: "Sign in again" on the e-mail page links to
// /login?next=/account/email; the login page must carry a safe next into
// the provider links (beginOAuth stashes it), and drop an unsafe one.
func TestLoginCarriesASafeNextIntoTheProviderLinks(t *testing.T) {
	s := New(nil, config.WebConfig{DevMode: true, GitHubClientID: "id", GitHubClientSecret: "secret"}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	get := func(target string) string {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		return strings.ToLower(w.Body.String())
	}
	if body := get("/login?next=%2Faccount%2Femail"); !strings.Contains(body, `href="/auth/github?next=%2faccount%2femail"`) {
		t.Errorf("the login page drops a safe next; body:\n%s", body)
	}
	for _, bad := range []string{"%2F%2Fevil.example", "https%3A%2F%2Fevil.example", "%2F%5Cevil.example"} {
		if body := get("/login?next=" + bad); strings.Contains(body, "/auth/github?next") {
			t.Errorf("the login page carries an unsafe next %q into the provider link", bad)
		}
	}
}

// Review round 1 of 0.29.113 (L17, two processes): the web process sets
// nosniff and HSTS on every answer before its /api/ proxy runs, and the api
// process sets them too; httputil.ReverseProxy ADDS the upstream's headers,
// so a proxied answer carried each twice. The proxy's response hook drops
// the api's copies (the web's own stand), and the hook is the one wired.
func TestProxiedAPIAnswersCarryEachSecurityHeaderOnce(t *testing.T) {
	s := New(nil, config.WebConfig{DevMode: false}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	resp.Header.Set("X-Content-Type-Options", "nosniff")
	resp.Header.Set("Strict-Transport-Security", httpserver.HSTSValue)
	resp.Header.Set("Content-Type", "application/json")
	if err := s.modifyProxyResponse(resp); err != nil {
		t.Fatal(err)
	}
	for _, h := range proxyStrippedHeaders {
		if got := resp.Header.Values(h); len(got) != 0 {
			t.Errorf("the api's %s survives the proxy hook (%v); the web's own copy is already set, so the answer would carry two", h, got)
		}
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Error("the hook must leave the api's other headers alone")
	}
	want := map[string]bool{"X-Content-Type-Options": true, "Strict-Transport-Security": true}
	for _, h := range proxyStrippedHeaders {
		delete(want, h)
	}
	if len(want) != 0 {
		t.Errorf("proxyStrippedHeaders misses %v", want)
	}
	body := srctest.StripGoComments(srctest.PackageFunc(t, "internal/web", "func New("))
	if !strings.Contains(body, "rp.ModifyResponse = s.modifyProxyResponse") {
		t.Error("the /api/ proxy must be wired with s.modifyProxyResponse")
	}
}
