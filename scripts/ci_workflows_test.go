// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

import (
	"os"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
	"gopkg.in/yaml.v3"
)

// TestCIWorkflowsCarryTheDeploymentJobs (0.29.107, after the PR #238
// loops): the integration workflow runs the upgrade-path job (the base
// release migrates and populates, the head release migrates again the way
// the ladders do) and the processes job (serve, api and web from one
// binary, the contracts between them over HTTP), each through its script
// under scripts/; the nightly workflow is scheduled AND dispatchable and
// carries the race stress and the golden collection. A job that is
// renamed, dropped or detached from its script turns this red.
func TestCIWorkflowsCarryTheDeploymentJobs(t *testing.T) {
	type step struct {
		Name string `yaml:"name"`
		Run  string `yaml:"run"`
	}
	type workflow struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	load := func(path string) workflow {
		var wf workflow
		if err := yaml.Unmarshal([]byte(srctest.Read(t, path)), &wf); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return wf
	}
	runs := func(wf workflow, job, script string) {
		j, ok := wf.Jobs[job]
		if !ok {
			t.Errorf("job %q is missing", job)
			return
		}
		for _, st := range j.Steps {
			if strings.Contains(st.Run, script) {
				return
			}
		}
		t.Errorf("job %q runs no step through %s", job, script)
	}
	integ := load(".github/workflows/integration.yml")
	runs(integ, "upgrade-path", "scripts/ci-upgrade-path.sh")
	runs(integ, "processes", "scripts/ci-process-contracts.sh")
	for _, script := range []string{"scripts/ci-upgrade-path.sh", "scripts/ci-process-contracts.sh"} {
		if info, err := os.Stat(srctest.Root(t) + "/" + script); err != nil || info.Mode()&0o111 == 0 {
			t.Errorf("%s is missing or not executable (%v)", script, err)
		}
	}
	nightly := load(".github/workflows/nightly.yml")
	if _, ok := nightly.On["schedule"]; !ok {
		t.Error("nightly.yml is not scheduled")
	}
	if _, ok := nightly.On["workflow_dispatch"]; !ok {
		t.Error("nightly.yml cannot be run by hand (workflow_dispatch)")
	}
	runs(nightly, "race-stress", "-race")
	runs(nightly, "golden-collection", "data-test")
}
