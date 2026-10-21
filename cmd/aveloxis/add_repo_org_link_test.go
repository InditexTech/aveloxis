// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// Stage 1 of summary/59 (N13, C2): `aveloxis add-repo <orgURL>` registers
// the organization and lists nothing itself — serve's org refresh lists it
// through the one lister and the one ingest, which links every repository
// (forks included) into every user group tracking the org (the bridge the
// operator diagnosed missing on 2026-05-07 now lives in one place,
// scheduler.ingestDiscovered). Source-contract test: the CLI's org arm
// registers (UpsertRepoGroup) and builds no forge client; cmd has no
// listing loop of its own.
func TestAddRepoRegistersAnOrganizationAndListsNothing(t *testing.T) {
	body := srctest.StripGoComments(srctest.PackageFunc(t, "cmd/aveloxis", "func runAddRepo("))
	if !strings.Contains(body, "store.UpsertRepoGroup(") {
		t.Error("runAddRepo's organization arm must register the org group (UpsertRepoGroup) — serve's org refresh lists it")
	}
	for _, banned := range []string{"platform.NewHTTPClient(", "loadKeys(", "ListOwnerRepos(", "ListGroupProjects(", "AddOrgRepoToGroupByID("} {
		if strings.Contains(body, banned) {
			t.Errorf("runAddRepo calls %s: the CLI lists and links nothing itself (N13)", banned)
		}
	}
	src := srctest.StripGoComments(srctest.PackageSource(t, "cmd/aveloxis", 20))
	for _, banned := range []string{`"/orgs/`, "/projects?per_page", "listGitHubOrgRepos", "listGitLabGroupRepos"} {
		if strings.Contains(src, banned) {
			t.Errorf("cmd/aveloxis spells %q: the forge listers live in internal/platform", banned)
		}
	}
}
