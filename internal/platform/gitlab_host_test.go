// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import "testing"

// TestGitLabAPIBaseForURL — v0.29.11; round-2 review of 0.29.100. GitLab
// API keys belong to the ONE instance configured as gitlab.base_url. A
// group or repository discovered on another host must not receive them:
// the helper returns the configured API base only when the URL is on that
// instance, by the same host rule the scan gate applies (a www. prefix, a
// trailing dot and the scheme's default port name the same host; the
// base's path prefix must match), so a caller can never build a keyed
// GitLab client for a foreign host and never refuse a URL the gate admits.
func TestGitLabAPIBaseForURL(t *testing.T) {
	for _, tc := range []struct {
		name, configured, url, wantBase string
		wantOK                          bool
	}{
		{"gitlab.com default", "https://gitlab.com/api/v4", "https://gitlab.com/gnome", "https://gitlab.com/api/v4", true},
		{"host is case-insensitive", "https://gitlab.com/api/v4", "https://GitLab.COM/gnome", "https://gitlab.com/api/v4", true},
		{"www. names the same host", "https://gitlab.com/api/v4", "https://www.gitlab.com/gnome", "https://gitlab.com/api/v4", true},
		{"the default port names the same host", "https://gitlab.com/api/v4", "https://gitlab.com:443/gnome", "https://gitlab.com/api/v4", true},
		{"a trailing dot names the same host", "https://gitlab.com/api/v4", "https://gitlab.com./gnome", "https://gitlab.com/api/v4", true},
		{"self-hosted instance", "https://git.example.org/api/v4", "https://git.example.org/mesa/sub", "https://git.example.org/api/v4", true},
		{"trailing slash trimmed", "https://gitlab.com/api/v4/", "https://gitlab.com/gnome", "https://gitlab.com/api/v4", true},
		{"port is part of the authority", "http://127.0.0.1:8080/api/v4", "http://127.0.0.1:8080/g/p", "http://127.0.0.1:8080/api/v4", true},
		{"instance under a path prefix", "https://example.org/gitlab/api/v4", "https://example.org/gitlab/g", "https://example.org/gitlab/api/v4", true},
		{"another path on that host is not the instance", "https://example.org/gitlab/api/v4", "https://example.org/other/g", "", false},
		{"different port is a different instance", "http://127.0.0.1:8080/api/v4", "http://127.0.0.1:9090/g/p", "", false},
		{"foreign host", "https://gitlab.com/api/v4", "https://git.example.org/g", "", false},
		{"lookalike subdomain", "https://gitlab.com/api/v4", "https://gitlab.com.evil.example/g", "", false},
		{"no configured instance", "", "https://gitlab.com/gnome", "", false},
		{"unparseable configured base", "://nope", "https://gitlab.com/gnome", "", false},
		{"configured base without a host", "/api/v4", "https://gitlab.com/gnome", "", false},
		// 0.29.101: the GitLab REST client appends nothing to the base, so a
		// base without the /api/v4 suffix sends every REST call to the wrong
		// path (GraphQL alone would be right): no instance, no keyed client.
		{"configured base without the API suffix", "https://gitlab.example.org", "https://gitlab.example.org/gnome", "", false},
		{"configured base with a path prefix but no API suffix", "https://example.org/gitlab", "https://example.org/gitlab/g", "", false},
		{"the API suffix is lower-case", "https://gitlab.example.org/API/V4", "https://gitlab.example.org/gnome", "", false},
		{"the API suffix is a path segment", "https://gitlab.example.org/xapi/v4", "https://gitlab.example.org/gnome", "", false},
		{"URL without a host", "https://gitlab.com/api/v4", "/gnome", "", false},
		{"URL without a path names nothing", "https://gitlab.com/api/v4", "https://gitlab.com/", "", false},
		{"credentials in the URL are refused", "https://gitlab.com/api/v4", "https://user:pw@gitlab.com/gnome", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, ok := GitLabAPIBaseForURL(tc.configured, tc.url)
			if ok != tc.wantOK || base != tc.wantBase {
				t.Errorf("GitLabAPIBaseForURL(%q, %q) = (%q, %v), want (%q, %v)", tc.configured, tc.url, base, ok, tc.wantBase, tc.wantOK)
			}
			// The gate's own answer is the same answer (SR-17: one rule).
			if webBase, cfgOK := GitLabWebBaseForAPI(tc.configured); cfgOK {
				if on := OnGitLabInstance(tc.url, webBase); on != tc.wantOK {
					t.Errorf("OnGitLabInstance(%q, %q) = %v; the keyed client and the gate must agree", tc.url, webBase, on)
				}
			}
		})
	}
}
