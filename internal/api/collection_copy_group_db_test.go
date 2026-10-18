// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/admin"
	"github.com/aveloxis/aveloxis/internal/db"
)

// TestCollectionCopyGroupNameGoesThroughTheService (AVELOXIS_TEST_DB;
// round-2 review of 0.29.100): the copy's caller-named destination group is
// created by the admin service, so its name bound applies — a name over
// admin.MaxGroupNameLength is a 400 and nothing is created — and an
// accepted name creates the group (reused when it exists) and copies.
func TestCollectionCopyGroupNameGoesThroughTheService(t *testing.T) {
	store := approveRefusalStore(t)
	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	login := "_avapi_copy_group" + suffix
	t.Cleanup(func() {
		p := store.Pool()
		_, _ = p.Exec(context.Background(), `DELETE FROM aveloxis_ops.user_repos WHERE group_id IN (SELECT group_id FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1))`, login)
		_, _ = p.Exec(context.Background(), `DELETE FROM aveloxis_ops.user_groups WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = p.Exec(context.Background(), `DELETE FROM aveloxis_ops.collections WHERE created_by IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = $1)`, login)
		_, _ = p.Exec(context.Background(), `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	})
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	var collID int64
	if err := store.Pool().QueryRow(ctx, `INSERT INTO aveloxis_ops.collections (name, description, created_by) VALUES ($1, 'copy probe', $2) RETURNING collection_id`, "_avapi_copy_coll"+suffix, uid).Scan(&collID); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	s := &Server{store: store, logger: slog.New(slog.NewTextHandler(logs, nil)), auth: newAuthenticator(store, false, nil)}
	s.admin = testAdminService(s)
	copyTo := func(name string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"group_name": name})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/collections/x/copy", strings.NewReader(string(body)))
		r.SetPathValue("collectionID", strconv.FormatInt(collID, 10))
		r = r.WithContext(context.WithValue(r.Context(), authCtxKey{}, authInfo{UserID: uid}))
		w := httptest.NewRecorder()
		s.handleCollectionCopy(w, r)
		return w
	}
	groups := func(name string) int {
		var n int
		if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM aveloxis_ops.user_groups WHERE user_id = $1 AND name = $2`, uid, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	long := strings.Repeat("g", admin.MaxGroupNameLength+1)
	if w := copyTo(long); w.Code != http.StatusBadRequest || groups(long) != 0 {
		t.Errorf("a %d-character group name = %d %q with %d groups created; want 400 and none", len(long), w.Code, strings.TrimSpace(w.Body.String()), groups(long))
	}
	name := "copy target " + suffix
	for i := 0; i < 2; i++ { // the second copy reuses the group
		if w := copyTo(name); w.Code != http.StatusOK {
			t.Fatalf("copy %d = %d %q; want 200", i, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	if n := groups(name); n != 1 {
		t.Errorf("groups named %q = %d; want one, reused by the second copy", name, n)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a refusal was logged as a server error:\n%s", logs.String())
	}
}
