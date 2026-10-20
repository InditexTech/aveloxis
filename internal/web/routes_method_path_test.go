// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestEveryWebRouteIsMethodAndPath (0.29.102, stage 2a of summary/59,
// WEB-8): one routing style. Every pattern Handler registers names its
// method; the handlers decide nothing about the method or the path shape
// themselves (no `r.Method`, no path splitter), so stage 2's CSRF
// middleware sees every state change as a POST route and B3's POST-only
// logout and token exchange are pattern changes. The API reverse proxy is
// the one method-less registration (it forwards whatever the API routes).
func TestEveryWebRouteIsMethodAndPath(t *testing.T) {
	for _, rt := range registeredRoutes(t) {
		method, _, ok := strings.Cut(rt.pattern, " ")
		if rt.pattern == "/api/" {
			continue
		}
		if !ok || (method != http.MethodGet && method != http.MethodPost) {
			t.Errorf("Handler registers %q without a GET or POST method", rt.pattern)
		}
	}
	// No handler decides the method or the path shape itself; the guard
	// (csrfGuard) is the one reader of r.Method, by design.
	for path, file := range srctest.PackageFiles(t, "internal/web", 10) {
		file = srctest.StripGoComments(file)
		for name, body := range srctest.FuncParts(t, file) {
			if strings.Contains(name, "csrfGuard") {
				continue
			}
			for _, needle := range []string{"r.Method", `strings.TrimPrefix(r.URL.Path, "/groups/")`, "StatusMethodNotAllowed"} {
				if strings.Contains(body, needle) {
					t.Errorf("%s: %s decides %s itself; the mux pattern decides the method and the path", path, name, needle)
				}
			}
		}
	}
}

// TestStateChangesNeverRideAGet (B3/D-13): /logout, /auth/logout and
// /auth/token answer 405 to GET and act on POST; an unknown sub-path under a
// group is a 404, not the group page (the prefix splitter served
// /groups/1/x/y as group 1).
func TestStateChangesNeverRideAGet(t *testing.T) {
	s := New(nil, config.WebConfig{Addr: ":0", APIInternalURL: "http://127.0.0.1:1", DevMode: true},
		"", slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := s.Handler()
	planted := 0
	withSession := func(r *http.Request) *http.Request {
		planted++
		token := plantSession(t, s, "method-path-"+strconv.Itoa(planted), &Session{UserID: 1, LoginName: "m", ExpiresAt: time.Now().Add(time.Hour)})
		r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: token})
		csrf(r) // the session's form token (stage 2f)
		return r
	}
	for _, tc := range []struct {
		method, path string
		session      bool
		want         int
	}{
		{http.MethodGet, "/logout", true, http.StatusMethodNotAllowed},
		{http.MethodPost, "/logout", false, http.StatusFound}, // acts (no cookie: nothing to delete in this store-less server)
		{http.MethodGet, "/auth/logout", true, http.StatusMethodNotAllowed},
		{http.MethodPost, "/auth/logout", false, http.StatusFound},
		{http.MethodGet, "/auth/token", true, http.StatusMethodNotAllowed},
		{http.MethodGet, "/groups/new", true, http.StatusNotFound},
		{http.MethodGet, "/groups/1/x/y", true, http.StatusNotFound},
		{http.MethodGet, "/groups/1/repos/2/sbom/extra", true, http.StatusNotFound},
		{http.MethodGet, "/groups/abc", true, http.StatusNotFound},
		{http.MethodGet, "/groups/add-repo", true, http.StatusNotFound},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.session {
			r = withSession(r)
		}
		code, _, reached := serve(h, r)
		if (reached && tc.session) || code != tc.want {
			t.Errorf("%s %s: got %d (handler reached=%v), want %d", tc.method, tc.path, code, reached, tc.want)
		}
	}
}
