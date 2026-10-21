// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/platform"
	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestGitLabPublicBasesAreSpelledOnce (0.29.101, WEB-14 of summary/59): the
// public GitLab web base and API base are platform constants; the config
// default, the OAuth endpoints and the stored instance key all read them.
// Five sites spelled "https://gitlab.com" before this release, each its
// own default to drift (CLAUDE.md: centralize a default at one layer).
func TestGitLabPublicBasesAreSpelledOnce(t *testing.T) {
	// The API suffix too (round 1 of 0.29.101: the GraphQL derivation and the
	// OAuth user read spelled it). internal/model cannot read the constant
	// (platform imports model; the reason is at instance_web_base.go's site).
	for _, dir := range []string{"internal/config", "internal/web", "internal/db", "internal/api", "internal/scheduler", "internal/platform/gitlab", "internal/admin", "cmd/aveloxis"} {
		src := srctest.StripGoComments(srctest.PackageSource(t, dir, 1))
		for _, lit := range []string{`"` + platform.PublicGitLabWebBase + `"`, `"` + platform.PublicGitLabAPIBase + `"`, `"` + platform.GitLabAPISuffix} {
			if strings.Contains(src, lit) {
				t.Errorf("%s spells %s itself; use the platform constant", dir, lit)
			}
		}
	}
}
