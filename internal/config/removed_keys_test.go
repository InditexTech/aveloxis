// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// Stage 1 of summary/59 (N15): gitlab.gitlab_hosts is gone — it was
// documented and read nowhere (the configured gitlab.base_url names the
// instance a URL is classified against). json.Unmarshal ignores an unknown
// key silently, so a file still carrying it gets a load-time warning that
// names the key and what replaced it; a file without it gets none. The
// negative tripwire keeps the field from coming back.
func TestRemovedGitLabHostsKeyIsWarnedAboutAndNeverRead(t *testing.T) {
	src := srctest.StripGoComments(srctest.PackageSource(t, "internal/config", 1))
	if strings.Contains(src, "GitLabHosts") || strings.Count(src, "gitlab_hosts") != 1 {
		t.Errorf("internal/config spells GitLabHosts / gitlab_hosts again (want the one removedKeys entry): the key was never read; remove it, do not deprecate it")
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg, err := Load(write("old.json", `{"gitlab": {"api_keys": [], "base_url": "https://gitlab.example.org/api/v4", "gitlab_hosts": ["code.example.org"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.LoadWarnings()
	if len(w) != 1 || !strings.Contains(w[0], "gitlab.gitlab_hosts") || !strings.Contains(w[0], "gitlab.base_url") {
		t.Errorf("warnings = %q; want one naming gitlab.gitlab_hosts and its replacement", w)
	}
	cfg, err = Load(write("new.json", `{"gitlab": {"api_keys": [], "base_url": "https://gitlab.example.org/api/v4"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if w := cfg.LoadWarnings(); len(w) != 0 {
		t.Errorf("warnings = %q; want none", w)
	}
}
