// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// The data test's standing repository set (operator decision 2026-10-13:
// augurlabs/augur had become too quiet to exercise recent collection paths).
// Running `aveloxis data-test` without --repo collects these three into both
// scratch databases; --repo replaces the set.
func TestDataTestDefaultRepositorySet(t *testing.T) {
	want := []string{
		"https://github.com/backstage/backstage",
		"https://github.com/MISP/MISP",
		"https://github.com/sunpy/sunpy",
	}
	if !slices.Equal(dataTestDefaultRepos, want) {
		t.Fatalf("dataTestDefaultRepos = %q, want %q", dataTestDefaultRepos, want)
	}
	if got := effectiveDataTestRepos(nil); !slices.Equal(got, want) {
		t.Fatalf("no --repo: collects %q, want the standing set %q", got, want)
	}
	only := []string{"https://github.com/chaoss/augur"}
	if got := effectiveDataTestRepos(only); !slices.Equal(got, only) {
		t.Fatalf("--repo given: collects %q, want exactly %q", got, only)
	}
	// The standing set is not shared state a caller could mutate.
	got := effectiveDataTestRepos(nil)
	got[0] = "x"
	if dataTestDefaultRepos[0] != want[0] {
		t.Fatal("effectiveDataTestRepos returned the package slice itself; a caller changed the standing set")
	}
}

// A collecting run records its repositories; --diff-only reads them back
// when --repo is omitted, and refuses rather than guess when nothing was
// recorded (so the report never names repositories the databases lack).
func TestDataTestDiffOnlyReportsTheCollectedRepositories(t *testing.T) {
	dir := t.TempDir()
	one := []string{"https://github.com/sunpy/sunpy"}
	if got, err := dataTestRunRepos(dir, one, false); err != nil || !slices.Equal(got, one) {
		t.Fatalf("collecting run = %q, %v", got, err)
	}
	if got, err := dataTestRunRepos(dir, nil, true); err != nil || !slices.Equal(got, one) {
		t.Fatalf("diff-only without --repo = %q, %v; want the recorded %q, not the standing set", got, err, one)
	}
	other := []string{"https://github.com/MISP/MISP"}
	if got, err := dataTestRunRepos(dir, other, true); err != nil || !slices.Equal(got, other) {
		t.Fatalf("diff-only with --repo = %q, %v; want %q", got, err, other)
	}
	if _, err := dataTestRunRepos(t.TempDir(), nil, true); err == nil {
		t.Fatal("diff-only with no --repo and nothing recorded must refuse")
	}
	if got, err := dataTestRunRepos(t.TempDir(), nil, false); err != nil || !slices.Equal(got, dataTestDefaultRepos) {
		t.Fatalf("collecting run without --repo = %q, %v; want the standing set", got, err)
	}
}

// The command uses the decision above (wiring; the review found that
// removing the call left the helper test green).
func TestDataTestCommandDecidesItsRepositoriesThroughTheHelper(t *testing.T) {
	src := srctest.StripGoComments(srctest.Read(t, "cmd/aveloxis/data_test_cmd.go"))
	if !strings.Contains(srctest.FuncBody(t, src, "func dataTestCmd("), "dataTestRunRepos(workDir, repoURLs, diffOnly)") {
		t.Fatal("dataTestCmd must decide its repositories with dataTestRunRepos")
	}
}
