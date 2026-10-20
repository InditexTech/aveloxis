// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"regexp"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// v0.29.91 (whole-PR review, L18; three reviewers): the UTC-day boundary,
// its stored date, the next day and the windows' lengths were re-spelled
// at five sites in the store and the api. They have one owner now —
// capacity.UTCDay.Start / End, capacity.UTCDate, Window.Seconds — and the
// shared count of one subject on one day is one statement
// (requestCountOnDaySQL). This pin examines every non-test file of both
// packages: a second spelling of the day boundary anywhere fails, and the
// capacity files may not spell the date or the next day themselves.
func TestCapacityDayRulesHaveOneOwner(t *testing.T) {
	truncDay := regexp.MustCompile(`Truncate\(\s*24\s*\*\s*time\.Hour\s*\)`)
	dayLength := regexp.MustCompile(`\(\s*24\s*\*\s*time\.Hour\s*\)\.Seconds\(\)`)
	// (The one reviewed exception, the contributions window's two-year
	// floor, moved onto db.UTCDay with the date reader in stage 1b.)
	excused := map[string]int{}
	capacityFiles := map[string]bool{
		"internal/db/capacity_store.go": true, "internal/db/capacity_admin_store.go": true, "internal/db/signup_escrow.go": true,
		"internal/api/capacity_limits.go": true, "internal/api/capacity_admin.go": true, "internal/api/ratelimit.go": true,
	}
	examined := 0
	sumSites := 0
	for _, dir := range []string{"internal/db", "internal/api"} {
		for name, src := range srctest.PackageFiles(t, dir, 25) {
			examined++
			code := srctest.StripGoComments(src)
			if n := len(truncDay.FindAllString(code, -1)); n != excused[name] {
				t.Errorf("%s spells the UTC-day boundary as Truncate(24*time.Hour) %d time(s) (reviewed %d): use capacity.UTCDay.Start", name, n, excused[name])
			}
			if dayLength.MatchString(code) {
				t.Errorf("%s spells a window's length: use capacity.Window.Seconds", name)
			}
			if capacityFiles[name] {
				for _, banned := range []string{".UTC().Format(time.DateOnly)", "AddDate(0, 0, 1)", `Format("2006-01-02")`} {
					if strings.Contains(code, banned) {
						t.Errorf("%s spells %q: use capacity.UTCDate / capacity.UTCDay.End", name, banned)
					}
				}
			}
			sumSites += strings.Count(code, "COALESCE(SUM(requests)")
		}
	}
	srctest.MinCount(t, "non-test files examined in internal/db and internal/api", examined, 150)
	if sumSites != 1 {
		t.Errorf("the shared day count is spelled %d times; want 1 (requestCountOnDaySQL)", sumSites)
	}
	if strings.Contains(srctest.StripGoComments(srctest.Read(t, "internal/api/capacity_limits.go")), "func windowSeconds(") {
		t.Error("windowSeconds re-spells Window.Seconds")
	}
}

// v0.29.91: the organization door decides who approved the registration
// covering a repository with ReconcileOrgRepoLinks's own prefix rule —
// the reconcile's SQL carries orgCoversRepoSQL's text exactly, so the two
// cannot drift apart (SR-17).
func TestOrgCoverageIsTheReconcileRule(t *testing.T) {
	src := srctest.Read(t, "internal/db/org_link_reconcile.go")
	body := srctest.FuncBody(t, src, "func (s *PostgresStore) ReconcileOrgRepoLinks(")
	if !strings.Contains(body, orgCoversRepoSQL) {
		t.Errorf("ReconcileOrgRepoLinks no longer carries %q: the org door and the reconcile disagree on what an organization covers", orgCoversRepoSQL)
	}
	if !strings.Contains(orgLinksCountedSQL, orgCoversRepoSQL) || !strings.Contains(orgLinksCountedSQL, "q.decided_by > 0") {
		t.Error("orgLinksCountedSQL must use orgCoversRepoSQL and count unless a real administrator (decided_by > 0) decided")
	}
}
