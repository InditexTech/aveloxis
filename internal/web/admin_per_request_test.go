// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/srctest"
)

// plantSession gives the server a session the tests can send as the
// cookie value `name` (0.29.102, stage 2c): with a store, a REAL web
// session row for sess.UserID (deleted at cleanup) that `name` maps to,
// so the gate still reads the users row (privilege, expiry); without a
// store, the literal itself. Returns name.
func plantSession(t testing.TB, s *Server, name string, sess *Session) string {
	t.Helper()
	prev := s.lookupSession
	if s.store == nil {
		// Without a store the /api/ proxy has no minter (stage 4, 0.29.110):
		// a planted session mints a planted Bearer, so the proxy tests of
		// forwarding and bounds run as before. A test of the minting itself
		// sets its own minter after planting.
		s.mintProxyToken = func(_ context.Context, cookie string) (string, error) { return "planted-" + cookie, nil }
	}
	if s.store != nil {
		raw, err := s.store.CreateWebSession(context.Background(), sess.UserID)
		if err != nil {
			t.Fatalf("plantSession(%q) for user %d: %v", name, sess.UserID, err)
		}
		t.Cleanup(func() { _ = s.store.DeleteWebSession(context.Background(), raw) })
		s.lookupSession = func(ctx context.Context, tok string) (*Session, error) {
			if tok == name {
				tok = raw
			}
			return prev(ctx, tok)
		}
		// The gate stores the cookie as sent (the name), and the proxy's
		// minter needs the real session (round-1 review of 0.29.110, I-3):
		// map the name back for the mint too.
		prevMint := s.mintProxyToken
		s.mintProxyToken = func(ctx context.Context, cookie string) (string, error) {
			if cookie == name {
				cookie = raw
			}
			return prevMint(ctx, cookie)
		}
		return name
	}
	return plantStubSession(t, s, name, sess)
}

// plantStubSession plants the literal whatever the store: for a server
// whose store is deliberately down (a closed pool), where the handler's
// own error path is the subject and the gate must not be the one to fail.
func plantStubSession(t testing.TB, s *Server, name string, sess *Session) string {
	t.Helper()
	prev := s.lookupSession
	planted := *sess
	s.lookupSession = func(ctx context.Context, tok string) (*Session, error) {
		if tok == name {
			c := planted
			return &c, nil
		}
		if s.store == nil {
			return nil, db.ErrNoWebSession
		}
		return prev(ctx, tok)
	}
	return name
}

// TestAdminPrivilegeIsDecidedPerRequest (0.29.102, stage 2b of summary/59,
// D-1 and WEB-7): the gate asks the users row on EVERY request. A session
// created while the account was an administrator loses /monitor the moment
// the row says otherwise — no re-login (v0.19.0 cached the flag for the
// session's lifetime). A lookup error is a 500 and an ERROR log line,
// never "not an administrator" (SR-5) and never the handler.
func TestAdminPrivilegeIsDecidedPerRequest(t *testing.T) {
	var logs strings.Builder
	s := New(nil, config.WebConfig{Addr: ":0", APIInternalURL: "http://127.0.0.1:1", DevMode: true},
		"", slog.New(slog.NewTextHandler(&logs, nil)))
	answer, fail := true, false
	s.lookupSession = func(context.Context, string) (*Session, error) {
		if fail {
			return nil, errors.New("users row unreadable")
		}
		return &Session{UserID: 42, LoginName: "ada", IsAdmin: answer, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	h := s.Handler()
	get := func(path string) (int, bool) {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: "ada"})
		code, _, reached := serve(h, r)
		return code, reached
	}
	if _, reached := get("/monitor"); !reached {
		t.Fatal("an administrator's request did not reach /monitor's handler")
	}
	answer = false
	if code, reached := get("/monitor"); reached || code != http.StatusForbidden {
		t.Errorf("after the demotion the same session got %d (reached=%v) on /monitor; want 403 before the handler, without a new login", code, reached)
	}
	if _, reached := get("/dashboard"); !reached {
		t.Error("the demoted account no longer reaches /dashboard: privilege is not a login")
	}
	fail = true
	if code, reached := get("/dashboard"); reached || code != http.StatusInternalServerError {
		t.Errorf("a session lookup error got %d (reached=%v); want 500 before the handler (SR-5)", code, reached)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "users row unreadable") {
		t.Errorf("the lookup error was not logged at ERROR with its cause; log:\n%s", logs.String())
	}
}

// TestNoHandlerLooksTheSessionUpTwice: the gates resolve the session once
// and hand it over (authedHandler); the login page's "HasSession" is the
// one handler-side read left, on a public route.
func TestNoHandlerLooksTheSessionUpTwice(t *testing.T) {
	allowed := map[string]bool{"hasSession": true, "resolveSession": true, "getSession": true} // the two public pages ask hasSession
	files := srctest.PackageFiles(t, "internal/web", 10)
	var src strings.Builder
	for path, file := range files {
		file = srctest.StripGoComments(file)
		src.WriteString(file)
		for name, body := range srctest.FuncParts(t, file) {
			short := name
			if i := strings.LastIndex(name, ")"); i >= 0 {
				short = strings.TrimSpace(name[i+1:])
			}
			if j := strings.Index(short, "("); j >= 0 {
				short = short[:j]
			}
			if strings.Contains(body, "s.getSession(r)") && !allowed[short] {
				t.Errorf("%s: %s looks the session up itself; take it from the gate (authedHandler)", path, short)
			}
		}
	}
	for _, needle := range []string{"IsUserAdmin(", "sessionMu", "map[string]*Session"} {
		if strings.Contains(src.String(), needle) {
			t.Errorf("internal/web still contains %q: privilege is decided by the gate per request, never at login", needle)
		}
	}
	_ = io.Discard
}

// TestBadAdminIdsAre400BeforeTheStore: an id that is not an id on an
// administrator's route is a 400 before any store read in the handler
// (the gate's own read is stubbed here).
func TestBadAdminIdsAre400BeforeTheStore(t *testing.T) {
	s := New(nil, config.WebConfig{Addr: ":0", APIInternalURL: "http://127.0.0.1:1", DevMode: true},
		"", slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := s.Handler()
	token := plantSession(t, s, "root", &Session{UserID: 7, LoginName: "root", IsAdmin: true, ExpiresAt: time.Now().Add(time.Hour)})
	for _, path := range []string{"/admin/users/x/admin", "/admin/users/0/admin", "/admin/users/99999999999999999999/admin", "/admin/users/4294967296/admin", "/admin/groups/-1/reject", "/admin/add-requests/x/approve", "/monitor/prioritize/x"} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("admin=true"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: token})
		csrf(r)
		code, _, reached := serve(h, r)
		if reached || code != http.StatusBadRequest {
			t.Errorf("POST %s = %d (store reached=%v); want 400 before the store", path, code, reached)
		}
	}
}
