// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

// Source-contract tests for the repo-add path. Originally written for
// the v0.25.32 AddRepoToGroup refactor (shared resolver/creator path);
// re-anchored at v0.27.20 when the flow moved into AddReposToGroup +
// ensureRepoCollectedInGroup (add_requests.go) under the per-add
// approval rule. The invariants are unchanged: exactly ONE way a repo
// row comes into existence (FindRepoByURL first, UpsertRepo only on
// the not-found branch — its DO UPDATE would clobber collected
// metadata), and the group-status abuse lever survives.

package db

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

func TestAddReposToGroupUsesSharedResolvers(t *testing.T) {
	body := extractFunctionBody(t, "add_requests.go", "ensureRepoCollectedInGroup") +
		extractFunctionBody(t, "add_requests.go", "AddReposToGroup")

	for _, needle := range []string{"s.FindRepoByURL(", "s.UpsertRepo(", "s.EnqueueRepo("} {
		if !strings.Contains(body, needle) {
			t.Errorf("the AddReposToGroup/ensureRepoCollectedInGroup path must call %s — the shared "+
				"resolver/creator path is what makes web bulk-paste case-variant-safe and "+
				"cross-user repo sharing work (a case variant of an already-collected repo "+
				"links the SHARED repo_id into the new user's group).", needle)
		}
	}

	if strings.Contains(body, "INSERT INTO aveloxis_data.repos") {
		t.Error("the repo-add path must NOT hand-roll its own INSERT INTO aveloxis_data.repos — " +
			"that parallel creation path bypassed case-variant resolution and left " +
			"data_source empty. Route through UpsertRepo.")
	}
}

// TestNoSecondRepoAddPath: the single-URL wrapper AddRepoToGroup, once a
// thin delegate kept for the CLI importers, was deleted in 0.29.100 (round-3
// review: the importers add through the admin service, and a wrapper
// without the instance hint was a second add path). The invariant it
// stood for is pinned at its stronger level: in the store, only the batch
// add and the two approval passes reach ensureRepoCollectedInGroup, and
// no wrapper of that name exists — two parallel add implementations WILL
// diverge on the per-add approval rule.
func TestNoSecondRepoAddPath(t *testing.T) {
	src := srctest.StripGoComments(srctest.PackageSource(t, "internal/db", 10))
	if strings.Contains(src, "AddRepoToGroup(") {
		t.Error("a single-URL AddRepoToGroup exists again in internal/db — add through admin.Service.AddToGroup, which calls the batch AddReposToGroup")
	}
	callers := map[string]bool{"AddReposToGroup": true, "processAddRequest": true, "processAutoApproved": true}
	examined := 0
	for _, fn := range []string{"AddReposToGroup", "processAddRequest", "processAutoApproved"} {
		if strings.Contains(srctest.StripGoComments(extractFunctionBody(t, "add_requests.go", fn)), "ensureRepoCollectedInGroup(") {
			examined++
		}
	}
	if n := strings.Count(src, "s.ensureRepoCollectedInGroup("); n != examined || examined < 2 {
		t.Errorf("ensureRepoCollectedInGroup is called from %d sites; %d are the batch add and the approval passes (%v) — any other caller is a second add path", n, examined, callers)
	}
}

// The v0.27.20 per-add approval gate: rejected groups refuse the add;
// non-admin additions of not-yet-tracked URLs pend on an add-request
// instead of enqueueing; tracked repos link into user_repos instantly.
func TestAddReposToGroupKeepsApprovalGate(t *testing.T) {
	body := extractFunctionBody(t, "add_requests.go", "AddReposToGroup")

	// v0.29.90 (Copilot review 5479554271): a non-admin paste is written
	// by addPasteWithinCap in one transaction, add request included, so the
	// gate's request writer is pinned there.
	for _, needle := range []string{`"rejected"`, "addPasteWithinCap", "repoTracked", "AddRepoToGroupByID"} {
		if !strings.Contains(body, needle) {
			t.Errorf("AddReposToGroup must keep the v0.27.20 per-add approval gate; "+
				"expected %q in the function body.", needle)
		}
	}
	if paste := extractFunctionBody(t, "capacity_store.go", "addPasteWithinCap"); !strings.Contains(paste, "insertAddRequest(") {
		t.Error("addPasteWithinCap must record a non-admin's new URLs as an add request (insertAddRequest) — " +
			"that request is the approval gate.")
	}
	// The non-admin new-URL branch must NOT enqueue directly — enqueue
	// happens via the admin path (ensureRepoCollectedInGroup) or after
	// approval (ProcessApprovedAddRequest). A direct EnqueueRepo call
	// in AddReposToGroup itself would bypass the approval rule.
	if strings.Contains(body, "s.EnqueueRepo(") {
		t.Error("AddReposToGroup must not call EnqueueRepo directly — enqueue is owned by " +
			"ensureRepoCollectedInGroup (admin/approved paths only), so a non-admin's new " +
			"URLs can never reach the queue without an approval.")
	}
}

// Existing repos must NOT be routed through UpsertRepo — its ON CONFLICT
// DO UPDATE would clobber populated columns (repo_description,
// primary_language, ...) with the web path's empty struct. UpsertRepo is
// only for the not-found branch.
func TestEnsureRepoCollectedOnlyUpsertsWhenNotFound(t *testing.T) {
	body := extractFunctionBody(t, "add_requests.go", "ensureRepoCollectedInGroup")

	findIdx := strings.Index(body, "s.FindRepoByURL(")
	upsertIdx := strings.Index(body, "s.UpsertRepo(")
	if findIdx < 0 || upsertIdx < 0 {
		t.Fatal("source structure changed (a skip would hide it): resolver calls missing — covered by TestAddReposToGroupUsesSharedResolvers")
	}
	if findIdx > upsertIdx {
		t.Error("ensureRepoCollectedInGroup must look up via FindRepoByURL FIRST and only call " +
			"UpsertRepo when the repo is not found — routing existing repos through " +
			"UpsertRepo's DO UPDATE clobbers collected metadata with empty values.")
	}
}
