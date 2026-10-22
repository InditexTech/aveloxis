// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/aveloxis/aveloxis/internal/capacity"
)

// Stage 3b of summary/59 (0.29.109) moved the token, capacity and
// collection rules out of these handlers into the admin service with the
// promise that every refusal answers as before — status, content type and
// body bytes. Round-1 review F1 found the one branch where that was not
// true (the admin-owned group refusal came back as JSON), and that no test
// could have caught it: this golden makes the promise executable. The
// bodies below are what v0.29.108 answered (`git show`); a change to one
// is a change for API clients and goes in the release notes.
func TestMovedAdminRefusalsAnswerAsBefore(t *testing.T) {
	s, store, admin, owner := apiTokenAdminServer(t)
	ctx := context.Background()
	collID, err := store.CreateCollection(ctx, "avx-it-refusal-bytes", "", 0, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteCollection(context.Background(), collID) })
	userGroup, err := store.CreateUserGroup(ctx, owner, "avx-it-refusal-bytes-group")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.user_groups WHERE group_id = $1`, userGroup)
	})
	known := capacity.QuotaNames()[0]
	const jsonType, textType = "application/json", "text/plain; charset=utf-8"
	jsonBody := func(msg string) string { return `{"error":"` + msg + `"}` + "\n" }
	textBody := func(msg string) string { return msg + "\n" }

	cases := []struct {
		name   string
		method string
		path   string
		body   any
		status int
		ctype  string
		want   string
	}{
		{"grant: user_id 0", http.MethodPost, "/api/v1/admin/api-tokens", map[string]any{"user_id": 0, "label": "x"}, 400, jsonType, jsonBody("user_id names the account the token authenticates as")},
		{"grant: empty label", http.MethodPost, "/api/v1/admin/api-tokens", map[string]any{"user_id": owner, "label": " "}, 400, jsonType, jsonBody("label is required (who the token is for, or what it is used by)")},
		{"grant: lifetime over a year", http.MethodPost, "/api/v1/admin/api-tokens", map[string]any{"user_id": owner, "label": "x", "lifetime_days": 366}, 400, jsonType, jsonBody("lifetime_days must be a positive number of days, at most 365")},
		{"grant: unknown account", http.MethodPost, "/api/v1/admin/api-tokens", map[string]any{"user_id": 2147483647, "label": "x"}, 400, jsonType, jsonBody("no account with that user_id")},
		{"revoke: id 0", http.MethodPost, "/api/v1/admin/api-tokens/0/revoke", nil, 400, jsonType, jsonBody("token id must be a positive integer")},
		{"revoke: unknown token", http.MethodPost, "/api/v1/admin/api-tokens/999999999/revoke", nil, 404, jsonType, jsonBody("no API token with that id")},
		{"defaults: hourly missing", http.MethodPost, "/api/v1/admin/api-token-settings", map[string]any{"default_lifetime_days": 30}, 400, jsonType, jsonBody("default_rate_limit_per_hour must be a positive number, at most 2147483647")},
		{"quota: unknown name", http.MethodPost, "/api/v1/admin/capacity/quotas/nope", map[string]any{"allowed": 1, "mode": "off"}, 404, jsonType, jsonBody(`no quota named \"nope\"`)},
		{"quota: zero", http.MethodPost, "/api/v1/admin/capacity/quotas/" + known, map[string]any{"allowed": 0, "mode": "off"}, 400, jsonType, jsonBody("allowed must be a positive number, at most 2147483647, and mode one of enforce, shadow, off")},
		{"contact: not an address", http.MethodPost, "/api/v1/admin/capacity/contact", map[string]any{"contact_email": "nope"}, 400, jsonType, jsonBody("contact_email must be an email address of at most 254 bytes, or empty")},
		{"override: unknown account", http.MethodPost, "/api/v1/admin/capacity/accounts/2147483647", map[string]any{"repos_allowed": 1}, 404, jsonType, jsonBody("no account with that user id")},
		{"override: bad value", http.MethodPost, fmt.Sprintf("/api/v1/admin/capacity/accounts/%d", owner), map[string]any{"repos_allowed": 0}, 400, jsonType, jsonBody("each value must be a positive number (or null for the quota's value); the note is at most 500 characters")},
		{"allowlist add: bad cidr", http.MethodPost, "/api/v1/admin/capacity/signup-allowlist", map[string]any{"cidr": "nope"}, 400, jsonType, jsonBody("cidr must be a network in CIDR form (such as 192.0.2.0/24); the note is at most 500 characters")},
		{"allowlist remove: bad cidr", http.MethodPost, "/api/v1/admin/capacity/signup-allowlist/remove", map[string]any{"cidr": "nope"}, 400, jsonType, jsonBody("cidr must be a network in CIDR form")},
		{"allowlist remove: absent", http.MethodPost, "/api/v1/admin/capacity/signup-allowlist/remove", map[string]any{"cidr": "203.0.113.0/24"}, 404, jsonType, jsonBody("that network is not on the allowlist")},
		{"collection create: no name", http.MethodPost, "/api/v1/admin/collections", map[string]any{"name": ""}, 400, textType, textBody("name required")},
		{"collection create: taken name", http.MethodPost, "/api/v1/admin/collections", map[string]any{"name": "avx-it-refusal-bytes"}, 409, jsonType, jsonBody("a collection with that name already exists")},
		// A client change (whole-branch review of 0.29.109): the int4 column's
		// range is the service's rule; before, the encoder's refusal was a 500.
		{"collection create: position over int4", http.MethodPost, "/api/v1/admin/collections", map[string]any{"name": "avx-it-refusal-pos", "position": 2147483648}, 400, textType, textBody("position must be between -2147483648 and 2147483647")},
		{"collection update: unknown", http.MethodPost, "/api/v1/admin/collections/999999999", map[string]any{"name": "x"}, 404, jsonType, jsonBody("no collection with that id")},
		{"collection update: bad id", http.MethodPost, "/api/v1/admin/collections/x", map[string]any{"name": "x"}, 400, textType, textBody("invalid collection_id")},
		// Zero and negative ids reached the store before the move, whose
		// RowsAffected()==0 arm answered this 404 (round-2 review A-5).
		{"collection update: id 0", http.MethodPost, "/api/v1/admin/collections/0", map[string]any{"name": "x"}, 404, jsonType, jsonBody("no collection with that id")},
		{"collection delete: id -1", http.MethodPost, "/api/v1/admin/collections/-1/delete", nil, 404, jsonType, jsonBody("no collection with that id")},
		{"add group: collection 0", http.MethodPost, "/api/v1/admin/collections/0/groups", map[string]any{"group_id": 1}, 404, jsonType, jsonBody("no collection with that id")},
		{"add group: group 0", http.MethodPost, fmt.Sprintf("/api/v1/admin/collections/%d/groups", collID), map[string]any{"group_id": 0}, 400, textType, textBody("group_id required")},
		{"add group: unknown group", http.MethodPost, fmt.Sprintf("/api/v1/admin/collections/%d/groups", collID), map[string]any{"group_id": 999999999}, 404, jsonType, jsonBody("no group with that id")},
		{"add group: not admin-owned", http.MethodPost, fmt.Sprintf("/api/v1/admin/collections/%d/groups", collID), map[string]any{"group_id": userGroup}, 400, textType, textBody("collection member groups must be admin-owned")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := adminCall(t, s, c.method, c.path, c.body, admin, true)
			if w.Code != c.status || w.Header().Get("Content-Type") != c.ctype || w.Body.String() != c.want {
				t.Errorf("%s %s = %d %q %q; want %d %q %q", c.method, c.path, w.Code, w.Header().Get("Content-Type"), w.Body.String(), c.status, c.ctype, c.want)
			}
		})
	}
}
