// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/model"
)

// E4 of summary/59 (stage 3e, 0.29.109): an administrator opens any
// account's group read-only through the service's one access rule
// (GroupAccess Read, logged at INFO by C5's line); the page then offers
// no write — no add forms, no Remove — and a crafted write is refused as
// a stranger's would be (404, N11). A non-administrator still gets 404.
func TestAdministratorOpensAnotherAccountsGroupReadOnly(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	store, err := db.NewPostgresStore(ctx, dsn, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	tag := fmt.Sprintf("_avro%d", time.Now().UnixNano())
	adminID := seedAdminAccount(t, store, tag+"-admin")
	ownerID, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: tag + "-owner", Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: tag + "-other", Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.users WHERE user_id = ANY($1)`, []int{ownerID, otherID})
	})
	groupID, err := store.CreateUserGroup(ctx, ownerID, tag+" group")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.user_groups WHERE group_id = $1`, groupID)
	})
	// One repository in the group, so the Remove link and its confirm page
	// exist to be hidden and refused (round-2 review B-F4: an empty group
	// made both assertions vacuous — the confirm 404 came from the
	// membership check, never from the owner check).
	repoURL := "https://github.com/" + tag + "-owner/" + tag + "-repo"
	repoID, err := store.UpsertRepo(ctx, &model.Repo{Owner: tag + "-owner", Name: tag + "-repo", GitURL: repoURL, Platform: model.PlatformGitHub})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddRepoToGroupByID(ctx, groupID, repoID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.user_repos WHERE group_id = $1`, groupID)
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = $1`, repoID)
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_data.repos WHERE repo_id = $1`, repoID)
	})
	removeConfirm := fmt.Sprintf("/groups/%d/repos/%d/remove", groupID, repoID)
	// A second repository, so the page can be paged at page_size=1 and its
	// pager's links be seen to carry the size (round-1 review of 0.29.110).
	repo2, err := store.UpsertRepo(ctx, &model.Repo{Owner: tag + "-owner", Name: tag + "-repo2", GitURL: repoURL + "2", Platform: model.PlatformGitHub})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddRepoToGroupByID(ctx, groupID, repo2); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = $1`, repo2)
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_data.repos WHERE repo_id = $1`, repo2)
	})
	s := New(store, config.WebConfig{DevMode: true}, "", logger)
	plantSession(t, s, "admin-tok", &Session{UserID: adminID, LoginName: "admin", ExpiresAt: time.Now().Add(time.Hour)})
	plantSession(t, s, "owner-tok", &Session{UserID: ownerID, LoginName: "owner", ExpiresAt: time.Now().Add(time.Hour)})
	plantSession(t, s, "other-tok", &Session{UserID: otherID, LoginName: "other", ExpiresAt: time.Now().Add(time.Hour)})
	h := s.Handler()
	get := func(path, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: tok})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	page := fmt.Sprintf("/groups/%d", groupID)

	w := get(page, "owner-tok")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="/groups/add-repo"`) || !strings.Contains(w.Body.String(), `href="`+removeConfirm+`"`) {
		t.Fatalf("the owner's page = %d; want 200 with the add form and the repository's Remove link", w.Code)
	}
	if w := get(removeConfirm, "owner-tok"); w.Code != http.StatusOK {
		t.Errorf("the owner's remove confirm = %d; want 200", w.Code)
	}
	// A page beyond what an int offset addresses is the last addressable
	// page, not a 500 (whole-branch review: the group page had its own
	// bare Atoi).
	if w := get(page+"?page=92233720368547760", "owner-tok"); w.Code != http.StatusOK {
		t.Errorf("the owner's page at an absurd page number = %d; want 200", w.Code)
	}
	if w := get(page+"?page_size=1", "owner-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `href="`+page+`?page=2&page_size=1">Next</a>`) {
		t.Errorf("the group page at page_size=1 = %d; want its pager's Next carrying page_size=1", w.Code)
	}
	if w := get(page, "other-tok"); w.Code != http.StatusNotFound {
		t.Errorf("another account's page = %d; want 404 (a group's existence is not disclosed)", w.Code)
	}
	w = get(page, "admin-tok")
	if w.Code != http.StatusOK {
		t.Fatalf("the administrator's view = %d; want 200\n%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "read-only") || !strings.Contains(body, tag+" group") {
		t.Error("the administrator's view must say it is read-only and show the group")
	}
	for _, write := range []string{`action="/groups/add-repo"`, `action="/groups/add-org"`, "/remove\"", "/remove'"} {
		if strings.Contains(body, write) {
			t.Errorf("the administrator's read-only view offers a write: %s", write)
		}
	}
	if !strings.Contains(logs.String(), "read another account's group") {
		t.Error("the administrator's read is not logged (C5)")
	}
	// The write routes refuse the administrator like a stranger.
	form := url.Values{"group_id": {fmt.Sprint(groupID)}, "repo_id": {fmt.Sprint(repoID)}, "csrf_token": {"test-csrf-token"}}
	r := httptest.NewRequest(http.MethodPost, "/groups/remove-repo", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: "admin-tok"})
	csrf(r)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("the administrator's remove on another account's group = %d; want 404", w.Code)
	}
	if w := get(removeConfirm, "admin-tok"); w.Code != http.StatusNotFound {
		t.Errorf("the administrator's remove confirm on another account's group = %d; want 404 (the repository IS in the group: the owner check decides)", w.Code)
	}
}
