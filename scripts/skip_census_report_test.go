// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// Copilot review 5559632343 (PR #229, r4304130891): on a failed suite the
// census printed the failed tests' NAMES and kept the raw JSON in a temp
// folder — and the integration job discarded the runner, so the assertion
// messages, panic and race traces and the `-test.shuffle` seed needed to
// reproduce an order-dependent failure were gone. The failure report now
// prints each failed package's seed and each failed test's own output; the
// script reads a recorded suite through SKIP_CENSUS_SUITE_JSON so the
// report is tested without running the suite (the recording is .jsonl:
// the repository ignores *.json).
func TestSkipCensusPrintsFailedOutputAndShuffleSeed(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	root := srctest.Root(t)
	out := t.TempDir()
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "skip-census.sh"))
	cmd.Env = append(os.Environ(),
		"AVELOXIS_TEST_DB=postgres://unused/the-suite-is-recorded",
		"SKIP_CENSUS_SUITE_JSON="+filepath.Join(root, "scripts", "testdata", "skip_census_failed_suite.jsonl"),
		"SKIP_CENSUS_OUT="+out,
	)
	got, err := cmd.CombinedOutput()
	report := string(got)
	if err == nil {
		t.Errorf("a recorded suite with a failed package must fail the census; it exited 0:\n%s", report)
	}
	for _, want := range []string{
		"internal/badpkg TestBroken",                                        // the failed test, named
		"-test.shuffle 4242424242",                                          // the failed package's seed
		`broken_test.go:12: reach = 2; want 1 (the "fill" links what fits)`, // its assertion, unescaped
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the failure report lacks %q:\n%s", want, report)
		}
	}
	for _, unwanted := range []string{"-test.shuffle 1111", "TestAlsoFine", "TestFine"} {
		if strings.Contains(report, unwanted) {
			t.Errorf("the failure report prints %q, which belongs to a passing package or test — the report is the failed tests' output, not the whole run:\n%s", unwanted, report)
		}
	}
	for _, kept := range []string{"suite.json", "stderr.txt"} {
		if _, err := os.Stat(filepath.Join(out, kept)); err != nil {
			t.Errorf("SKIP_CENSUS_OUT must keep %s for the CI artifact: %v", kept, err)
		}
	}
}

// The integration job must keep the census's raw output when it fails:
// the run step names the folder through SKIP_CENSUS_OUT and an
// upload-artifact step on failure uploads that same folder.
func TestIntegrationJobUploadsTheSkipCensusOutputOnFailure(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Uses string            `yaml:"uses"`
				If   string            `yaml:"if"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(srctest.Read(t, ".github/workflows/integration.yml")), &wf); err != nil {
		t.Fatal(err)
	}
	var outDir string
	uploaded := false
	for _, job := range wf.Jobs {
		for _, st := range job.Steps {
			if strings.Contains(st.Run, "scripts/skip-census.sh") {
				outDir = st.Env["SKIP_CENSUS_OUT"]
				if outDir == "" {
					t.Error("the skip-census step must set SKIP_CENSUS_OUT so the raw output lands where the artifact step looks")
				}
			}
		}
		for _, st := range job.Steps {
			if strings.HasPrefix(st.Uses, "actions/upload-artifact") && outDir != "" && st.With["path"] == outDir {
				uploaded = true
				if !strings.Contains(st.If, "failure()") {
					t.Errorf("the skip-census artifact step must run on failure (if: failure()), got if: %q", st.If)
				}
			}
		}
	}
	if !uploaded {
		t.Errorf("no upload-artifact step uploads the skip-census output folder %q", outDir)
	}
}
