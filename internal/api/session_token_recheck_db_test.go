// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
)

// TestACachedSessionTokenDiesWhenItsRowIsDeleted (0.29.102 review A1): the
// web GUI's sign-out deletes the Bearer tokens the session minted, and the
// docs promise a 401 on the GUI's next request. The authenticator caches a
// resolved identity for authCacheTTL; a session token is therefore
// rechecked against its row on every call (one PK read), as an API token
// already was — a deleted row answers 401 at once, not after the TTL.
func TestACachedSessionTokenDiesWhenItsRowIsDeleted(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	store, err := db.NewPostgresStore(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	const login = "_avapi_cached_session_recheck"
	_, _ = store.Pool().Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	uid, err := store.UpsertOAuthUser(ctx, db.OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.users WHERE user_id = $1`, uid)
	})
	tok, err := store.CreateSessionToken(ctx, uid, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a := newAuthenticator(store, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if info, err := a.resolveToken(ctx, tok); err != nil || info.UserID != uid {
		t.Fatalf("first resolve = (%+v, %v)", info, err)
	}
	if err := store.DeleteSessionToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := a.resolveToken(ctx, tok); !errors.Is(err, errInvalidToken) {
		t.Errorf("after the row was deleted the cached token still resolves: %v; want errInvalidToken at once", err)
	}
}
