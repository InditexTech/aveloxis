// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import "testing"

// TestGitLabWebBaseForAPIRequiresTheAPISuffix (0.29.101, deferred from the
// 0.29.100 round-5 option): gitlab.base_url is the URL the GitLab REST
// client appends paths to, so only a value ending in /api/v4 names an
// instance. A bare web base used to derive a web base from itself and load
// cleanly while every REST call went to the wrong path. The one rule lives
// here: the load-time refusal, the classifier hint and the keyed-client
// gate all read this function.
func TestGitLabWebBaseForAPIRequiresTheAPISuffix(t *testing.T) {
	for _, tc := range []struct {
		name, configured, wantBase string
		wantOK                     bool
	}{
		{"public default", "https://gitlab.com/api/v4", "https://gitlab.com", true},
		{"trailing slash", "https://gitlab.com/api/v4/", "https://gitlab.com", true},
		{"self-hosted under a prefix", "https://example.org/gitlab/api/v4", "https://example.org/gitlab", true},
		{"surrounding space", "  https://gitlab.example.org/api/v4  ", "https://gitlab.example.org", true},
		{"empty: no instance", "", "", false},
		// The client trims ONE trailing slash (NewHTTPClient); a second one
		// would reach the wire as …/api/v4//projects (round 2 of 0.29.101).
		{"two trailing slashes", "https://gitlab.example.org/api/v4//", "", false},
		{"bare web base", "https://gitlab.example.org", "", false},
		{"bare web base with a slash", "https://gitlab.example.org/", "", false},
		{"prefix without the suffix", "https://example.org/gitlab", "", false},
		{"upper-case suffix", "https://gitlab.example.org/API/V4", "", false},
		{"suffix inside a longer segment", "https://gitlab.example.org/xapi/v4", "", false},
		{"another API version", "https://gitlab.example.org/api/v3", "", false},
		{"no scheme", "gitlab.example.org/api/v4", "", false},
		{"a query", "https://gitlab.example.org/api/v4?x=1", "", false},
		{"credentials", "https://u:p@gitlab.example.org/api/v4", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, ok := GitLabWebBaseForAPI(tc.configured)
			if ok != tc.wantOK || base != tc.wantBase {
				t.Errorf("GitLabWebBaseForAPI(%q) = (%q, %v), want (%q, %v)", tc.configured, base, ok, tc.wantBase, tc.wantOK)
			}
			// The hint list is the same answer: one spelling or none.
			hints := GitLabHints(tc.configured)
			if tc.wantOK && (len(hints) != 1 || hints[0] != tc.wantBase) {
				t.Errorf("GitLabHints(%q) = %q, want [%q]", tc.configured, hints, tc.wantBase)
			}
			if !tc.wantOK && hints != nil {
				t.Errorf("GitLabHints(%q) = %q, want nil for an unusable base", tc.configured, hints)
			}
		})
	}
}
