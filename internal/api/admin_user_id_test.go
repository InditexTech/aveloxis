// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAdminUserIDsOutsideInt4Are400BeforeTheStore (0.29.103 review F2):
// users.user_id is int4; a wider number used to reach pgx's parameter
// encoder as a 500. Every admin route that names an account by number
// answers 400 before any store read — the Server has no store here, so a
// handler that reached it would panic (reported as "reached").
func TestAdminUserIDsOutsideInt4Are400BeforeTheStore(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.admin = testAdminService(s)
	admin := context.WithValue(context.Background(), authCtxKey{}, authInfo{UserID: 1, IsAdmin: true})
	for _, tc := range []struct {
		name string
		req  *http.Request
		call func(w http.ResponseWriter, r *http.Request)
	}{
		{"set admin", httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/4294967296/admin", strings.NewReader(`{"admin": true}`)), s.handleAdminSetUserAdmin},
		{"capacity override", httptest.NewRequest(http.MethodPost, "/api/v1/admin/capacity/accounts/4294967296", strings.NewReader(`{"note": "x"}`)), s.handleAdminCapacityOverride},
		{"capacity account", httptest.NewRequest(http.MethodGet, "/api/v1/admin/capacity/account?user_id=4294967296", nil), s.handleAdminCapacityAccount},
	} {
		r := tc.req.WithContext(admin)
		r.SetPathValue("userID", "4294967296")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		reached := false
		func() {
			defer func() {
				if recover() != nil {
					reached = true
				}
			}()
			tc.call(w, r)
		}()
		if reached || w.Code != http.StatusBadRequest {
			t.Errorf("%s: id 4294967296 = %d (store reached=%v) %q; want 400 before the store", tc.name, w.Code, reached, strings.TrimSpace(w.Body.String()))
		}
	}
}
