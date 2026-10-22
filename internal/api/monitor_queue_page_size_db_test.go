// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The queue route's page_size is documented as "at most 100" (0.29.109):
// this drives the cap through the handler, since a pin on the parse call
// alone let a mutant raise the bound (round-2 review A-4).
func TestAdminMonitorQueuePageSizeIsCappedAtOneHundred(t *testing.T) {
	s, _, admin, _ := apiTokenAdminServer(t)
	for _, c := range []struct {
		query    string
		pageSize int
	}{
		{"", 100},
		{"?page_size=1000", 100},
		{"?page_size=7", 7},
		{"?page_size=0", 100},
		{"?page_size=-3", 100},
		{"?page_size=x", 100},
	} {
		w := adminCall(t, s, http.MethodGet, "/api/v1/admin/monitor/queue"+c.query, nil, admin, true)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d\n%s", c.query, w.Code, w.Body.String())
		}
		var env struct {
			Jobs     []json.RawMessage `json:"jobs"`
			PageSize int               `json:"page_size"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if env.PageSize != c.pageSize || len(env.Jobs) > c.pageSize {
			t.Errorf("%q: page_size %d with %d rows; want %d and at most that many rows", c.query, env.PageSize, len(env.Jobs), c.pageSize)
		}
	}
}
