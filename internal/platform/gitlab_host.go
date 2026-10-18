// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import (
	"strings"

	"github.com/aveloxis/aveloxis/internal/model"
)

// PublicGitLabWebBase is gitlab.com's web base: the default of
// web.gitlab_base_url (the instance people sign in through) and the
// instance recorded on a GitLab account when none was configured. The
// one spelling (0.29.101; five sites each had their own).
const PublicGitLabWebBase = "https://gitlab.com"

// PublicGitLabAPIBase is gitlab.com's API base: the default of
// gitlab.base_url.
const PublicGitLabAPIBase = PublicGitLabWebBase + GitLabAPISuffix

// GitLabAPISuffix is the path every GitLab REST API base ends in. The
// GitLab client appends nothing to its base, so a gitlab.base_url without
// it would send every REST call to the wrong path (GraphQL alone would be
// right: its endpoint is derived by removing the suffix); GitLabWebBaseForAPI
// therefore names no instance without it (0.29.101, deferred from the
// 0.29.100 round 5).
const GitLabAPISuffix = "/api/v4"

// GitLabHints is the GitLab web-base hint list every URL classifier takes
// on 0.29 (stage 1 of summary/59): the configured instance's web base, or
// none. v0.30's `gitlab.instances` replaces the derivation; the callers
// keep the []string shape.
func GitLabHints(configuredAPIBase string) []string {
	base, _ := GitLabWebBaseForAPI(configuredAPIBase)
	return GitLabWebHints(base)
}

// GitLabWebHints is the hint list for one configured instance's web base
// (GitLabWebBaseForAPI's form; "" without an instance → nil): the one
// spelling every classifier takes — ParseRepoURLWithHints,
// ParseAnyRepoURLWithHints and ParseOrgRef (round-3 review of 0.29.100).
func GitLabWebHints(webBase string) []string {
	if webBase == "" {
		return nil
	}
	return []string{webBase}
}

// GitLabWebBaseForAPI derives the configured GitLab instance's web base
// URL (model.NormalizeInstanceWebBase's form) from gitlab.base_url — the API
// URL with its /api/v4 suffix removed — and true; ("", false) when no
// instance is configured or the URL does not name one: no scheme or host,
// a query, a fragment, a user name, or no GitLabAPISuffix (exact, lower
// case: GitLab's API path; a value without it is a web URL, and the REST
// client would append to it). The trim is the client's — ONE trailing
// slash (NewHTTPClient's) — so a value the gate admits is the value the
// wire sees: "…/api/v4//" is refused, not sent as "…/api/v4//projects"
// (round 2 of 0.29.101). On this release it is the ONE hint
// ParseRepoURLWithHints and ParseOrgRef take (stage 1 of summary/59), and
// the rule config.Load refuses gitlab.base_url by; v0.30 replaces it with
// the instances registry.
func GitLabWebBaseForAPI(configuredAPIBase string) (string, bool) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(configuredAPIBase), "/")
	webPart, hasSuffix := strings.CutSuffix(trimmed, GitLabAPISuffix)
	if trimmed == "" || !hasSuffix {
		return "", false
	}
	base, err := model.NormalizeInstanceWebBase(webPart)
	if err != nil {
		return "", false
	}
	return base, true
}

// GitLabAPIBaseForURL is the API base GitLab keys may be sent to for a URL:
// the configured instance's (gitlab.base_url) when the URL is on that
// instance by the ONE host rule every GitLab gate applies (OnGitLabInstance).
// A URL elsewhere, no configuration or an unusable base: ok false, and no
// keyed client is built (v0.29.11; round-2 review of 0.29.100: the scan's
// gate admitted www.gitlab.com while this compared the host byte for byte,
// and the admitted row looped unstamped).
func GitLabAPIBaseForURL(configuredAPIBase, rawURL string) (string, bool) {
	webBase, ok := GitLabWebBaseForAPI(configuredAPIBase)
	if !ok || !OnGitLabInstance(rawURL, webBase) {
		return "", false
	}
	// The gate's trim (one slash), so the scan's base is the client's base.
	return strings.TrimSuffix(strings.TrimSpace(configuredAPIBase), "/"), true
}

// OnGitLabInstance reports whether rawURL is on the GitLab instance whose
// web base is webBase (GitLabWebBaseForAPI's form): the one host rule
// (SR-17) behind the scan gate (db.OrgScanEligible), the never-scanned
// probe and the keyed client (GitLabAPIBaseForURL). The match is
// model.MatchInstanceWebBase's: host case, a www. prefix, a trailing dot
// and the scheme's default port name one host, and the base's path prefix
// must match. A URL without a path names no group or project: false.
func OnGitLabInstance(rawURL, webBase string) bool {
	ref, err := ParseOrgRef(rawURL, []string{webBase})
	return err == nil && ref.WebBase == webBase
}
