// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// All metric endpoint tests verify parameter validation and route registration.
// Database interactions are tested implicitly — nil store panics are recovered.

// ============================================================
// Utility endpoint routes
// ============================================================

func TestRoute_RepoGroups(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repo-groups", nil)
	w := httptest.NewRecorder()
	defer func() { recover() }()
	srv.mux.ServeHTTP(w, req)
	// Should not return 404 (route exists).
	if w.Code == http.StatusNotFound {
		t.Error("route /repo-groups should be registered")
	}
}

func TestRoute_AllRepos(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos", nil)
	w := httptest.NewRecorder()
	defer func() { recover() }()
	srv.mux.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Error("route /repos should be registered")
	}
}

func TestRoute_RepoByID_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestRoute_ReposByGroup_InvalidGroupID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repo-groups/abc/repos", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestRoute_RepoByOwnerName(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/owner/chaoss/repo/augur", nil)
	w := httptest.NewRecorder()
	defer func() { recover() }()
	srv.mux.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Error("route /owner/:owner/repo/:repo should be registered")
	}
}

// ============================================================
// Issue metric route tests
// ============================================================

func TestRoute_IssuesNew_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/issues-new", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("issues-new: status = %d, want 400", w.Code)
	}
}

func TestRoute_IssuesClosed_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/issues-closed", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("issues-closed: status = %d, want 400", w.Code)
	}
}

func TestRoute_IssueBacklog_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/issue-backlog", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("issue-backlog: status = %d, want 400", w.Code)
	}
}

func TestRoute_IssueThroughput_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/issue-throughput", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("issue-throughput: status = %d, want 400", w.Code)
	}
}

func TestRoute_AbandonedIssues_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/abandoned-issues", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("abandoned-issues: status = %d, want 400", w.Code)
	}
}

// ============================================================
// PR metric route tests
// ============================================================

func TestRoute_PRsNew_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/pull-requests-new", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("pull-requests-new: status = %d, want 400", w.Code)
	}
}

func TestRoute_ReviewsAccepted_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/reviews-accepted", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("reviews-accepted: status = %d, want 400", w.Code)
	}
}

// ============================================================
// Commit/contributor metric route tests
// ============================================================

func TestRoute_Committers_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/committers", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("committers: status = %d, want 400", w.Code)
	}
}

func TestRoute_Contributors_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/contributors", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("contributors: status = %d, want 400", w.Code)
	}
}

// ============================================================
// Metadata metric route tests
// ============================================================

func TestRoute_Stars_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/stars", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("stars: status = %d, want 400", w.Code)
	}
}

func TestRoute_Forks_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/forks", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("forks: status = %d, want 400", w.Code)
	}
}

func TestRoute_Deps_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/deps", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("deps: status = %d, want 400", w.Code)
	}
}

func TestRoute_Releases_InvalidID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/v1/repos/abc/releases", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("releases: status = %d, want 400", w.Code)
	}
}

// ============================================================
// Parameter parsing tests
// ============================================================

func TestParsePeriod_Valid(t *testing.T) {
	tests := []struct{ input, want string }{
		{"day", "day"},
		{"week", "week"},
		{"month", "month"},
		{"year", "year"},
		{"", "month"},        // default
		{"invalid", "month"}, // default
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/?period="+tt.input, nil)
		got := parsePeriod(r)
		if got != tt.want {
			t.Errorf("parsePeriod(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// The Augur window (stage 1b): day-aligned defaults — the year before
// today's UTC midnight through the end of today — so hi is always a UTC
// midnight and the store's `< hi` is exact.
func TestParseDateRange_Defaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	begin, end, err := parseDateRange(r)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if !begin.Equal(today.AddDate(-1, 0, 0)) && !begin.Equal(today.AddDate(0, 0, -1).AddDate(-1, 0, 0)) { // a test running across midnight
		t.Errorf("default begin = %v, want a year before today's UTC midnight", begin)
	}
	if !end.Equal(today.AddDate(0, 0, 1)) && !end.Equal(today.AddDate(0, 0, 2)) {
		t.Errorf("default end = %v, want the midnight after today (UTC)", end)
	}
}

// A given end_date is inclusive (D4): the bound handed to the store is the
// midnight after it.
func TestParseDateRange_Custom(t *testing.T) {
	r := httptest.NewRequest("GET", "/?begin_date=2023-01-01&end_date=2023-12-31", nil)
	begin, end, err := parseDateRange(r)
	if err != nil {
		t.Fatal(err)
	}
	if !begin.Equal(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("begin = %v, want 2023-01-01", begin)
	}
	if !end.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v, want 2024-01-01 (the midnight after the inclusive 2023-12-31)", end)
	}
}

// D7: a date that does not parse is an error (a 400), never a default.
func TestParseDateRange_InvalidDates(t *testing.T) {
	for _, q := range []string{"begin_date=not-a-date", "end_date=also-bad", "begin_date=2023-02-30", "end_date=2023-1-1"} {
		r := httptest.NewRequest("GET", "/?"+q, nil)
		if _, _, err := parseDateRange(r); err == nil || !strings.Contains(err.Error(), "want YYYY-MM-DD") {
			t.Errorf("?%s: err = %v; want the date refusal", q, err)
		}
	}
}
