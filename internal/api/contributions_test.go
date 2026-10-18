// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ============================================================
// Source-contract: routes are registered + handlers exist
// ============================================================

// TestContributionsRoutesRegistered pins that the three contributions
// routes (v0.23.10 identities + affiliations, v0.23.11 coverage) flow
// through the same mux as the existing endpoints. If a future refactor
// moves them to a different package or drops the registration, this
// test catches it.
func TestContributionsRoutesRegistered(t *testing.T) {
	body, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	src := string(body)
	for _, needle := range []string{
		`/api/v1/repos/{repoID}/contributions/identities`,
		`/api/v1/repos/{repoID}/contributions/affiliations`,
		`/api/v1/repos/{repoID}/contributions/coverage`,
		`s.handleRepoContributors`,
		`s.handleRepoAffiliations`,
		`s.handleRepoContributionsCoverage`,
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("server.go missing route registration token %q", needle)
		}
	}
}

// TestRepoContributionsCoverage_InvalidID pins the 400-on-bad-repo_id
// contract for the coverage endpoint.
func TestRepoContributionsCoverage_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/contributions/coverage", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid repo_id", w.Code)
	}
}

// TestRepoContributionsCoverage_SinceAfterUntil pins the same
// window-validation contract as the other two endpoints.
func TestRepoContributionsCoverage_SinceAfterUntil(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET",
		"/api/v1/repos/1/contributions/coverage?since=2026-01-01&until=2025-01-01", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for since >= until", w.Code)
	}
}

// ============================================================
// Parameter validation (nil store — routing-layer only)
// ============================================================

func TestRepoContributors_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/contributions/identities", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid repo_id", w.Code)
	}
}

func TestRepoAffiliations_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/contributions/affiliations", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid repo_id", w.Code)
	}
}

// TestRepoContributors_SinceAfterUntil pins that an invalid window
// surfaces as 400 instead of being silently flipped to a default
// (which would mask the operator's mistake).
func TestRepoContributors_SinceAfterUntil(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET",
		"/api/v1/repos/1/contributions/identities?since=2026-01-01&until=2025-01-01", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for since >= until", w.Code)
	}
}

func TestRepoAffiliations_SinceAfterUntil(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET",
		"/api/v1/repos/1/contributions/affiliations?since=2026-01-01&until=2025-01-01", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for since >= until", w.Code)
	}
}

// ============================================================
// parseWindow unit tests
// ============================================================

// TestParseWindowDefaults pins the 2-year default for ?since with no
// upper bound (zero until). Matches the operator's "last 2 years" ask
// from the original query request.
func TestParseWindowDefaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/repos/1/contributions/identities", nil)
	since, until, err := parseWindow(r)
	if err != nil {
		t.Fatalf("default window: %v", err)
	}
	// since should be ~2 years before now; until should be zero.
	if since.IsZero() {
		t.Error("default since should not be zero")
	}
	if !until.IsZero() {
		t.Error("default until should be zero (unbounded)")
	}
}

// TestParseWindowInclusiveUntil pins the "until is inclusive" contract:
// a YYYY-MM-DD until param is shifted by +1 day before going to the
// store, so the store's exclusive-upper comparison includes the
// requested final calendar day.
func TestParseWindowInclusiveUntil(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/api/v1/repos/1/contributions/identities?since=2024-01-01&until=2024-12-31", nil)
	since, until, err := parseWindow(r)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if since.Format("2006-01-02") != "2024-01-01" {
		t.Errorf("since = %v, want 2024-01-01", since)
	}
	// until passed as 2024-12-31 should land as 2025-01-01 after the
	// inclusive-end shift.
	if until.Format("2006-01-02") != "2025-01-01" {
		t.Errorf("until = %v, want 2025-01-01 (one day past 2024-12-31)", until)
	}
}

// TestParseWindowInvalidDateIsAnError pins D7 (stage 1b): a value that is
// not a YYYY-MM-DD date is an error the handler answers with 400 — it fell
// back to the default before, which hid a client's typo as a default
// window. The error names the parameter and the value.
func TestParseWindowInvalidDateIsAnError(t *testing.T) {
	for _, q := range []string{"since=garbage", "until=2024-02-30", "since=2024-1-1", "until=2024-13-01&since=2024-01-01"} {
		r := httptest.NewRequest("GET", "/api/v1/repos/1/contributions/identities?"+q, nil)
		_, _, err := parseWindow(r)
		if err == nil || !strings.Contains(err.Error(), "want YYYY-MM-DD") {
			t.Errorf("?%s: err = %v; want the date refusal", q, err)
		}
	}
	// The order rule is unchanged, and distinct from a bad date.
	r := httptest.NewRequest("GET", "/api/v1/repos/1/contributions/identities?since=2024-06-01&until=2024-01-01", nil)
	if _, _, err := parseWindow(r); err == nil || !strings.Contains(err.Error(), "since must be before until") {
		t.Errorf("since after until: err = %v", err)
	}
}
