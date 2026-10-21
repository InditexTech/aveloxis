// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestOnlyTheTwoParsersReadAPathValue (0.29.103; CodeQL
// go/incorrect-integer-conversion, PR #238 alert 203 on
// handleSetUserAdmin's int(userID64)): a handler never parses a path
// value itself. pathID reads the int64 ids and pathUserID the int4 user id
// (through admin.ParseUserID, the one rule for a users.user_id). Round 1's
// regex on the spelling `int(x64)` was a spelling pin (F3); round 2 showed
// this reachability pin alone lets the alert's shape back in, since pathID
// hands an int64 to seven functions (ten bindings; F1): so the user-id
// route's handler is pinned to pathUserID, and no function converts a name
// it bound from pathID to a narrower integer type (one word-bounded regex
// per name, round 3: a narrowing through a second variable, an assignment
// rather than a binding, or a helper is flow, which CodeQL's
// go/incorrect-integer-conversion in CI and the local security-extended
// run decide — this pin is the cheap tripwire, not the oracle). The
// behavioural half is TestBadAdminIdsAre400BeforeTheStore's int4 case.
func TestOnlyTheTwoParsersReadAPathValue(t *testing.T) {
	readers := map[string]bool{"pathID": true, "pathUserID": true}
	bound := regexp.MustCompile(`(\w+)\s*,\s*\w+\s*:=\s*pathID\(`)
	examined, found, narrowings := 0, 0, 0
	bodies := map[string]string{} // every function in the package, by name
	for path, src := range srctest.PackageFiles(t, "internal/web", 10) {
		src = srctest.StripGoComments(src)
		for name, body := range srctest.FuncParts(t, src) {
			examined++
			bodies[name] = body
			if strings.Contains(body, ".PathValue(") {
				found++
				if !readers[name] {
					t.Errorf("%s: %s reads a path value itself; route ids go through pathID or pathUserID", path, name)
				}
				if name != "pathID" && (strings.Contains(body, "strconv.ParseInt(") || strings.Contains(body, "strconv.ParseUint(")) {
					t.Errorf("%s: %s parses a path value at 64 bits; the int-typed id is read by admin.ParseUserID", path, name)
				}
			}
			for _, m := range bound.FindAllStringSubmatch(body, -1) {
				narrowings++
				narrowed := regexp.MustCompile(`\bu?int(8|16|32)?\(\s*` + regexp.QuoteMeta(m[1]) + `\s*\)`)
				if hit := narrowed.FindString(body); hit != "" {
					t.Errorf("%s: %s narrows %s (%s), the int64 pathID returned; an int-typed id is read by pathUserID", path, name, m[1], hit)
				}
			}
		}
	}
	srctest.MinCount(t, "functions examined", examined, 60)
	srctest.MinCount(t, "pathID bindings examined", narrowings, 8)
	if found != len(readers) {
		t.Errorf("%d functions read a path value, want exactly the %d parsers (one moved or was renamed; retarget this pin)", found, len(readers))
	}
	// The wiring: the handler Handler registers for the user-id route reads
	// through pathUserID and never through pathID.
	routes := srctest.StripGoComments(srctest.PackageFunc(t, "internal/web", "func (s *Server) Handler("))
	reg := regexp.MustCompile(`"POST /admin/users/\{id\}/admin",\s*s\.requireAdmin\(s\.(\w+)\)`).FindStringSubmatch(routes)
	if reg == nil {
		t.Fatal("Handler no longer registers POST /admin/users/{id}/admin behind requireAdmin as a method value; retarget this pin")
	}
	handler, ok := bodies[reg[1]]
	if !ok || !strings.Contains(handler, "pathUserID(r, \"id\")") || strings.Contains(handler, "pathID(") {
		t.Errorf("%s (POST /admin/users/{id}/admin) must read the user id through pathUserID and never pathID", reg[1])
	}
}
