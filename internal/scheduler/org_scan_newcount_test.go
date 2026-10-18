// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scheduler

import (
	"github.com/aveloxis/aveloxis/internal/srctest"
	"os"
	"strings"
	"testing"
)

// TestOrgScanCountsOnlyGenuinelyNewLinks pins the 2026-08-18 miscount fix.
//
// refreshUserOrgs' per-group loop used to increment newCounts[gid] whenever
// AddRepoToGroupByID returned a nil error — but that INSERT is ON CONFLICT
// DO NOTHING, which returns nil on the no-op path too. Result in the
// Aug 7–16 production run: "user org scan found new repos" fired 63,960
// times claiming 9.3 MILLION new repos, with the SAME org reporting the
// SAME count every 4-hour pass (kubernetes new_repos=79 for 8.8 days).
//
// The counter must gate on the inserted bool that AddRepoToGroupByID now
// returns from the command tag's RowsAffected. The LINKING behavior itself
// is unchanged — the call still runs for every repo × group every pass so
// new repos in a tracked org keep landing in every registered group.
func TestOrgScanCountsOnlyGenuinelyNewLinks(t *testing.T) {
	data, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("read scheduler.go: %v", err)
	}
	src := string(data)

	// Isolate refreshUserOrgs' body (up to the next top-level func).
	start := strings.Index(src, "func (s *Scheduler) refreshUserOrgs(")
	if start < 0 {
		t.Fatal("refreshUserOrgs not found in scheduler.go")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end >= 0 {
		rest = rest[:end+1]
	}
	// Stage 1: the link runs in the one ingest, which reports the groups
	// whose link row it INSERTED (ingestOutcome.linked); the pass counts
	// those and nothing else.
	ingest := srctest.StripGoComments(srctest.PackageFunc(t, "internal/scheduler", "func (s *Scheduler) ingestDiscovered("))
	if strings.Contains(ingest, "AddOrgRepoToGroupByID(ctx, gid, repoID); err == nil {") {
		t.Error("ingestDiscovered marks a group linked on a bare err == nil from " +
			"AddOrgRepoToGroupByID — ON CONFLICT DO NOTHING returns nil on no-op " +
			"links, so this counts every repo in the org as \"new\" on every " +
			"pass (the 9.3M bogus new-repos flood, 2026-08-18 diagnosis)")
	}
	if !strings.Contains(ingest, "if inserted {\n\t\t\tout.linked[gid] = true") {
		t.Error("ingestDiscovered must mark a group linked only on the inserted bool AddOrgRepoToGroupByID returns")
	}
	if !strings.Contains(rest, "for gid := range out.linked {\n\t\t\t\t\tnewCounts[gid]++") {
		t.Error("refreshUserOrgs must count newCounts[gid]++ over ingestOutcome.linked only")
	}
	// v0.27.92 (Copilot round on PR #178): link failures must be logged —
	// a silently failed link means a repo never appears in the user's
	// group with zero operator signal; one message, one site, one grep.
	if !strings.Contains(ingest, "failed to link discovered repo into user_repos") {
		t.Error("ingestDiscovered must WARN when AddOrgRepoToGroupByID fails (\"failed to link discovered repo into user_repos\")")
	}
}
