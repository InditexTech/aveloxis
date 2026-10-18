// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"testing"

	"github.com/aveloxis/aveloxis/internal/platform"
)

// Stage 1e of summary/59 (C4, N14): a GitLab group is named by its FULL
// path — a subgroup registration enumerates the subgroup, never its parent
// (eval-3's C4 blocker) — and GitHub by its account; a host neither forge
// owns keeps the fallback label and first-segment name it always had
// (DBS-11: observed, never rewritten).
func TestParseOrgURLMetaNamesAGitLabGroupByItsFullPath(t *testing.T) {
	for _, tc := range []struct {
		in             string
		name, platform string
	}{
		{"https://github.com/chaoss", "chaoss", "github"},
		{"github.com/chaoss/", "chaoss", "github"},
		{"https://github.com/chaoss/augur", "chaoss", "github"},
		{"https://gitlab.com/gnome", "gnome", "gitlab"},
		{"https://gitlab.com/gnome/sub", "gnome/sub", "gitlab"},
		{"https://gitlab.com/groups/gnome/sub/-/issues", "gnome/sub", "gitlab"},
		{"https://gitlab.freedesktop.org/mesa/sub", "mesa/sub", "gitlab"},
		// The fallback: not a forge host this store can classify.
		{"https://git.example.invalid/foo/bar", "foo", "github"},
		{"https://code.example.invalid/gitlab-ish/sub", "gitlab-ish", "github"}, // the host decides, as before
		{"not a url at all \x00", "", "github"},
	} {
		name, plat := parseOrgURLMeta(tc.in, "")
		if name != tc.name || plat != tc.platform {
			t.Errorf("parseOrgURLMeta(%q, \"\") = %q, %q; want %q, %q", tc.in, name, plat, tc.name, tc.platform)
		}
	}
	// With the configured instance's web base (the caller's
	// gitlab.base_url): a group there is GitLab, by its full path, whatever
	// the host name says; the rest classify as without it (round-2 review
	// of 0.29.100: code.example.org was labelled github and refused).
	const inst = "https://code.example.invalid"
	for _, tc := range []struct{ in, name, platform string }{
		{"https://code.example.invalid/mygroup", "mygroup", "gitlab"},
		{"https://code.example.invalid/mygroup/sub", "mygroup/sub", "gitlab"},
		{"https://www.code.example.invalid:443/groups/mygroup", "mygroup", "gitlab"},
		{"https://github.com/chaoss", "chaoss", "github"},
		{"https://gitlab.com/gnome/sub", "gnome/sub", "gitlab"},
		{"https://git.example.invalid/foo/bar", "foo", "github"},
	} {
		name, plat := parseOrgURLMeta(tc.in, inst)
		if name != tc.name || plat != tc.platform {
			t.Errorf("parseOrgURLMeta(%q, %q) = %q, %q; want %q, %q", tc.in, inst, name, plat, tc.name, tc.platform)
		}
	}
}

// TestOrgScanEligibleGitLabArm: a GitLab row is enumerable only on the
// configured instance, and never while the probe passes no base (no
// GitLab key: the pass would leave it unstamped and the probe would loop).
func TestOrgScanEligibleGitLabArm(t *testing.T) {
	const gl = "https://gitlab.example.org"
	for _, tc := range []struct {
		platform, orgURL, glBase string
		want                     bool
	}{
		{"gitlab", gl + "/gnome/sub", gl, true},
		{"gitlab", gl + "/groups/gnome", gl, true},
		{"gitlab", "https://gitlab.com/gnome", gl, false},          // another instance
		{"gitlab", gl + "/gnome/sub", "", false},                   // no base: no key
		{"gitlab", "https://user:tok@" + gl[8:] + "/g", gl, false}, // credentials
		{"gitlab", gl + "/", gl, false},                            // no group
		{"github", "https://github.com/chaoss", gl, true},
		{"github", gl + "/chaoss", gl, false},
		{"bitbucket", "https://bitbucket.org/x", gl, false},
	} {
		if got := OrgScanEligible(tc.platform, tc.orgURL, "", tc.glBase); got != tc.want {
			t.Errorf("OrgScanEligible(%q, %q, gl=%q) = %v, want %v", tc.platform, tc.orgURL, tc.glBase, got, tc.want)
		}
	}
	if platform.OnGitLabInstance("https://gitlab.example.org:443/g", gl) != true {
		t.Error("a default port names the same instance")
	}
}
