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
	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestEverySetCookieCarriesTheHouseAttributes (0.29.102, stage 2a,
// WEB-13): one cookie constructor. Every Set-Cookie the web process emits —
// the session, the OAuth state, the stashed ?next=, and every deletion —
// carries HttpOnly, Secure unless dev_mode, Path=/ and SameSite=Lax; the
// expiry cookies used to omit SameSite. The constructor is the only
// `http.Cookie{` literal in the package, so a sixth cookie cannot restate
// the rule differently.
func TestEverySetCookieCarriesTheHouseAttributes(t *testing.T) {
	for _, dev := range []bool{false, true} {
		s := New(nil, config.WebConfig{Addr: ":0", APIInternalURL: "http://127.0.0.1:1", DevMode: dev,
			GitHubClientID: "id", GitHubClientSecret: "secret", SPAURL: "http://localhost:8000"},
			"", slog.New(slog.NewTextHandler(io.Discard, nil)))
		h := s.Handler()
		var cookies []*http.Cookie
		for _, req := range []*http.Request{
			httptest.NewRequest(http.MethodGet, "/auth/github?next=http://localhost:8000/hub.html", nil),
			httptest.NewRequest(http.MethodPost, "/logout", nil),
		} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			cookies = append(cookies, rec.Result().Cookies()...)
		}
		rec := httptest.NewRecorder()
		s.clearNext(rec)
		cookies = append(cookies, rec.Result().Cookies()...)
		cookies = append(cookies, s.sessionCookie("x"))
		if len(cookies) < 6 {
			t.Fatalf("dev=%v: only %d cookies observed; the drive no longer reaches every emitter", dev, len(cookies))
		}
		for _, c := range cookies {
			if !c.HttpOnly || c.Secure == dev || c.Path != "/" || c.SameSite != http.SameSiteLaxMode {
				t.Errorf("dev=%v: cookie %q: HttpOnly=%v Secure=%v Path=%q SameSite=%v; want HttpOnly, Secure=%v, Path=/, Lax", dev, c.Name, c.HttpOnly, c.Secure, c.Path, c.SameSite, !dev)
			}
		}
	}
	src := srctest.StripGoComments(srctest.PackageSource(t, "internal/web", 10))
	if n := strings.Count(src, "http.Cookie{"); n != 1 {
		t.Errorf("internal/web spells http.Cookie{ %d times; the one constructor is Server.cookie", n)
	}
}
