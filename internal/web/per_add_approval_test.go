// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

// Source-contract pins for the v0.27.20 per-add approval surface on
// the web process (summary/15, Option A).

package web

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestHandleAddRepoBatchesThroughAddReposToGroup pins that the bulk
// paste is ONE AddReposToGroup call (one approval unit per submit)
// carrying the configured auto-approve limit, not a per-URL loop over
// the legacy single-add.
func TestHandleAddRepoBatchesThroughAddReposToGroup(t *testing.T) {
	handler := srctest.StripGoComments(srctest.PackageFunc(t, "internal/web", "func (s *Server) handleAddRepo("))
	if n := strings.Count(handler, "s.admin.AddToGroup("); n != 1 || !strings.Contains(handler, `strings.Split(raw, "\n")`) {
		t.Errorf("handleAddRepo must hand the WHOLE paste to one admin.Service.AddToGroup call (found %d)", n)
	}
	// The service makes one store call with the configured limit (stage 1
	// of summary/59; the handler no longer reaches the store).
	svc := srctest.StripGoComments(srctest.PackageFunc(t, "internal/admin", "func (s *Service) AddToGroup("))
	if n := strings.Count(svc, "s.st.AddReposToGroup("); n != 1 || !strings.Contains(svc, "s.opts.AutoApproveAddLimit, s.glWebBase)") {
		t.Errorf("admin.Service.AddToGroup must route the paste through ONE AddReposToGroup call carrying Options.AutoApproveAddLimit (found %d)", n)
	}
	wiring := srctest.StripGoComments(srctest.PackageFunc(t, "internal/web", "func (s *Server) newAdminService("))
	if !strings.Contains(wiring, "AutoApproveAddLimit: s.cfg.AutoApproveAddLimitValue()") {
		t.Error("the web process must hand web.auto_approve_add_limit to the service")
	}
	for _, src := range []string{handler, svc} {
		if strings.Contains(src, "AddRepoToGroup(") {
			t.Error("a paste must not fall back to per-URL AddRepoToGroup calls — " +
				"that would split one paste into N approval units")
		}
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
	web := srctest.StripGoComments(srctest.PackageFunc(t, "internal/web", "func (s *Server) decideAddRequestPage("))
	if n := strings.Count(web, "s.admin.DecideAddRequest("); n != 1 {
		t.Errorf("the web decision page must call admin.Service.DecideAddRequest once (found %d)", n)
	}
	body := srctest.StripGoComments(srctest.PackageFunc(t, "internal/admin", "func (s *Service) DecideAddRequest(") +
		srctest.PackageFunc(t, "internal/admin", "func (s *Service) runApprovedPass("))
	for _, needle := range []string{"safego.Go", "ProcessApprovedAddRequest(context.Background()", "SendAddRequestDecided"} {
		if !strings.Contains(body, needle) {
			t.Errorf("admin.Service.DecideAddRequest missing %q — approval must process items "+
				"in the background and notify the requester", needle)
		}
	}
}
