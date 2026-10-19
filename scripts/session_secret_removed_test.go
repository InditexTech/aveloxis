// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestSessionSecretNotReintroduced (0.29.102, CORE-11): web.session_secret
// was dead config — accepted by the loader, read by nothing — and is
// removed, not deprecated (the house rule). Sessions are hashed database
// rows that sign nothing. The key must not come back as a field, in an
// example configuration, or in a public page (a reader would set it).
func TestSessionSecretNotReintroduced(t *testing.T) {
	root := srctest.Root(t)
	// The one allowed spelling in internal/config is the removedKeys entry
	// that reports a file still carrying the key (0.29.106, P3) — never a
	// field.
	if src := srctest.StripGoComments(srctest.PackageSource(t, "internal/config", 1)); strings.Contains(src, "SessionSecret") || strings.Count(src, "session_secret") != 1 {
		t.Error("internal/config declares web.session_secret again (want the one removedKeys entry)")
	}
	var checked int
	// The tracked example configurations only: an operator's own file (the
	// private aveloxis.<host>.json files are untracked) may still carry the
	// key, which the loader ignores.
	for _, pattern := range []string{"aveloxis.example.json", "aveloxis.docker.example.json", "README.md", "docs/*.md", "docs/*/*.md"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			if strings.Contains(m, "_build") || strings.Contains(m, "node_modules") {
				continue
			}
			b, err := os.ReadFile(m)
			if err != nil {
				t.Fatal(err)
			}
			checked++
			for _, line := range strings.Split(string(b), "\n") {
				// The removal is stated once per page; a key or a table row is not.
				if strings.Contains(line, `"session_secret"`) || strings.Contains(line, "| `web.session_secret`") {
					t.Errorf("%s sets or documents web.session_secret: %q", strings.TrimPrefix(m, root+"/"), line)
				}
			}
		}
	}
	srctest.MinCount(t, "files checked for session_secret", checked, 30)
}
