// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestDecodeJSONBodyIsOneValueThenEOF (0.29.104, Copilot on PR #238,
// review 5566438301): the one bounded JSON reader accepts exactly one
// value followed by nothing but whitespace. `Decoder.More()` was the
// trailing-data check; it answers false for a stray `}` or `]` AND on any
// reader error, so `{"admin":true}}` and a value followed by whitespace
// past the limit both reached the writers. The rule is "a second Decode
// must answer io.EOF": anything else — a second value, a stray delimiter,
// the reader's own error — refuses the body.
func TestDecodeJSONBodyIsOneValueThenEOF(t *testing.T) {
	const limit = 64
	for _, tc := range []struct {
		name, body string
		refused    bool
	}{
		{"one object", `{"admin":true}`, false},
		{"one object then whitespace", "{\"admin\":true}\n \t\r\n", false},
		{"stray closing brace", `{"admin":true}}`, true},
		{"stray closing bracket", `{"admin":true}]`, true},
		{"second value", `{"admin":true}{"admin":false}`, true},
		{"second value after whitespace", `{"admin":true} {"admin":false}`, true},
		{"trailing word", `{"admin":true} x`, true},
		{"trailing whitespace past the limit", `{"admin":true}` + strings.Repeat(" ", limit), true},
		{"truncated second value", `{"admin":true}{"admin":`, true}, // io.ErrUnexpectedEOF is not io.EOF (round 1 F2)
		{"body of exactly limit bytes", `{"admin":true}` + strings.Repeat(" ", limit-len(`{"admin":true}`)), false},
		{"body of limit+1 bytes", `{"admin":true}` + strings.Repeat(" ", limit+1-len(`{"admin":true}`)), true},
		{"unterminated object", `{"admin":`, true},
		{"object past the limit", `{"admin":true,"note":"` + strings.Repeat("x", limit) + `"}`, true},
		{"empty body", ``, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			var v struct {
				Admin *bool `json:"admin"`
			}
			err := decodeJSONBody(w, r, limit, &v)
			if (err != nil) != tc.refused {
				t.Fatalf("body %q: err=%v; want refused=%v", tc.body, err, tc.refused)
			}
			if !tc.refused && (v.Admin == nil || !*v.Admin) {
				t.Errorf("body %q: decoded %+v; want admin=true", tc.body, v)
			}
		})
	}
}

// TestAdminRoutesRefuseTrailingJSONBeforeTheService: the review's body on
// the role route, through the handler: a 400 before the service and the
// store (the Server has no store; a handler that reached it would panic,
// reported as "reached").
func TestAdminRoutesRefuseTrailingJSONBeforeTheService(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.admin = testAdminService(s)
	admin := context.WithValue(context.Background(), authCtxKey{}, authInfo{UserID: 1, IsAdmin: true})
	for _, tc := range []struct {
		name, path, body string
		call             func(w http.ResponseWriter, r *http.Request)
	}{
		{"set admin", "/api/v1/admin/users/7/admin", `{"admin":true}}`, s.handleAdminSetUserAdmin},
		{"capacity contact", "/api/v1/admin/capacity/contact", `{"contact_email":"a@b.example"}]`, s.handleAdminCapacityContact},
		{"collection create", "/api/v1/admin/collections", `{"name":"x"}}`, s.handleAdminCollectionCreate}, // a stray delimiter: the one tail More() let through (round 2 F4)
		{"signup allowlist add", "/api/v1/admin/capacity/signup-allowlist", `{"cidr":"10.0.0.0/8"}}}garbage`, s.handleAdminSignupAllowlistAdd},
		{"group add", "/api/v1/groups/1/repos", `{"url":"https://github.com/a/b"}}`, s.handleGroupAddRepo},
		{"forge-id adopt", "/api/v1/admin/forge-id-changes/1/adopt", `{"old_forge_id":"1","new_forge_id":"2","note":"x"}}`, s.handleAdminForgeIDAdopt},
	} {
		r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)).WithContext(admin)
		r.SetPathValue("userID", "7")  // read by set-admin
		r.SetPathValue("groupID", "1") // read by group add (round 2: without it the 400 was "invalid group id", not the decoder's)
		r.SetPathValue("repoID", "1")  // read by forge-id adopt
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		reached := false
		func() {
			defer func() {
				if recover() != nil {
					reached = true
				}
			}()
			tc.call(w, r)
		}()
		if reached || w.Code != http.StatusBadRequest {
			t.Errorf("%s: body %q = %d (service/store reached=%v) %q; want 400 before the service", tc.name, tc.body, w.Code, reached, strings.TrimSpace(w.Body.String()))
		}
	}
}

// readsRequestBody: the body itself (r.Body), or the form readers that
// consume it without naming it (whole-branch review of PR #238, API-F3:
// ParseForm, ParseMultipartForm, FormValue, PostFormValue, PostForm,
// MultipartReader read r.Body behind the request's own methods).
func readsRequestBody(body string) bool {
	for _, needle := range []string{"r.Body", "ParseForm(", "ParseMultipartForm(", "FormValue(", "PostFormValue(", "PostForm", "FormFile(", "MultipartReader("} {
		if strings.Contains(body, needle) {
			return true
		}
	}
	return false
}

// TestEveryRequestBodyIsReadThroughDecodeJSONBody (round 1 F1, lens L18 /
// SR-18): seven routes read their JSON body with a bare Decode of their
// own — no trailing-data check at all, a worse shape than the one the
// review found — while the stage-1f record called decodeJSONBody "the one
// bounded JSON decoder"; an eighth (round 2 F1) read it with io.ReadAll +
// json.Unmarshal, which a "json.NewDecoder over r.Body" predicate cannot
// see. So the predicate is the owning layer's: decodeJSONBody is the one
// function in the package that touches r.Body at all — a decoder, a
// ReadAll, a LimitReader or a helper handed the body all name it — and the
// request's own form readers, which consume the body without naming it,
// count as reads too (readsRequestBody). Every handler names its request r
// (the residuals: a handler whose request is named otherwise; a METHOD
// named decodeJSONBody, which FuncParts folds under the function's key). The floors guard the denominator; the
// definition is not a caller.
func TestEveryRequestBodyIsReadThroughDecodeJSONBody(t *testing.T) {
	readers, callers, functions := 0, 0, 0
	for path, file := range srctest.PackageFiles(t, "internal/api", 20) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file = srctest.StripGoComments(file)
		for name, body := range srctest.FuncParts(t, file) {
			if name != srctest.PackageLevel {
				functions++
			}
			if name != "decodeJSONBody" {
				callers += strings.Count(body, "decodeJSONBody(")
			}
			if !readsRequestBody(body) {
				continue
			}
			readers++
			if name != "decodeJSONBody" {
				t.Errorf("%s: %s reads r.Body itself; every request body goes through decodeJSONBody (its limit parameter takes the route's bound)", path, name)
			}
		}
	}
	srctest.MinCount(t, "functions scanned in internal/api", functions, 300)
	srctest.MinCount(t, "decodeJSONBody call sites", callers, 16) // 16 since 0.29.109: the two collection decodes share collectionFields (stage 3b)
	if readers != 1 {
		t.Errorf("%d functions touch r.Body; want exactly one (decodeJSONBody)", readers)
	}
}
