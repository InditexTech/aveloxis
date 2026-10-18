// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

// Source-contract pins for the v0.27.20 per-add approval surface on
// the web process (summary/15, Option A).

package web

import (
	"os"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

func readWebSource(t *testing.T, name string) string {
	t.Helper()
	src, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(src)
}

// TestHandleAddRepoBatchesThroughAddReposToGroup pins that the bulk
// paste is ONE AddReposToGroup call (one approval unit per submit)
// carrying the configured auto-approve limit, not a per-URL loop over
// the legacy single-add.
func TestHandleAddRepoBatchesThroughAddReposToGroup(t *testing.T) {
	body := srctest.PackageFunc(t, "internal/web", "func (s *Server) handleAddRepo(")
	if !strings.Contains(body, "s.store.AddReposToGroup(") {
		t.Error("handleAddRepo must route the whole paste through ONE AddReposToGroup call")
	}
	if !strings.Contains(body, "AutoApproveAddLimitValue()") {
		t.Error("handleAddRepo must pass the configured web.auto_approve_add_limit")
	}
	if strings.Contains(body, "s.store.AddRepoToGroup(") {
		t.Error("handleAddRepo must not fall back to per-URL AddRepoToGroup calls — " +
			"that would split one paste into N approval units")
	}
}

// TestHandleAddOrgHonorsPendingOutcome pins that the immediate org
// scan fires ONLY when the registration actually happened (admin
// path) — a non-admin's pending request must not scan anything.
func TestHandleAddOrgHonorsPendingOutcome(t *testing.T) {
	body := srctest.PackageFunc(t, "internal/web", "func (s *Server) handleAddOrg(")
	if !strings.Contains(body, "out.Registered") {
		t.Error("handleAddOrg must branch on OrgAddOutcome.Registered — scanning an " +
			"unregistered (pending) org would bypass the per-add approval rule")
	}
}

// Stage 1 of summary/59 (C2): the web process scans no org — it holds no
// forge key and no HTTP client to a forge; an org registered or approved
// here is listed by serve's one lister, whose rejected-group gate is
// pinned at runtime (scheduler TestRefreshUserOrgsEnumeratesEachOrgOnce)
// and in source (scheduler org_rejected_gate_fail_closed_test.go).
func TestWebScansNoOrganization(t *testing.T) {
	src := srctest.StripGoComments(srctest.PackageSource(t, "internal/web", 10))
	for _, banned := range []string{"platform.NewHTTPClient(", "platform.KeyPool", "scanOrgRepos", `"/orgs/`, `"/users/`} {
		if strings.Contains(src, banned) {
			t.Errorf("internal/web spells %q: the web process holds no forge key and lists no org (C2)", banned)
		}
	}
}

// TestAddRequestAdminRoutesRegistered pins the web admin decision
// routes + their requireAdmin gating.
func TestAddRequestAdminRoutesRegistered(t *testing.T) {
	s := srctest.PackageFunc(t, "internal/web", "func (s *Server) Handler(")
	for _, needle := range []string{
		`mux.HandleFunc("POST /admin/add-requests/{id}/approve", s.requireAdmin(s.handleApproveAddRequest))`,
		`mux.HandleFunc("POST /admin/add-requests/{id}/reject", s.requireAdmin(s.handleRejectAddRequest))`,
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("Handler must register (admin-gated): %s", needle)
		}
	}
}

// TestApprovalProcessingRunsInBackground pins that a large approval
// never blocks the admin's request: item processing runs under a safego
// goroutine from the shared decision flow (the approved org is listed by
// serve, stage 1 of summary/59).
func TestApprovalProcessingRunsInBackground(t *testing.T) {
	s := readWebSource(t, "admin.go")
	start := strings.Index(s, "func (s *Server) decideAddRequest(")
	if start < 0 {
		t.Fatal("decideAddRequest not found in admin.go")
	}
	body := s[start:]
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end+1]
	}
	for _, needle := range []string{"safego.Go", "ProcessApprovedAddRequest", "SendAddRequestDecided"} {
		if !strings.Contains(body, needle) {
			t.Errorf("decideAddRequest missing %q — approval must process items/scan the org "+
				"in the background and notify the requester", needle)
		}
	}
}
