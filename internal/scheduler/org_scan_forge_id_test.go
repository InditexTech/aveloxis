// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

// v0.27.102 — org scans capture the forge's numeric repo ID.
//
// The 2026-08-19 rename-dup audit: 12 data-bearing duplicate rows were
// minted by org scans re-discovering renamed/transferred repos under
// their NEW URLs. The listing JSON the scans decode carries the numeric
// `id` — the only rename-proof identity — and the one lister per forge
// must (a) decode it with the listing's created_at, and the one ingest
// must (b) pass it into UpsertRepo (which heals renames instead of
// duplicating), and (c) opportunistically backfill it, dated, onto
// already-tracked rows so the at-risk cohort gains protection without
// waiting for Phase 0 collection cycles (stage 1 of summary/59: three
// scan sites became two listers and one ingest).
package scheduler

import (
	"regexp"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

func TestOrgScansCaptureForgeRepoID(t *testing.T) {
	for _, l := range []struct{ dir, sig string }{
		{"internal/platform/github", "func (c *Client) ListOwnerRepos("},
		{"internal/platform/gitlab", "func (c *Client) ListGroupProjects("},
	} {
		code := srctest.StripGoComments(srctest.PackageFunc(t, l.dir, l.sig))
		if !strings.Contains(code, "`json:\"id\"`") {
			t.Errorf("%s must decode the listing's numeric `id` field", l.sig)
		}
		if !strings.Contains(code, "`json:\"created_at\"`") {
			t.Errorf("%s must decode the listing's created_at", l.sig)
		}
		if !strings.Contains(code, "ForgeID: model.ForgeIDString(it.ID)") || !strings.Contains(code, "CreatedAt: it.CreatedAt") {
			t.Errorf("%s must yield the forge id (model.ForgeIDString) and the creation time", l.sig)
		}
	}
	code := srctest.StripGoComments(srctest.PackageFunc(t, "internal/scheduler", "func (s *Scheduler) ingestDiscovered("))
	if !strings.Contains(code, "PlatformID: item.ForgeID") {
		t.Error("ingestDiscovered must pass the forge ID into UpsertRepo via model.Repo.PlatformID")
	}
	// SetPlatformRepoIDIfEmptySeen (v0.29.63) is the same backfill that
	// also records the listed repository's creation date; the date must
	// REACH the call (a zero time.Time{} passed an earlier pin).
	if strings.Contains(code, "SetPlatformRepoIDIfEmpty(") {
		t.Error("ingestDiscovered calls the plain SetPlatformRepoIDIfEmpty — use SetPlatformRepoIDIfEmptySeen with the listing's created_at")
	}
	calls := regexp.MustCompile(`SetPlatformRepoIDIfEmptySeen\(.*`).FindAllString(code, -1)
	if len(calls) != 1 || !strings.Contains(calls[0], "item.CreatedAt") {
		t.Errorf("ingestDiscovered must backfill the forge ID, dated with the listing's CreatedAt, onto already-tracked rows: %q", calls)
	}
	// No pass decodes a listing itself any more.
	src := srctest.StripGoComments(srctest.Read(t, "internal/scheduler/scheduler.go"))
	if strings.Contains(src, "json.NewDecoder(resp.Body)") {
		t.Error("scheduler.go decodes a forge listing itself; the listers in internal/platform are the only decoders")
	}
}
