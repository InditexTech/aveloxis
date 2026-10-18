// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package gitlab

import (
	"io"
	"log/slog"
	"testing"
)

// TestParseRepoURLTakesTheInstanceWebBase (0.29.101): the client's
// ParseRepoURL classifies with the web base derived from its API base by
// the one rule (platform.GitLabWebBaseForAPI), not a host-only hint —
// an instance under a path prefix split its prefix off as the owner
// (noted in the 0.29.100 round 5; no production caller then).
func TestParseRepoURLTakesTheInstanceWebBase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name, base, url, owner, repo string
		wantErr                      bool
	}{
		{"public", "https://gitlab.com/api/v4", "https://gitlab.com/gnome/gtk", "gnome", "gtk", false},
		{"self-hosted", "https://git.example.org/api/v4", "https://git.example.org/mesa/mesa", "mesa", "mesa", false},
		{"under a path prefix", "https://example.org/gitlab/api/v4", "https://example.org/gitlab/g/p", "g", "p", false},
		{"another host is not this instance", "https://git.example.org/api/v4", "https://github.com/g/p", "", "", true},
		{"a base without the API suffix names no instance", "https://git.example.org", "https://git.example.org/g/p", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, err := New(tc.base, nil, logger).ParseRepoURL(tc.url)
			if (err != nil) != tc.wantErr || owner != tc.owner || repo != tc.repo {
				t.Fatalf("New(%q).ParseRepoURL(%q) = (%q, %q, %v), want (%q, %q, err=%v)", tc.base, tc.url, owner, repo, err, tc.owner, tc.repo, tc.wantErr)
			}
		})
	}
}
