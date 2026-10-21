// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/admin"
	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/db"
)

// TestRepositoryPageAndSBOMRequireMembership (0.29.102, stage 2g of
// summary/59, B5/D-8 — PR #218 C16's recorded SBOM decision reversed in
// stage 1 by the service's one access rule, WEB-15): the repository page,
// the SBOM download and the remove confirmation answer 404 for a
// repository the group does not hold, even to the group's owner, and serve
// the one it holds. Pinned here through the real handlers.
func TestRepositoryPageAndSBOMRequireMembership(t *testing.T) {
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
	pool := store.Pool()
	login := fmt.Sprintf("_avweb_member_%d", time.Now().UnixNano())
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	var inRepo, outRepo int64
	for _, pair := range []struct {
		name string
		id   *int64
	}{{"in", &inRepo}, {"out", &outRepo}} {
		if err := pool.QueryRow(ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id, repo_group_id)
			VALUES ($1, $2, $3, 1, 1) RETURNING repo_id`, "https://github.com/"+login+"/"+pair.name, login, pair.name).Scan(pair.id); err != nil {
			t.Fatal(err)
		}
	}
	s := New(store, config.WebConfig{DevMode: true}, "", logger)
	gid, err := s.admin.CreateGroup(ctx, admin.Actor{UserID: uid, Kind: admin.Session}, "membership")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO aveloxis_ops.user_repos (group_id, repo_id) VALUES ($1, $2)`, gid, inRepo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM aveloxis_ops.user_repos WHERE group_id = $1`, gid)
		_, _ = pool.Exec(c, `DELETE FROM aveloxis_ops.user_groups WHERE group_id = $1`, gid)
		_, _ = pool.Exec(c, `DELETE FROM aveloxis_data.repos WHERE repo_id IN ($1, $2)`, inRepo, outRepo)
		_, _ = pool.Exec(c, `DELETE FROM aveloxis_ops.users WHERE user_id = $1`, uid)
	})
	token := plantSession(t, s, "member", &Session{UserID: uid, LoginName: login, ExpiresAt: time.Now().Add(time.Hour)})
	h := s.Handler()
	get := func(path string) int {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: s.cookieName("aveloxis_session"), Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, suffix := range []string{"", "/sbom?format=cyclonedx", "/remove"} {
		if code := get(fmt.Sprintf("/groups/%d/repos/%d%s", gid, outRepo, suffix)); code != http.StatusNotFound {
			t.Errorf("a repository the group does not hold: GET …/repos/%d%s = %d; want 404 (the owner's group, the wrong repository)", outRepo, suffix, code)
		}
		if code := get(fmt.Sprintf("/groups/%d/repos/%d%s", gid, inRepo, suffix)); code == http.StatusNotFound || code == http.StatusForbidden {
			t.Errorf("the group's own repository: GET …/repos/%d%s = %d; want it served", inRepo, suffix, code)
		}
	}
}
