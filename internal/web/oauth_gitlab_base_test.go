// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/platform"
	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestGitLabOAuthEndpointsTakeTheOneBase (0.29.101, WEB-14 of summary/59):
// the OAuth endpoints are built from config.WebConfig.GitLabOAuthBase —
// the one default and the one trailing-slash trim — so an empty setting
// signs in through gitlab.com and a self-hosted instance's spelling is
// kept as typed (an instance path is case-sensitive).
func TestGitLabOAuthEndpointsTakeTheOneBase(t *testing.T) {
	for configured, base := range map[string]string{
		"":                                platform.PublicGitLabWebBase,
		"https://GitLab.Example.org/Git/": "https://GitLab.Example.org/Git",
	} {
		s := New(nil, config.WebConfig{DevMode: true, GitLabClientID: "id", GitLabClientSecret: "secret", GitLabBaseURL: configured}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
		if s.glOAuth == nil {
			t.Fatalf("GitLabBaseURL %q: no GitLab OAuth config built", configured)
		}
		if got, want := s.glOAuth.Endpoint.AuthURL, base+"/oauth/authorize"; got != want {
			t.Errorf("GitLabBaseURL %q: AuthURL = %q, want %q", configured, got, want)
		}
		if got, want := s.glOAuth.Endpoint.TokenURL, base+"/oauth/token"; got != want {
			t.Errorf("GitLabBaseURL %q: TokenURL = %q, want %q", configured, got, want)
		}
		if got := s.cfg.GitLabBaseURL; got != base {
			t.Errorf("GitLabBaseURL %q: the server keeps %q, want the one base %q", configured, got, base)
		}
	}
}

// TestWebSpellsNoGitLabDefaultItself: the gitlab.com default is
// platform.PublicGitLabWebBase through the config accessor; a second
// fallback in the package (the callback had its own until 0.29.101) is a
// second default to drift.
func TestWebSpellsNoGitLabDefaultItself(t *testing.T) {
	src := srctest.StripGoComments(srctest.PackageSource(t, "internal/web", 10))
	if strings.Contains(src, `"`+platform.PublicGitLabWebBase+`"`) {
		t.Errorf("internal/web spells %q itself; the default is config.WebConfig.GitLabOAuthBase", platform.PublicGitLabWebBase)
	}
	for _, needle := range []string{`glBase == ""`, `GitLabBaseURL == ""`} {
		if strings.Contains(src, needle) {
			t.Errorf("internal/web still falls back on %s; New takes GitLabOAuthBase once", needle)
		}
	}
}
