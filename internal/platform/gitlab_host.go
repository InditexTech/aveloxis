// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import (
	"strings"

	"github.com/aveloxis/aveloxis/internal/model"
)

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
// instance is configured or the URL does not name one. On this release it
// is the ONE hint ParseRepoURLWithHints and ParseOrgRef take (stage 1 of
// summary/59); v0.30 replaces it with the instances registry.
func GitLabWebBaseForAPI(configuredAPIBase string) (string, bool) {
	trimmed := strings.TrimRight(strings.TrimSpace(configuredAPIBase), "/")
	if trimmed == "" {
		return "", false
	}
	trimmed = strings.TrimSuffix(trimmed, "/api/v4")
	base, err := model.NormalizeInstanceWebBase(trimmed)
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
	return strings.TrimRight(strings.TrimSpace(configuredAPIBase), "/"), true
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
