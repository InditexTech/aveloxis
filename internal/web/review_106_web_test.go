// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/admin"
	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/static"
)

// The 0.29.106 whole-branch review of PR #238, web area.

func storelessServer(t *testing.T, logs io.Writer) *Server {
	t.Helper()
	return New(nil, config.WebConfig{Addr: ":0", APIInternalURL: "http://127.0.0.1:1", DevMode: true},
		"", slog.New(slog.NewTextHandler(logs, nil)))
}

// multipartTempFiles counts the temp files mime/multipart leaves when a
// part overflows its memory budget (os.CreateTemp(dir, "multipart-")).
func multipartTempFiles(t *testing.T) int {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(os.TempDir(), "multipart-*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(names)
}

// TestCSRFGuardBoundsAndRestrictsFormBodies (W1): the guard parses a form
// to read its csrf_token for any request that carries a session cookie —
// before the mux, before any gate. Until 0.29.106 that was PostFormValue,
// i.e. ParseMultipartForm with a 32 MiB memory budget and temp files for
// the rest, bounded only by the read timeout, for an ANONYMOUS request on
// any path with any cookie value. Now: every non-GET body on a form route
// is bounded by formBodyMaxBytes (one http.MaxBytesReader, which the
// handlers' own ParseForm then sees too), only
// application/x-www-form-urlencoded is parsed (no page posts multipart),
// an overrun is a 413 and another content type a 415, both logged.
func TestCSRFGuardBoundsAndRestrictsFormBodies(t *testing.T) {
	var logs lockedBuffer
	s := storelessServer(t, &logs)
	h := s.Handler()

	t.Run("anonymous multipart body is refused before any parse", func(t *testing.T) {
		before := multipartTempFiles(t)
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, err := mw.CreateFormFile("f", "f.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(bytes.Repeat([]byte{'x'}, 33<<20)); err != nil { // one part past the 32 MiB memory budget
			t.Fatal(err)
		}
		_ = mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/no/such/route", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		r.AddCookie(&http.Cookie{Name: s.cookieName("aveloxis_session"), Value: "bogus"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		after := multipartTempFiles(t)
		if after > before {
			names, _ := filepath.Glob(filepath.Join(os.TempDir(), "multipart-*"))
			for _, n := range names {
				_ = os.Remove(n)
			}
			t.Errorf("the guard parsed a multipart body to disk: %d new temp file(s)", after-before)
		}
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("an anonymous multipart POST = %d; want 415 before any parse", rec.Code)
		}
		if !strings.Contains(logs.String(), "level=WARN") {
			t.Errorf("the refusal is not logged:\n%s", logs.String())
		}
	})

	withSession := func(t *testing.T, r *http.Request) {
		t.Helper()
		token := plantSession(t, s, "w1-"+strconv.Itoa(len(logs.String())), &Session{UserID: 1, LoginName: "w1", ExpiresAt: time.Now().Add(time.Hour)})
		r.AddCookie(&http.Cookie{Name: s.cookieName("aveloxis_session"), Value: token})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-csrf-token"})
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	t.Run("a form one byte over the bound is a 413", func(t *testing.T) {
		prefix := "csrf_token=test-csrf-token&group_id=1&repo_urls="
		body := prefix + strings.Repeat("a", int(formBodyMaxBytes)+1-len(prefix))
		r := httptest.NewRequest(http.MethodPost, "/groups/add-repo", strings.NewReader(body))
		withSession(t, r)
		code, _, reached := serve(h, r)
		if reached || code != http.StatusRequestEntityTooLarge {
			t.Errorf("a %d-byte form = %d (handler reached=%v); want 413 before the handler", len(body), code, reached)
		}
	})

	t.Run("the largest legal paste reaches the handler", func(t *testing.T) {
		one := "https://github.com/o/" + strings.Repeat("r", db.MaxAddURLBytes-len("https://github.com/o/"))
		lines := make([]string, admin.MaxPasteURLs)
		for i := range lines {
			lines[i] = one
		}
		form := url.Values{"csrf_token": {"test-csrf-token"}, "group_id": {"1"}, "repo_urls": {strings.Join(lines, "\n")}}
		body := form.Encode()
		if int64(len(body)) > formBodyMaxBytes {
			t.Fatalf("the largest legal paste encodes to %d bytes, over formBodyMaxBytes %d", len(body), formBodyMaxBytes)
		}
		r := httptest.NewRequest(http.MethodPost, "/groups/add-repo", strings.NewReader(body))
		withSession(t, r)
		code, _, reached := serve(h, r)
		if !reached {
			t.Errorf("the largest legal paste (%d bytes) = %d and never reached the handler", len(body), code)
		}
	})
}

// TestFormBodyBoundIsDerived (W1): the bound is the largest legal form —
// admin.MaxPasteURLs URLs of db.MaxAddURLBytes, each byte percent-encoded
// (three bytes), the CRLF a textarea submits per line (%0D%0A), and the fixed fields — not a
// number someone liked.
func TestFormBodyBoundIsDerived(t *testing.T) {
	if got := len(rand.Text()); got != csrfTokenLen {
		t.Fatalf("rand.Text() is %d characters; csrfTokenLen says %d", got, csrfTokenLen)
	}
	if got := len(strconv.FormatInt(math.MaxInt64, 10)); got != maxInt64Digits {
		t.Fatalf("an int64 has %d digits; maxInt64Digits says %d", got, maxInt64Digits)
	}
	// The browser's maximal encoding: every URL byte percent-encoded (a
	// 0xFF byte is %FF), lines joined by the CRLF a textarea submits, the
	// widest group id and a real token.
	line := strings.Repeat("\xff", db.MaxAddURLBytes)
	lines := make([]string, admin.MaxPasteURLs)
	for i := range lines {
		lines[i] = line
	}
	form := url.Values{"csrf_token": {rand.Text()}, "group_id": {strconv.FormatInt(math.MaxInt64, 10)}, "repo_urls": {strings.Join(lines, "\r\n")}}
	body := int64(len(form.Encode()))
	if body > formBodyMaxBytes {
		t.Errorf("the maximal legal paste encodes to %d bytes, over formBodyMaxBytes %d", body, formBodyMaxBytes)
	}
	if slack := formBodyMaxBytes - body; slack > int64(len(formLineTerminator)) {
		t.Errorf("formBodyMaxBytes %d leaves %d bytes over the maximal legal paste (%d); the bound is the derivation, not an allowance", formBodyMaxBytes, slack, body)
	}
}

// TestRemoveConfirmAnswers404ForARepoThatIsGone (W5, AVELOXIS_TEST_DB): a
// group's link to a repository whose row is gone (dedup-repos, a
// consolidation between the authorization and the read) is the caller's
// 404 on the confirmation page, as it is on the repository page — not a
// logged 500.
func TestRemoveConfirmAnswers404ForARepoThatIsGone(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	store, err := db.NewPostgresStore(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	const login = "_avweb_remove_confirm_gone"
	pool := store.Pool()
	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_repos WHERE group_id IN (SELECT group_id FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1))`, login)
		_, _ = pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_session_tokens WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	}
	clean()
	t.Cleanup(clean)
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	gid, err := store.CreateUserGroup(ctx, uid, "remove confirm probe")
	if err != nil {
		t.Fatal(err)
	}
	const goneRepo = int64(9_000_000_007) // user_repos.repo_id carries no FK: a link to a row that is gone
	if _, err := pool.Exec(ctx, `INSERT INTO aveloxis_ops.user_repos (group_id, repo_id) VALUES ($1, $2)`, gid, goneRepo); err != nil {
		t.Fatal(err)
	}
	var logs lockedBuffer
	s := New(store, config.WebConfig{Addr: ":0", APIInternalURL: "http://127.0.0.1:1", DevMode: true}, "", slog.New(slog.NewTextHandler(&logs, nil)))
	token := plantSession(t, s, "remove-confirm", &Session{UserID: uid, LoginName: login, ExpiresAt: time.Now().Add(time.Hour)})
	r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/groups/%d/repos/%d/remove", gid, goneRepo), nil)
	r.AddCookie(&http.Cookie{Name: s.cookieName("aveloxis_session"), Value: token})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Errorf("the confirmation page for a repository whose row is gone = %d; want 404 (logs:\n%s)", rec.Code, logs.String())
	}
}

// TestLogoutClearsEveryAveloxisCookie (W7): the login page's control reads
// "Sign out & clear cookies"; sign-out expires every cookie this process
// sets — the session, the form token, and the two OAuth cookies — through
// the one constructor.
func TestLogoutClearsEveryAveloxisCookie(t *testing.T) {
	s := storelessServer(t, io.Discard)
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusFound {
		t.Fatalf("POST /logout = %d; want 302", rec.Code)
	}
	expired := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 && c.Value == "" {
			expired[c.Name] = true
		}
	}
	for _, name := range []string{"aveloxis_session", csrfCookieName, "oauth_state", "oauth_next"} {
		if !expired[name] {
			t.Errorf("sign-out does not expire the %s cookie (expired: %v)", name, expired)
		}
	}
}

// TestSetUserAdminRequiresABooleanField (A2): the API twin refuses `{}`
// (58 §7 item 18: a missing field demoted the account); the web form is
// the same operation, so an absent or non-boolean `admin` field is a 400
// before the service — exactly "true" or "false" reaches it.
func TestSetUserAdminRequiresABooleanField(t *testing.T) {
	var logs lockedBuffer
	s := storelessServer(t, &logs)
	h := s.Handler()
	for i, tc := range []struct {
		body string
		want int
	}{
		{"csrf_token=test-csrf-token", http.StatusBadRequest},
		{"csrf_token=test-csrf-token&admin=maybe", http.StatusBadRequest},
		{"csrf_token=test-csrf-token&admin=", http.StatusBadRequest},
		{"csrf_token=test-csrf-token&admin=false", 0}, // reaches the service (the nil store panics: reached)
		{"csrf_token=test-csrf-token&admin=true", 0},
	} {
		token := plantSession(t, s, "a2-"+strconv.Itoa(i), &Session{UserID: 1, LoginName: "root", IsAdmin: true, ExpiresAt: time.Now().Add(time.Hour)})
		r := httptest.NewRequest(http.MethodPost, "/admin/users/5/admin", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: s.cookieName("aveloxis_session"), Value: token})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-csrf-token"})
		code, _, reached := serve(h, r)
		if tc.want == 0 {
			if !reached {
				t.Errorf("%q: = %d and never reached the service", tc.body, code)
			}
			continue
		}
		if reached || code != tc.want {
			t.Errorf("%q: = %d (service reached=%v); want %d before the service", tc.body, code, reached, tc.want)
		}
	}
}

// fixedWidthInline is an inline fixed width in px (any spacing after the
// colon; min-width is not it — the live ones sit under the 320 px content
// width): one oracle for the templates and the hand-written admin pages.
var fixedWidthInline = regexp.MustCompile(`[;"\s]width:\s*\d+px`)

// TestChartGridsAndInputsReflow (W2, the operator's no-horizontal-scrolling
// rule): the four-chart grids and the compare search box were inline
// styles — two 1fr tracks over canvases whose intrinsic width is 300 px
// (660 px at a 500 px viewport) and a 350 px box (438 px at 414). The
// layout lives in app.css: tracks that wrap, canvases that shrink, a box
// that is at most 350 px. No template carries a fixed pixel width or a
// fixed track list.
func TestChartGridsAndInputsReflow(t *testing.T) {
	var css string
	for _, a := range static.Assets() {
		if a.Path == "app.css" {
			css = string(a.Body)
		}
	}
	for _, needle := range []string{".chart-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,280px),1fr))", "canvas{max-width:100%}", ".search-box{position:relative;width:100%;max-width:350px}"} {
		if !strings.Contains(css, needle) {
			t.Errorf("app.css lacks %q", needle)
		}
	}
	fixedWidth := fixedWidthInline
	for _, needle := range []string{"grid-template-columns:1fr 1fr", "grid-template-columns: 1fr 1fr"} {
		if strings.Contains(allTemplates, needle) {
			t.Errorf("a template still fixes its chart tracks: %q", needle)
		}
	}
	if m := fixedWidth.FindString(allTemplates); m != "" {
		t.Errorf("a template carries a fixed pixel width %q; widths are max-widths in app.css", m)
	}
	for _, page := range []string{`<div class="chart-grid">`, `<div class="search-box">`} {
		if !strings.Contains(allTemplates, page) {
			t.Errorf("no template uses %s", page)
		}
	}
}

// TestAdminPagesCarryTheViewportAndStackedTables (W8, the same rule): the
// two administrator pages (approvals, users) were hand-written HTML
// outside the template set — plain seven-column tables with no viewport
// meta, sideways scrolling on a phone. Since 0.29.109 (stage 3a) they are
// templates; rendered through the production parse with the golden
// fixtures, each carries the viewport meta, the shared stylesheet and
// stacked tables (.tbl-stack with data-label cells), and the template text
// fixes no pixel width.
func TestAdminPagesCarryTheViewportAndStackedTables(t *testing.T) {
	tmpl := parseTemplates()
	for _, name := range []string{"admin_approvals", "admin_users"} {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, renderFixtures[name]); err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		page := buf.String()
		for _, needle := range []string{`name="viewport"`, `href="/static/app.css"`, `class="tbl-stack"`, `data-label="`} {
			if !strings.Contains(page, needle) {
				t.Errorf("%s: the page lacks %s", name, needle)
			}
		}
		src, err := templateFS.ReadFile("templates/" + name + ".html")
		if err != nil {
			t.Fatal(err)
		}
		if m := fixedWidthInline.FindString(string(src)); m != "" {
			t.Errorf("%s: %q — an inline fixed width on a phone is a horizontal scroll (operator rule)", name, m)
		}
		if strings.Contains(page, "max-width: 28rem") || strings.Contains(page, "<table><tr>") {
			t.Errorf("%s: the page still lays its table out for a wide screen", name)
		}
	}
}
