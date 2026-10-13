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
		// Review HF-2: a found repository that could not be recorded or queued.
		{"func (s *Scheduler) refreshGitHubOrg(", `"org refresh: could not record discovered repository"`},
		{"func (s *Scheduler) refreshGitHubOrg(", `"org refresh: could not queue discovered repository — it will not be collected until ` + "`aveloxis reconcile-repos`" + ` queues it"`},
		{"func (s *Scheduler) refreshGitLabGroup(", `"group refresh: could not record discovered repository"`},
		{"func (s *Scheduler) refreshGitLabGroup(", `"group refresh: could not queue discovered repository — it will not be collected until ` + "`aveloxis reconcile-repos`" + ` queues it"`},
		{"func (s *Scheduler) refreshUserOrgs(", `"user-org scan: could not record discovered repository"`},
	}
	for _, c := range cases {
		body := srctest.FuncBody(t, src, c.fn)
		i := strings.Index(body, c.msg)
		if i < 0 {
			t.Errorf("%s: the failure branch must log %s", c.fn, c.msg)
			continue
		}
		// The shutdown check comes first (scripts' shutdown-classification ratchet).
		// Within the same branch: the check sits a few lines above the log
		// (other classifications, e.g. the HF-1 ErrNotFound case, may sit between).
		if !strings.Contains(body[max(0, i-900):i], "errors.Is(err, context.Canceled)") {
			t.Errorf("%s: %s must follow a context.Canceled check", c.fn, c.msg)
		}
	}
}

// Review HF-1: a 404 on /orgs/<name> is the designed fallback for a personal
// account (the /users/ listing follows); it logs at Debug, before the WARN.
func TestUserOrgScanPersonalAccountFallbackIsNotAWarning(t *testing.T) {
	body := srctest.FuncBody(t, srctest.StripGoComments(srctest.Read(t, "internal/scheduler/scheduler.go")), "func (s *Scheduler) refreshUserOrgs(")
	dbg := strings.Index(body, `"user-org scan: not an organization — trying the personal account"`)
	warn := strings.Index(body, `"user-org scan: listing request failed"`)
	if dbg < 0 || warn < 0 || dbg > warn {
		t.Fatal("refreshUserOrgs must log the /orgs/ 404 fallback at Debug before the listing-failure WARN")
	}
	if !strings.Contains(body[:dbg], "platform.ErrNotFound") || !strings.Contains(body[strings.LastIndex(body[:dbg], "if "):dbg], "logger.Debug(") {
		t.Fatal("the Debug branch must be the platform.ErrNotFound case on the /orgs/ path")
	}
}
