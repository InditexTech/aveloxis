// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// N23 (stage 1b): an entry of ?ids= that is not a repository id is a 400,
// not skipped (a D7-class silent fallback: a client's typo read as a
// shorter list). Unknown and out-of-scope ids stay left out of the answer.
func TestRepoStatsBatchRefusesAnUnparseableID(t *testing.T) {
	srv := newTestServer()
	for _, ids := range []string{"1,abc", "1,,2", "0", "-1", "1,2.5"} {
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/repos/stats?ids="+ids, nil))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid repo_id") {
			t.Errorf("ids=%q = %d %q; want 400 naming the invalid entry", ids, w.Code, w.Body.String())
		}
	}
}
