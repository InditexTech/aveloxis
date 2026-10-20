// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestV02997ChecklistNamesItsOperatorVisibleChanges pins what an operator
// must learn from `aveloxis deploy-checklist` for 0.29.97, which keeps
// GitHub repository traffic (aveloxis/aveloxis#234): the five tables the
// migrate adds, the new route, the access a key needs, the Info line a
// key without it logs, that any other traffic error now fails the job as
// a releases error does, the extra requests, and that nothing older than
// GitHub's 14 days is backfilled. Each table, the route and the log line
// must be the ones the code declares, so a rename cannot orphan the note.
func TestV02997ChecklistNamesItsOperatorVisibleChanges(t *testing.T) {
	steps, ok := deployChecklistFor("0.29.97")
	if !ok || len(steps) == 0 {
		t.Fatal("0.29.97 has no deploy checklist")
	}
	if steps[0].cmd != "aveloxis stop all" {
		t.Errorf("the ladder must start with stop all: %v", steps[0])
	}
	var migrate, start string
	for _, s := range steps {
		switch {
		case strings.HasPrefix(s.cmd, "aveloxis migrate"):
			migrate = s.cmd
		case s.cmd == "aveloxis start all":
			start = s.desc
		}
	}
	// No view definition changed (matviews_version_test.go would demand a
	// plain migrate), so the 8Knot view rebuild is skipped.
	if migrate != "aveloxis migrate --skip-views" {
		t.Errorf("the migrate step must be `aveloxis migrate --skip-views`, got %q", migrate)
	}
	if !strings.HasPrefix(start, "0.29.97: ") {
		t.Fatalf("the start step must open with this release's note, got %.80q", start)
	}
	// A fleet that skipped 0.29.96 still reads its note; the pins below
	// read this release's own text only.
	own, _, carried := strings.Cut(start, "0.29.96: ")
	if !carried {
		t.Fatal("the start note must carry 0.29.96's note for a fleet that skipped it")
	}

	schema := srctest.Read(t, "internal/db/schema.sql")
	for _, table := range []string{"repo_views", "repo_traffic_paths", "repo_traffic_referrers", "repo_traffic_windows", "repo_traffic_weeks"} {
		if !strings.Contains(own, table) {
			t.Errorf("the start note must name the new table %s", table)
		}
		if !strings.Contains(schema, "CREATE TABLE IF NOT EXISTS aveloxis_data."+table+" (") {
			t.Errorf("schema.sql does not create aveloxis_data.%s, which the start note names", table)
		}
	}

	const route = "GET /api/v1/repos/{repoID}/traffic"
	if !strings.Contains(own, route) {
		t.Errorf("the start note must name the new route %q", route)
	}
	if !strings.Contains(srctest.StripGoComments(srctest.Read(t, "internal/api/server.go")), `"`+route+`"`) {
		t.Errorf("server.go does not register %q, which the start note names", route)
	}

	const skipLine = "skipping traffic endpoints"
	if !strings.Contains(own, "'"+skipLine+"'") {
		t.Errorf("the start note must quote the Info line %q", skipLine)
	}
	body := srctest.FuncBody(t, srctest.StripGoComments(srctest.Read(t, "internal/collector/staged.go")), "func (sc *StagedCollector) collectTraffic(")
	// The note describes the 403 on the daily clones read, the first
	// traffic request: that skip must log this line.
	if !srctest.ContainsNormalized(body, `sc.logger.Info("`+skipLine+`", "owner", owner, "repo", repo, "reason", cloneErr)`) {
		t.Errorf("collectTraffic does not log %q at Info when the daily clones read is skipped", skipLine)
	}

	for _, want := range []string{
		"403",                  // what a key without access gets
		"push access",          // classic token
		"Administration",       // fine-grained token permission
		"success=false",        // a traffic error now fails the job
		"last_error",           // where its message lands
		"releases",             // as a releases error does
		"clone stats: ",        // the daily clones error, dropped before
		"traffic <endpoint>: ", // the other reads' errors
		"five more GitHub requests",
		"nothing is backfilled",
		// The scheduler keeps only the collection's first error
		// (buildOutcome stores result.Errors[0]), so a traffic error
		// reaches last_error only when nothing failed before it.
		"the first error's message in last_error",
	} {
		if !strings.Contains(own, want) {
			t.Errorf("the start note must say %q", want)
		}
	}
	// GitLab serves daily clone counts to a Reporter; this release only
	// does not collect them, so the note must not call them admin-only.
	// ("Administration" is GitHub's permission name and stays allowed.)
	for _, bad := range []string{"admin-only", "non-admin", "administrators only"} {
		if strings.Contains(strings.ToLower(own), bad) {
			t.Errorf("the 0.29.97 note must not say %q: GitLab's daily clone counts need only the Reporter role", bad)
		}
	}
}
