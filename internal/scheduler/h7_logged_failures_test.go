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
// errors should be logged"). They log, after the shutdown check. Stage 1
// (summary/59 §5b) moved the passes onto one lister per forge and one
// ingest: a listing that ends early is logged by each pass, and a found
// repository that could not be recorded or queued by the ingest (the rename
// sweep is gone, N3). The listers themselves have runtime tests.
func TestOrgScanFailureBranchesLog(t *testing.T) {
	cases := []struct{ fn, msg string }{
		{"func (s *Scheduler) refreshGitHubOrg(", `"org refresh: listing failed — recorded what was listed; the next pass retries"`},
		{"func (s *Scheduler) refreshGitLabGroup(", `"group refresh: listing failed — recorded what was listed; the next pass retries"`},
		{"func (s *Scheduler) refreshUserOrgs(", `"org listing failed — linking what was listed; the next full pass retries"`},
		// Review HF-2: a found repository that could not be recorded or queued.
		{"func (s *Scheduler) ingestDiscovered(", `"org scan: could not record discovered repository"`},
		{"func (s *Scheduler) ingestDiscovered(", `"org scan: could not queue discovered repository — it will not be collected until ` + "`aveloxis reconcile-repos`" + ` queues it"`},
		{"func (s *Scheduler) ingestDiscovered(", `"org scan: repository lookup failed — not recorded this pass"`},
	}
	for _, c := range cases {
		body := srctest.StripGoComments(srctest.PackageFunc(t, "internal/scheduler", c.fn))
		i := strings.Index(body, c.msg)
		if i < 0 {
			t.Errorf("%s: the failure branch must log %s", c.fn, c.msg)
			continue
		}
		// The shutdown check comes first (scripts' shutdown-classification ratchet).
		// Within the same branch: the check sits a few lines above the log
		// (other classifications, e.g. the HF-1 ErrNotFound case, may sit between).
		if !strings.Contains(body[max(0, i-900):i], "context.Canceled") {
			t.Errorf("%s: %s must follow a context.Canceled check", c.fn, c.msg)
		}
	}
}

// Review HF-1: a 404 on /orgs/<name> is the designed fallback for a personal
// account (the /users/ listing follows); it logs at Debug, in the lister
// since stage 1, on the platform.ErrNotFound arm only.
func TestUserOrgScanPersonalAccountFallbackIsNotAWarning(t *testing.T) {
	body := srctest.StripGoComments(srctest.PackageFunc(t, "internal/platform/github", "func (c *Client) ListOwnerRepos("))
	dbg := strings.Index(body, `"not an organization — listing the personal account"`)
	if dbg < 0 {
		t.Fatal("ListOwnerRepos must log the /orgs/ 404 fallback at Debug")
	}
	if !strings.Contains(body[strings.LastIndex(body[:dbg], "if "):dbg], "logger.Debug(") || !strings.Contains(body[strings.LastIndex(body[:dbg], "if "):dbg], "platform.ErrNotFound") {
		t.Fatal("the Debug branch must be the platform.ErrNotFound case on the /orgs/ path")
	}
	if strings.Contains(body, "logger.Warn(") {
		t.Error("the lister yields its error; the caller decides the WARN")
	}
}
