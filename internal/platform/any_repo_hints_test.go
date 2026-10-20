// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import (
	"testing"

	"github.com/aveloxis/aveloxis/internal/model"
)

// TestParseAnyRepoURLWithHints (round-3 review of 0.29.100): a repository
// URL under the configured GitLab instance's web base is a GitLab
// repository whatever its host name says — the rule the org scan applies
// — while without the hint such a host is generic git, as before; a hint
// changes nothing for github.com, gitlab.com or a host that says gitlab.
func TestParseAnyRepoURLWithHints(t *testing.T) {
	hints := GitLabWebHints("https://code.example.invalid")
	if hints == nil || GitLabWebHints("") != nil {
		t.Fatalf("GitLabWebHints: %v / %v", hints, GitLabWebHints(""))
	}
	for _, tc := range []struct {
		name, url    string
		hints        []string
		wantPlatform model.Platform
		wantOwner    string
		wantRepo     string
	}{
		{"instance with the hint", "https://code.example.invalid/g/p", hints, model.PlatformGitLab, "g", "p"},
		{"instance, www. and the default port", "https://www.code.example.invalid:443/g/p.git", hints, model.PlatformGitLab, "g", "p"},
		{"instance without the hint", "https://code.example.invalid/g/p", nil, model.PlatformGenericGit, "g", "p"},
		{"another host with the hint", "https://other.example.invalid/g/p", hints, model.PlatformGenericGit, "g", "p"},
		{"github.com with the hint", "https://github.com/o/r", hints, model.PlatformGitHub, "o", "r"},
		{"gitlab.com with the hint", "https://gitlab.com/o/r", hints, model.PlatformGitLab, "o", "r"},
		{"a host that says gitlab, no hint", "https://gitlab.freedesktop.org/mesa/mesa", nil, model.PlatformGitLab, "mesa", "mesa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ru, err := ParseAnyRepoURLWithHints(tc.url, tc.hints)
			if err != nil {
				t.Fatalf("ParseAnyRepoURLWithHints(%q) = %v", tc.url, err)
			}
			if ru.Platform != tc.wantPlatform || ru.Owner != tc.wantOwner || ru.Repo != tc.wantRepo {
				t.Errorf("ParseAnyRepoURLWithHints(%q, %v) = platform %d %q/%q; want %d %q/%q", tc.url, tc.hints, ru.Platform, ru.Owner, ru.Repo, tc.wantPlatform, tc.wantOwner, tc.wantRepo)
			}
		})
	}
	if ru, err := ParseAnyRepoURL("https://code.example.invalid/g/p"); err != nil || ru.Platform != model.PlatformGenericGit {
		t.Errorf("ParseAnyRepoURL is the hint-less form: %+v, %v", ru, err)
	}
}
