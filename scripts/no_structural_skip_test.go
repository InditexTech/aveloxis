// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// envSkipVocabulary is the reviewed set of environment reasons a test may
// skip for (see TestNoStructuralSkips).
var envSkipVocabulary = regexp.MustCompile(strings.Join([]string{
	`AVELOXIS_TEST_\w+`, `AVELOXIS_CANARY_DB`, `AVELOXIS_WS_SCALE_REPO`, `EnvVar \+ " not set"`, `testdb\.EnvVar`,
	`set %s to[^"]*",\s*\w*Env\b`, // a format whose argument is a named ...Env constant (review HF2-3)
	`not installed`, `not on PATH`, `not found on PATH`, `no go toolchain`, `git not available`, `sha256sum nor shasum`,
	`running as root`, `root ignores`, `permission bits`, `can create directories under a read-only parent`,
	`only on darwin`, `acts only on darwin`, `symlinks (unavailable|unsupported)`, `shell-script fakes`,
	`timing comparison`, `cannot start sleep`, `cannot spawn`, `pgrep never saw`, `the closed port answered`, "`false` did not fail",
	`cannot CREATE ROLE`, `cannot GRANT`, `CREATEDB`, `cannot create scratch roles`, `pg_hba does not admit`,
	`must be a URL so the viewer`, `premise gone on this server`, `no background workers visible`,
	`an aveloxis-serve backend is already connected`, `could not acquire the test-migrate advisory lock`,
	`this host cannot name itself`, `no package -timeout`,
	`not a git work tree`, `CLAUDE\.md absent \(public checkout\)`,
	`materialized views; the absent case cannot be driven here`, `8Knot views; the fixture needs none`,
}, "|"))

// A call on a receiver (t.Skip, tb.Skipf, s.T().SkipNow …): an identifier or ) right before the dot.
var skipCallRE = regexp.MustCompile(`[\w)]\.Skip(f|Now)?\(`)

// TestNoStructuralSkips (operator rule, 2026-10-13): a test may skip only
// because its ENVIRONMENT cannot run it — no test database or network token,
// a missing tool, root, a platform, a database role without a privilege, a
// timing test under -race/-short. It may never skip because the CODE it
// checks moved, was renamed or was not found: that is a failure. A refactor
// that moves a function must turn its tests red, never silently off.
//
// 0.29.93 converted 37 structural skips ("X not yet defined", "function not
// found", "could not find repo root", …) to failures; two of them were
// already hiding broken tests (TestCDXDevScopeNotOptional's subject had
// moved to model.CycloneDXScopeForScope; TestContributorUpsertDuplicateIsDebug
// searched postgres.go for a log that now lives in internal/collector and is
// deliberately WARN — upserts are idempotent, so a failure there is real).
// Three placeholder tests that always skipped and
// tested nothing were deleted, and two data-premise skips now seed their
// data or run behind a named variable.
//
// Every t.Skip/Skipf/SkipNow message must match the reviewed environment
// vocabulary below. A message held in a variable, or none at all, fails.
// The closing suite run also counts skips (scripts/skip-census.sh): a test
// that newly skips with AVELOXIS_TEST_DB set fails the census.
func TestNoStructuralSkips(t *testing.T) {
	root := srctest.Root(t)
	examined := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Hidden directories (.git, .claude worktrees of agents) are not this tree's tests.
			if n := d.Name(); (strings.HasPrefix(n, ".") && p != root) || n == "_build" || n == "node_modules" || n == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		// Every Go file: a helper that skips on a caller's behalf (testdb,
		// srctest, a shared fixture) is held to the same rule (review HF2-3).
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		src := srctest.StripGoComments(string(b))
		rel, _ := filepath.Rel(root, p)
		for _, loc := range skipCallRE.FindAllStringIndex(src, -1) {
			// The call's argument text, up to its closing parenthesis on the line.
			line := src[loc[0]:]
			if nl := strings.IndexByte(line, '\n'); nl >= 0 {
				line = line[:nl]
			}
			if rel == filepath.Join("scripts", "url_log_redaction_test.go") && strings.Contains(line, `.Skip() }\n", false},`) {
				continue // a fixture string inside a test table, not a call
			}
			examined++
			if !envSkipVocabulary.MatchString(line) {
				t.Errorf("%s:%d skips without naming an environment reason: %s\n  a test whose subject moved must FAIL (t.Fatal), never skip; an environment gate must name its variable or tool",
					rel, strings.Count(src[:loc[0]], "\n")+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	srctest.MinCount(t, "t.Skip calls examined", examined, 300)
}
