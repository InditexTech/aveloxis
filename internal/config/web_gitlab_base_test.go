// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/platform"
)

// TestLoadRefusesAnUnusableWebGitLabBase (0.29.101, deferred from the
// 0.29.100 round-6 sibling): web.gitlab_base_url is the GitLab instance
// people sign in through — its OAuth endpoints and the /api/v4/user read
// are built by appending to it, and the users rows record it as the
// account's instance. A value that is not the instance's web URL (no
// scheme, the API URL, a query, a user name) is refused at load, naming
// the field, as gitlab.base_url is; the default and an explicit "" mean
// gitlab.com. The refusal echoes the value redacted.
func TestLoadRefusesAnUnusableWebGitLabBase(t *testing.T) {
	for _, tc := range []struct {
		name string
		web  string
		want string // a substring of the error; "" = loads
		base string // the effective OAuth base when it loads
	}{
		{"absent block keeps the default", `{}`, "", platform.PublicGitLabWebBase},
		{"explicit empty is gitlab.com", `{"gitlab_base_url": ""}`, "", platform.PublicGitLabWebBase},
		{"self-hosted", `{"gitlab_base_url": "https://gitlab.example.org"}`, "", "https://gitlab.example.org"},
		{"trailing slash dropped", `{"gitlab_base_url": "https://gitlab.example.org/"}`, "", "https://gitlab.example.org"},
		{"under a path prefix", `{"gitlab_base_url": "https://example.org/gitlab/"}`, "", "https://example.org/gitlab"},
		{"the API URL is not the web site", `{"gitlab_base_url": "https://gitlab.example.org/api/v4"}`, "web.gitlab_base_url", ""},
		{"no scheme", `{"gitlab_base_url": "gitlab.example.org"}`, "web.gitlab_base_url", ""},
		{"a query", `{"gitlab_base_url": "https://gitlab.example.org?x=1"}`, "web.gitlab_base_url", ""},
		{"a fragment", `{"gitlab_base_url": "https://gitlab.example.org#x"}`, "web.gitlab_base_url", ""},
		{"credentials", `{"gitlab_base_url": "https://u:p@gitlab.example.org"}`, "web.gitlab_base_url", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "aveloxis.json")
			if err := os.WriteFile(path, []byte(`{"database": {"host": "localhost"}, "web": `+tc.web+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Load = %v; want the block accepted", err)
				}
				if got := cfg.Web.GitLabOAuthBase(); got != tc.base {
					t.Fatalf("GitLabOAuthBase() = %q, want %q", got, tc.base)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "invalid config: ") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %v; want an invalid-config error naming %s", err, tc.want)
			}
			if strings.Contains(err.Error(), "u:p@") || strings.Contains(err.Error(), ":p@") {
				t.Errorf("the refusal echoes the URL's credentials: %v", err)
			}
		})
	}
}

// TestWebGitLabOAuthBaseIsTheOneDefault: the accessor is the single
// place the gitlab.com default and the trailing-slash trim live (0.29.101;
// WEB-14 of summary/59 — the web server, the callback and the store each
// spelled the default themselves).
func TestWebGitLabOAuthBaseIsTheOneDefault(t *testing.T) {
	for raw, want := range map[string]string{
		"":                               platform.PublicGitLabWebBase,
		"   ":                            platform.PublicGitLabWebBase,
		"https://gitlab.com":             platform.PublicGitLabWebBase,
		"https://gitlab.example.org/":    "https://gitlab.example.org",
		" https://gitlab.example.org// ": "https://gitlab.example.org",
		"https://GitLab.Example.org/Git": "https://GitLab.Example.org/Git",
	} {
		if got := (WebConfig{GitLabBaseURL: raw}).GitLabOAuthBase(); got != want {
			t.Errorf("WebConfig{GitLabBaseURL: %q}.GitLabOAuthBase() = %q, want %q", raw, got, want)
		}
	}
}
