// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scheduler

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// The legacy repo_groups refreshes bridge into modern user_groups
// (2026-05-07 diagnostic: repos discovered by the periodic refresh ticker
// landed in the catalog but never reached user_repos — the fork-clustering
// drift the operator diagnosed): each pass maps the legacy
// repo_groups.rg_website to any matching user_org_requests.org_url rows
// (GetUserGroupIDsForOrgURL) and hands them to the one ingest, which links
// EVERY repository it sees (existing ones too — self-healing for any drift
// accumulated before the bridge; forks included, the listing asks for
// type=all) through AddOrgRepoToGroupByID. Stage 1 of summary/59: the
// link moved from the three passes into ingestDiscovered.
func TestLegacyRefreshesBridgeToUserReposThroughTheOneIngest(t *testing.T) {
	for _, fn := range []string{"func (s *Scheduler) refreshGitHubOrg(", "func (s *Scheduler) refreshGitLabGroup("} {
		body := srctest.StripGoComments(srctest.PackageFunc(t, "internal/scheduler", fn))
		if !strings.Contains(body, "GetUserGroupIDsForOrgURL") {
			t.Errorf("%s must call GetUserGroupIDsForOrgURL to find the user_groups tracking this org", fn)
		}
		if !strings.Contains(body, "userGroupIDs: userGroupIDs") || !strings.Contains(body, "s.ingestDiscovered(") {
			t.Errorf("%s must hand the user groups to ingestDiscovered as its targets", fn)
		}
	}
	ingest := srctest.StripGoComments(srctest.PackageFunc(t, "internal/scheduler", "func (s *Scheduler) ingestDiscovered("))
	find := strings.Index(ingest, "FindRepoByURL(")
	link := strings.Index(ingest, "AddOrgRepoToGroupByID(")
	if find < 0 || link < find {
		t.Fatal("ingestDiscovered must look the repository up, then link it (lookup → create if missing → link)")
	}
	// The link sits outside the existing/new branches: an existing
	// repository is linked too (the legacy `if existing > 0 { continue }`
	// skipped it).
	between := ingest[find:link]
	if strings.Contains(between, "continue\n") {
		t.Error("ingestDiscovered skips the link for a repository already in the catalog; existing repositories must be linked too, or pre-bridge drift never heals")
	}
}
