// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestForgeBasesReachEveryAdminService (round-2 review of 0.29.100; SR-10):
// gitlab.base_url reaches every admin service a process builds — the web
// server (WithGitLabAPIBase), the API (Options.GitLabAPIBase) and each CLI
// loader's admin.New — so an organisation on the configured instance is
// classified GitLab everywhere (the behavior half is
// TestInstanceWebBaseReachesTheStore in internal/admin and
// TestAddOrgToGroupRegistersAGroupOnTheConfiguredInstance in internal/db).
// The needle is negative — an admin.Options literal WITHOUT GitLabAPIBase
// anywhere in the three packages — with the literals examined as the
// denominator; the two process builders are pinned positively on their
// own bodies.
func TestForgeBasesReachEveryAdminService(t *testing.T) {
	examined := 0
	for _, dir := range []string{"cmd/aveloxis", "internal/web", "internal/api"} {
		src := srctest.StripGoComments(srctest.PackageSource(t, dir, 1))
		for rest := src; ; {
			i := strings.Index(rest, "admin.Options{")
			if i < 0 {
				break
			}
			lit := rest[i:]
			end := strings.Index(lit, "}")
			if end < 0 {
				t.Fatalf("%s: an admin.Options literal never closes", dir)
			}
			examined++
			if !strings.Contains(lit[:end], "GitLabAPIBase") {
				t.Errorf("%s: an admin.Options literal without GitLabAPIBase — that service classifies a group on the configured GitLab instance by its host name:\n%s", dir, lit[:end])
			}
			rest = lit[end:]
		}
	}
	if examined < 6 {
		t.Fatalf("examined %d admin.Options literals; want at least the four loaders, the web adapter and the API", examined)
	}
	if web := srctest.StripGoComments(srctest.PackageFunc(t, "cmd/aveloxis", "func newWebServer(")); !strings.Contains(web, ".WithGitLabAPIBase(cfg.GitLab.BaseURL)") {
		t.Errorf("newWebServer does not hand the web server gitlab.base_url (WithGitLabAPIBase):\n%s", web)
	}
	// Fields, not the raw text: gofmt aligns the literal's values.
	if apiOpts := strings.Join(strings.Fields(srctest.StripGoComments(srctest.PackageFunc(t, "cmd/aveloxis", "func apiOptions("))), " "); !strings.Contains(apiOpts, "GitLabAPIBase: cfg.GitLab.BaseURL,") {
		t.Errorf("apiOptions does not carry gitlab.base_url:\n%s", apiOpts)
	}
}

// TestRepoURLsTakeTheInstanceHint (round-3 review of 0.29.100): every
// command that classifies a repository URL does so with the configured
// GitLab instance's hint, so a URL under gitlab.base_url's web URL is a
// GitLab repository whatever its host name says (commands.md promises it;
// the org scan already records that instance's projects as GitLab). A bare
// ParseRepoURL in these bodies is the defect.
func TestRepoURLsTakeTheInstanceHint(t *testing.T) {
	// The negative needle over the whole package (round-4 review: a fifth
	// command, import-foundations, was invisible to the four-name list):
	// no command classifies a repository URL hint-less.
	pkg := srctest.StripGoComments(srctest.PackageSource(t, "cmd/aveloxis", 10))
	for _, bare := range []string{"platform.ParseRepoURL(", "platform.ParseAnyRepoURL("} {
		if strings.Contains(pkg, bare) {
			t.Errorf("cmd/aveloxis calls %s somewhere — every command classifies with platform.ParseRepoURLWithHints(x, platform.GitLabHints(cfg.GitLab.BaseURL))", bare)
		}
	}
	for _, sig := range []string{"func runAddRepo(", "func runImportFromAugur(", "func runPrioritize(", "func runRecollect(", "func runImportFoundations("} {
		body := srctest.StripGoComments(srctest.PackageFunc(t, "cmd/aveloxis", sig))
		if strings.Contains(body, "platform.ParseRepoURL(") || strings.Contains(body, "platform.ParseAnyRepoURL(") {
			t.Errorf("%s classifies a repository URL without the instance hint", sig)
		}
		if !strings.Contains(body, "platform.GitLabHints(cfg.GitLab.BaseURL)") {
			t.Errorf("%s does not pass the configured GitLab instance's hint", sig)
		}
	}
}
