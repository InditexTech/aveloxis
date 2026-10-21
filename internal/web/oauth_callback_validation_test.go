// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

// Source-contract tests for the OAuth callback handlers' defensive
// validation. The motivating incident: GitHub's /user response on
// one production callback returned without a `login` field; the
// handler json.Unmarshal'd into a zero-valued struct, passed
// Login="" through to UpsertOAuthUser, and inserted a junk
// user row that shadowed the real account on every subsequent
// login. The fix is layered:
//
//   1. handleGitHubCallback / handleGitLabCallback check the HTTP
//      status of the user-info response.
//   2. They check json.Unmarshal returned without error.
//   3. They check the resulting Login/Username field is non-empty
//      BEFORE invoking UpsertOAuthUser.
//
// UpsertOAuthUser still rejects an empty Login as a last line of
// defense (covered in internal/db/web_store_oauth_test.go), but the
// handler-side checks let us produce a useful error page instead of
// a generic 500 from the database layer.

package web

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestGitHubCallbackChecksHTTPStatus pins that the GitHub callback
// rejects a non-200 response from /user. Without this, a transient
// 401/403/5xx on the user-info fetch silently produced a zero-value
// TestOAuthHeadChecksStatusBeforeDecoding (0.29.102, WEB-14): both
// callbacks share fetchOAuthUser, so the status check and the decode's
// error arm are pinned once, in ORDER — a non-200 response can carry an
// empty body that unmarshals to zero values (the blank-user incident).
func TestOAuthHeadChecksStatusBeforeDecoding(t *testing.T) {
	body := srctest.StripGoComments(srctest.PackageFunc(t, "internal/web", "func (s *Server) fetchOAuthUser("))
	status := strings.Index(body, "resp.StatusCode != http.StatusOK")
	decode := strings.Index(body, "json.Unmarshal(body, out); err != nil")
	if status < 0 || decode < 0 || status > decode {
		t.Errorf("fetchOAuthUser must refuse a non-200 before decoding and check the decode error (status at %d, decode at %d)", status, decode)
	}
	for _, h := range []string{"handleGitHubCallback", "handleGitLabCallback"} {
		if !strings.Contains(srctest.PackageFunc(t, "internal/web", "func (s *Server) "+h+"("), "s.fetchOAuthUser(w, r, ctx, s.") {
			t.Errorf("%s does not read its identity through fetchOAuthUser", h)
		}
	}
}

// TestGitHubCallbackRejectsEmptyLogin pins the central invariant:
// no UpsertOAuthUser call until ghUser.Login is verified non-empty.
// Source-contract check: the function body must contain a
// `ghUser.Login == ""` (or equivalent) test BEFORE the call to
// UpsertOAuthUser.
func TestGitHubCallbackRejectsEmptyLogin(t *testing.T) {
	body := extractWebFunc(t, "handleGitHubCallback")
	if body == "" {
		t.Fatal("could not locate handleGitHubCallback function body")
	}
	// The callback hands the login to completeOAuthLogin, which signs the
	// user in (SignInOAuthUser). Comments are stripped: until the final
	// review's round 3 this found "UpsertOAuthUser" in a comment, not a
	// call, and would have passed with the check below after the handoff.
	body = srctest.StripGoComments(body)
	upsertIdx := strings.Index(body, "s.completeOAuthLogin(")
	if upsertIdx < 0 {
		t.Fatal("handleGitHubCallback must hand the login to completeOAuthLogin")
	}
	preUpsert := body[:upsertIdx]
	// Accept any of the common empty-string check patterns for the
	// Login field (== "", strings.TrimSpace, len(...)==0).
	hasCheck := strings.Contains(preUpsert, `ghUser.Login == ""`) ||
		strings.Contains(preUpsert, `ghUser.Login==""`) ||
		(strings.Contains(preUpsert, "ghUser.Login") && strings.Contains(preUpsert, "TrimSpace"))
	if !hasCheck {
		t.Error("handleGitHubCallback must verify ghUser.Login is non-empty BEFORE calling UpsertOAuthUser — an empty Login on /user response is the root cause of the blank-user incident")
	}
}

// TestGitLabCallbackRejectsEmptyUsername pins the same invariant
// for the GitLab path. GitLab's user object exposes Username (not
// Login).
func TestGitLabCallbackRejectsEmptyUsername(t *testing.T) {
	body := extractWebFunc(t, "handleGitLabCallback")
	if body == "" {
		t.Fatal("could not locate handleGitLabCallback function body")
	}
	// v0.27.42: the callback hands off to completeOAuthLogin (the
	// shared tail that calls UpsertOAuthUser) — the invariant is now
	// "empty-username check BEFORE the handoff".
	upsertIdx := strings.Index(body, "completeOAuthLogin")
	if upsertIdx < 0 {
		t.Fatal("handleGitLabCallback must hand off to completeOAuthLogin (which performs UpsertOAuthUser)")
	}
	preUpsert := body[:upsertIdx]
	hasCheck := strings.Contains(preUpsert, `glUser.Username == ""`) ||
		strings.Contains(preUpsert, `glUser.Username==""`) ||
		(strings.Contains(preUpsert, "glUser.Username") && strings.Contains(preUpsert, "TrimSpace"))
	if !hasCheck {
		t.Error("handleGitLabCallback must verify glUser.Username is non-empty BEFORE calling UpsertOAuthUser")
	}
}

// extractWebFunc returns the body of the named method on *Server wherever
// it lives in the package (v0.29.98: it read server.go by name and
// returned "" — a vacuous pass — when the method was not there).
func extractWebFunc(t *testing.T, name string) string {
	t.Helper()
	return srctest.PackageFunc(t, "internal/web", "func (s *Server) "+name+"(")
}
