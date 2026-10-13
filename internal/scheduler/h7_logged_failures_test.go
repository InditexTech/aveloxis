// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scheduler

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// H-7 (summary/59 §5, the hotfix release): four org-scan and rename-check
// failure branches ended the work without a log line ("everything that
// errors should be logged"). They now log, after the shutdown check. These
// are source pins on purpose: stage 1 replaces this code with one lister per
// forge that gets runtime tests (summary/59 §5b), so a scheduler harness
// built now would be thrown away.
func TestOrgScanFailureBranchesLog(t *testing.T) {
	src := srctest.StripGoComments(srctest.Read(t, "internal/scheduler/scheduler.go"))
	cases := []struct{ fn, msg string }{
		{"func (s *Scheduler) refreshGitHubOrg(", `"org refresh: page did not decode — listing stopped"`},
		{"func (s *Scheduler) refreshGitLabGroup(", `"group refresh: page did not decode — listing stopped"`},
		{"func (s *Scheduler) refreshUserOrgs(", `"user-org scan: listing request failed"`},
		{"func (s *Scheduler) checkForRenames(", `"rename check: prelim failed"`},
	}
	for _, c := range cases {
		body := srctest.FuncBody(t, src, c.fn)
		i := strings.Index(body, c.msg)
		if i < 0 {
			t.Errorf("%s: the failure branch must log %s", c.fn, c.msg)
			continue
		}
		// The shutdown check comes first (scripts' shutdown-classification ratchet).
		if j := strings.LastIndex(body[:i], "errors.Is("); j < 0 || !strings.Contains(body[j:i], "context.Canceled") {
			t.Errorf("%s: %s must follow a context.Canceled check", c.fn, c.msg)
		}
	}
}
