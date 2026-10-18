// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import (
	"errors"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/model"
)

func TestParseOrgRef(t *testing.T) {
	hints := []string{"https://code.example.org"}
	for _, tc := range []struct {
		in   string
		want OrgRef
	}{
		{"https://github.com/chaoss", OrgRef{model.PlatformGitHub, "github.com", "https://github.com", "chaoss"}},
		{"github.com/chaoss", OrgRef{model.PlatformGitHub, "github.com", "https://github.com", "chaoss"}},
		{"  https://github.com/chaoss/ ", OrgRef{model.PlatformGitHub, "github.com", "https://github.com", "chaoss"}},
		// GitHub: one segment, whatever follows (a repository URL names its owner).
		{"https://github.com/chaoss/augur", OrgRef{model.PlatformGitHub, "github.com", "https://github.com", "chaoss"}},
		{"https://GitHub.com:443/Chaoss", OrgRef{model.PlatformGitHub, "github.com:443", "https://github.com", "Chaoss"}},
		// GitLab: the full group path (N14).
		{"https://gitlab.com/gnome", OrgRef{model.PlatformGitLab, "gitlab.com", "https://gitlab.com", "gnome"}},
		{"https://gitlab.com/gnome/sub", OrgRef{model.PlatformGitLab, "gitlab.com", "https://gitlab.com", "gnome/sub"}},
		{"https://gitlab.com/groups/gnome/sub", OrgRef{model.PlatformGitLab, "gitlab.com", "https://gitlab.com", "gnome/sub"}},
		{"https://gitlab.com/gnome/sub/-/issues", OrgRef{model.PlatformGitLab, "gitlab.com", "https://gitlab.com", "gnome/sub"}},
		{"https://gitlab.freedesktop.org/mesa/sub/", OrgRef{model.PlatformGitLab, "gitlab.freedesktop.org", "https://gitlab.freedesktop.org", "mesa/sub"}},
		// A configured instance whose host does not say "gitlab" is matched by its hint.
		{"https://code.example.org/team/sub", OrgRef{model.PlatformGitLab, "code.example.org", "https://code.example.org", "team/sub"}},
	} {
		got, err := ParseOrgRef(tc.in, hints)
		if err != nil || got != tc.want {
			t.Errorf("ParseOrgRef(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		in   string
		want error
	}{
		{"", ErrInvalidRepoURL},
		{"https://github.com", ErrInvalidRepoURL},
		{"https://github.com/", ErrInvalidRepoURL},
		{"https://gitlab.com/groups/", ErrInvalidRepoURL},
		{"https://gitlab.com/-/profile", ErrInvalidRepoURL},
		{"https://user:tok@github.com/chaoss", ErrURLUserinfo},
		{"tok@github.com/chaoss", ErrURLUserinfo},
		{"ssh://git@github.com/chaoss", ErrInvalidRepoURL},
		{"https://git.example.invalid/foo", ErrUnknownPlatform},
		{"https://code.example.org/team", ErrUnknownPlatform}, // no hint given below
	} {
		var h []string
		if !strings.HasPrefix(tc.in, "https://code.example.org") {
			h = hints
		}
		if _, err := ParseOrgRef(tc.in, h); !errors.Is(err, tc.want) {
			t.Errorf("ParseOrgRef(%q) = %v; want %v", tc.in, err, tc.want)
		}
		if err := func() error { _, err := ParseOrgRef(tc.in, h); return err }(); err != nil && strings.Contains(err.Error(), "tok") {
			t.Errorf("ParseOrgRef(%q) echoes the credential: %v", tc.in, err)
		}
	}
}

func TestOrgURLFor(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/apache/arrow":          "https://github.com/apache",
		"https://gitlab.com/gnome/sub/project":     "https://gitlab.com/gnome/sub",
		"https://gitlab.freedesktop.org/mesa/mesa": "https://gitlab.freedesktop.org/mesa",
		"https://git.kernel.org/pub/scm/x/y":       "",
	} {
		r, err := ParseAnyRepoURL(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := OrgURLFor(r); got != want {
			t.Errorf("OrgURLFor(%s) = %q, want %q", in, got, want)
		}
	}
	if got := OrgURLFor(RepoURL{Platform: model.PlatformGitHub, WebBase: "https://github.com"}); got != "" {
		t.Errorf("an owner-less parse = %q, want empty", got)
	}
}
