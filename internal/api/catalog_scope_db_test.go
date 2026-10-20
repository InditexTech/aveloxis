// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
)

// Stage 1b item 5 (D1, N20, N21) through the handlers: an API token sees
// only its owner's groups in the Augur catalog; a caller without an
// identity sees the fleet; a token whose scope is empty sees [] (never
// the fleet); the owner/name lookup answers the structured 403 naming the
// twin when the name exists only outside the token's scope, and 404 when
// it exists nowhere.
func TestCatalogIsScopedForAPITokens(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := db.NewPostgresStore(ctx, dsn, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	suffix := time.Now().UnixNano()
	owner := fmt.Sprintf("avx-apicat-%d", suffix)
	name := fmt.Sprintf("twin-%d", suffix)
	rg := fmt.Sprintf("avx-apicat-rg-%d", suffix)
	pool := store.Pool()
	var groupID int64
	if err := pool.QueryRow(ctx, `INSERT INTO aveloxis_data.repo_groups (rg_name) VALUES ($1) RETURNING repo_group_id`, rg).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	var gh, gl int64
	for _, r := range []struct {
		host     string
		platform int
		id       *int64
	}{{"github.com", 1, &gh}, {"gitlab.com", 2, &gl}} {
		if err := pool.QueryRow(ctx, `
			INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id, repo_group_id)
			VALUES ($1, $2, $3, $4, $5) RETURNING repo_id`, "https://"+r.host+"/"+owner+"/"+name, owner, name, r.platform, groupID).Scan(r.id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM aveloxis_data.repos WHERE repo_owner = $1`, owner)
		_, _ = pool.Exec(bg, `DELETE FROM aveloxis_data.repo_groups WHERE rg_name = $1`, rg)
	})
	s, err := NewWithOptions(store, logger, Options{})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, info *authInfo) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if info != nil {
			r = r.WithContext(context.WithValue(r.Context(), authCtxKey{}, *info))
		}
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		return w
	}
	ids := func(t *testing.T, w *httptest.ResponseRecorder) map[int64]bool {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var rows []struct {
			ID int64 `json:"repo_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("body %q: %v", w.Body.String(), err)
		}
		out := map[int64]bool{}
		for _, r := range rows {
			out[r.ID] = true
		}
		return out
	}
	groupsOf := func(t *testing.T, w *httptest.ResponseRecorder) map[int64]bool {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var rows []struct {
			ID int64 `json:"repo_group_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("body %q: %v", w.Body.String(), err)
		}
		out := map[int64]bool{}
		for _, r := range rows {
			out[r.ID] = true
		}
		return out
	}
	tokenGL := &authInfo{UserID: 1, APITokenID: 9, Scope: map[int64]bool{gl: true}}
	tokenNone := &authInfo{UserID: 1, APITokenID: 9, Scope: map[int64]bool{}}
	groupPath := fmt.Sprintf("/api/v1/repo-groups/%d/repos", groupID)

	// Anonymous: the fleet.
	if got := ids(t, get("/api/v1/repos", nil)); !got[gh] || !got[gl] {
		t.Errorf("anonymous /repos lacks the twins: %v", got)
	}
	if got := groupsOf(t, get("/api/v1/repo-groups", nil)); !got[groupID] {
		t.Errorf("anonymous /repo-groups lacks the group")
	}
	// A token: its scope.
	if got := ids(t, get("/api/v1/repos", tokenGL)); got[gh] || !got[gl] || len(got) != 1 {
		t.Errorf("token /repos = %v; want exactly the in-scope twin %d", got, gl)
	}
	if got := ids(t, get(groupPath, tokenGL)); got[gh] || !got[gl] {
		t.Errorf("token %s = %v; want only the in-scope twin", groupPath, got)
	}
	if got := groupsOf(t, get("/api/v1/repo-groups", tokenGL)); !got[groupID] || len(got) != 1 {
		t.Errorf("token /repo-groups = %v; want only the group holding its repository (N20)", got)
	}
	// An empty scope is nothing, and nothing is [].
	for _, path := range []string{"/api/v1/repos", "/api/v1/repo-groups", groupPath} {
		if w := get(path, tokenNone); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
			t.Errorf("empty-scope token %s = %d %q; want 200 []", path, w.Code, w.Body.String())
		}
	}
	if w := get("/api/v1/rg-name/"+rg, tokenNone); w.Code != http.StatusNotFound {
		t.Errorf("empty-scope token /rg-name = %d; want 404 (N20)", w.Code)
	}
	// Owner/name: in scope → the in-scope twin; out of scope → 403 naming
	// the twin the unscoped pick would return; absent → 404.
	ownerPath := "/api/v1/owner/" + owner + "/repo/" + strings.ToUpper(name)
	var picked struct {
		ID int64 `json:"repo_id"`
	}
	if w := get(ownerPath, tokenGL); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &picked) != nil || picked.ID != gl {
		t.Errorf("token owner/name = %d %q; want the in-scope twin %d", w.Code, w.Body.String(), gl)
	}
	if w := get(ownerPath, nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &picked) != nil || picked.ID != gh {
		t.Errorf("anonymous owner/name = %d %q; want the first by platform, %d", w.Code, w.Body.String(), gh)
	}
	w := get(ownerPath, tokenNone)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "repo_out_of_scope") || !strings.Contains(w.Body.String(), fmt.Sprint(gh)) {
		t.Errorf("out-of-scope owner/name = %d %q; want the structured 403 naming %d (N21)", w.Code, w.Body.String(), gh)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("the 403 is marked %q; want no-store", cc)
	}
	if w := get("/api/v1/owner/"+owner+"/repo/nobody-"+name, tokenNone); w.Code != http.StatusNotFound {
		t.Errorf("absent owner/name = %d; want 404", w.Code)
	}
	// Group + name: the same, through the group.
	if w := get("/api/v1/rg-name/"+rg+"/repo-name/"+strings.ToUpper(name), tokenGL); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &picked) != nil || picked.ID != gl {
		t.Errorf("token rg-name/repo-name = %d %q; want %d", w.Code, w.Body.String(), gl)
	}
	if w := get("/api/v1/rg-name/"+rg+"/repo-name/"+name, tokenNone); w.Code != http.StatusNotFound {
		t.Errorf("empty-scope rg-name/repo-name = %d; want 404 (the group itself is out of scope, N20)", w.Code)
	}
}
