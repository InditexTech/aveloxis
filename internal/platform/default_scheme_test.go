// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

func TestWithDefaultScheme(t *testing.T) {
	for in, want := range map[string]string{
		"":                                  "",
		"   ":                               "",
		"github.com/chaoss/augur":           "https://github.com/chaoss/augur",
		"  github.com/chaoss/augur \n":      "https://github.com/chaoss/augur",
		"https://github.com/chaoss/augur":   "https://github.com/chaoss/augur",
		"http://gitlab.example.com/g/p":     "http://gitlab.example.com/g/p",
		"ssh://git@github.com/chaoss/augur": "ssh://git@github.com/chaoss/augur",
		"git://example.com/x/y":             "git://example.com/x/y",
		"localhost:8080/a/b":                "https://localhost:8080/a/b",
	} {
		if got := WithDefaultScheme(in); got != want {
			t.Errorf("WithDefaultScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDefaultSchemeHasOneSpelling (SR-17): the only non-test source that
// prepends the scheme is WithDefaultScheme itself; every other site calls
// it. The denominator is the whole package source of each package that
// accepts pasted URLs (a negative needle, srctest.PackageSource).
func TestDefaultSchemeHasOneSpelling(t *testing.T) {
	count := func(src string) int {
		src = srctest.StripGoComments(src)
		return strings.Count(src, `"https://" + `) + strings.Count(src, `"https://"+`)
	}
	own := count(srctest.PackageFunc(t, "internal/platform", "func WithDefaultScheme("))
	if own != 1 {
		t.Fatalf("WithDefaultScheme prepends the scheme %d times, want 1 — the pin lost its subject", own)
	}
	if n := count(srctest.PackageSource(t, "internal/platform", 10)); n != own {
		t.Errorf("internal/platform prepends \"https://\" inline at %d site(s) besides WithDefaultScheme: call it instead (one spelling)", n-own)
	}
	for _, dir := range []string{"internal/db", "internal/web", "internal/api", "internal/admin", "cmd/aveloxis"} {
		if n := count(srctest.PackageSource(t, dir, 5)); n != 0 {
			t.Errorf("%s prepends \"https://\" inline at %d site(s): call platform.WithDefaultScheme instead (one spelling)", dir, n)
		}
	}
}
