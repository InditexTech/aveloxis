// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/platform"
	"github.com/aveloxis/aveloxis/internal/srctest"
)

// Copilot finding on PR #179 (2026-08-18), verified real as a LATENT
// hazard: handleAddOrg (web) and handleGroupAddRepo kind:"org" (api) pass
// raw trimmed user input to AddOrgToGroup, and platform.ParseOrgURL
// TOLERATES schemeless input — so "github.com/foo" produced a fully
// WORKING registration (org_name/platform parse fine, enumeration scans
// it) while the stored org_url stayed schemeless. That silently defeats
// every exact/prefix org_url matcher: ReconcileOrgRepoLinks
// (starts_with against https:// repo_git values), the v0.19.3
// GetUserGroupIDsForOrgURL bridge, and the v0.27.84
// IsOrgRegisteredAnywhere dedup (schemeless and schemed registrations of
// the SAME org would count as different orgs). Production had zero
// schemeless rows on 2026-08-18, so the fix is the store choke point all
// four callers route through — no migration.

// TestAddOrgToGroupCanonicalizesScheme pins the normalization shape: the
// scheme-prepend lives in CanonicalOrgURL, the ONE spelling of an org URL
// (v0.29.68, worklist follow-up 3 added the lowercase there), and
// AddOrgToGroup applies it BEFORE the registered-anywhere check, the
// add-request write and the user_org_requests INSERT see the value.
func TestAddOrgToGroupCanonicalizesScheme(t *testing.T) {
	// NOTE: cannot comment-strip here — stripLineComments is naive and
	// would truncate the needle line at the "//" INSIDE the "https://"
	// string literal. The ordering anchor uses the CALL-site forms, which
	// prose comments never contain.
	src := srctest.Read(t, "internal/db/web_store.go")
	canon := srctest.FuncBody(t, src, "func CanonicalOrgURL(")
	pre := srctest.FuncBody(t, src, "func orgURLTrimmedSchemed(")
	if !strings.Contains(pre, `platform.WithDefaultScheme(`) || !strings.Contains(canon, "strings.ToLower(orgURLTrimmedSchemed(") {
		t.Fatal("CanonicalOrgURL must prepend https:// to a schemeless org URL and lowercase it — a schemeless " +
			"or differently-cased org_url row registers and scans fine but silently defeats ReconcileOrgRepoLinks, " +
			"GetUserGroupIDsForOrgURL and the (group_id, org_url) unique key")
	}
	body := extractFunctionBody(t, "web_store.go", "AddOrgToGroup")
	canonIdx := strings.Index(body, "CanonicalOrgURL(orgURL)")
	regIdx := strings.Index(body, "s.IsOrgRegisteredAnywhere(")
	if canonIdx < 0 || regIdx < 0 || regIdx < canonIdx {
		t.Error("AddOrgToGroup must canonicalize the org URL (CanonicalOrgURL) BEFORE the " +
			"s.IsOrgRegisteredAnywhere( consult — otherwise github.com/foo and https://github.com/foo " +
			"register as different orgs")
	}
}

// TestAddOrgToGroupCanonicalizesSchemeEndToEnd proves the stored row is
// canonical when the caller supplies schemeless input.
func TestAddOrgToGroupCanonicalizesSchemeEndToEnd(t *testing.T) {
	store, ctx := v0251Connect(t)
	t.Cleanup(store.pool.Close)

	cleanup := func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_org_requests
			WHERE org_url LIKE '%avtest-schemeless-org%'`)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.collection_add_requests
			WHERE user_id IN (SELECT user_id FROM aveloxis_ops.users WHERE login_name = 'avtest-schemeless-admin')`)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.user_groups WHERE name = 'avtest-schemeless-group'`)
		_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = 'avtest-schemeless-admin'`)
	}
	cleanup()
	t.Cleanup(cleanup)

	// Admin user so the registration bypasses the pending-approval arm and
	// lands directly in user_org_requests.
	var userID int
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO aveloxis_ops.users (login_name, admin) VALUES ('avtest-schemeless-admin', TRUE)
		RETURNING user_id`).Scan(&userID); err != nil {
		t.Fatalf("seed admin user: %v", err)
	}
	var groupID int64
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO aveloxis_ops.user_groups (user_id, name, status)
		VALUES ($1, 'avtest-schemeless-group', 'approved') RETURNING group_id`, userID).Scan(&groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}

	out, err := store.AddOrgToGroup(ctx, userID, groupID, "github.com/avtest-schemeless-org", "", "")
	if err != nil {
		t.Fatalf("AddOrgToGroup: %v", err)
	}
	if !out.Registered {
		t.Fatal("admin org add must register immediately")
	}

	var stored string
	if err := store.pool.QueryRow(ctx, `
		SELECT org_url FROM aveloxis_ops.user_org_requests
		WHERE group_id = $1`, groupID).Scan(&stored); err != nil {
		t.Fatalf("read stored org_url: %v", err)
	}
	if stored != "https://github.com/avtest-schemeless-org" {
		t.Errorf("stored org_url = %q, want the canonical %q — schemeless "+
			"storage silently defeats every org_url prefix/equality matcher",
			stored, "https://github.com/avtest-schemeless-org")
	}
}

// TestAnOrgURLNoParserAcceptsIsRefusedAtRegistration (0.29.106 review
// round 1): once ParseOrgURL refused a non-http(s) scheme, the registration
// gate's "a URL that does not parse is left to later checks" arm let
// `ftp://github.com/chaoss` register with an empty org_name — audited
// "registered", never counted by the demand probe, logged as off-host by
// every 4 h pass, stamped by none. It is the caller's input, refused here.
func TestAnOrgURLNoParserAcceptsIsRefusedAtRegistration(t *testing.T) {
	dsn := os.Getenv("AVELOXIS_TEST_DB")
	if dsn == "" {
		t.Skip("AVELOXIS_TEST_DB not set")
	}
	ctx := context.Background()
	store, err := NewPostgresStore(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	const login = "_avtest_badscheme_org"
	_, _ = store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.users WHERE login_name = $1`, login)
	userID, err := store.UpsertOAuthUser(ctx, OAuthUserInfo{Login: login, Provider: "github"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = store.pool.Exec(c, `DELETE FROM aveloxis_ops.user_org_requests WHERE user_id = $1`, userID)
		_, _ = store.pool.Exec(c, `DELETE FROM aveloxis_ops.user_groups WHERE user_id = $1`, userID)
		_, _ = store.pool.Exec(c, `DELETE FROM aveloxis_ops.users WHERE user_id = $1`, userID)
	})
	var groupID int64
	if err := store.pool.QueryRow(ctx, `INSERT INTO aveloxis_ops.user_groups (user_id, name, status)
		VALUES ($1, 'avtest-badscheme-group', 'approved') RETURNING group_id`, userID).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]error{
		"ftp://github.com/chaoss":      ErrInvalidOrgURL,
		"ftp://gitlab.com/gnome":       ErrInvalidOrgURL,
		"ssh://git@github.com/chaoss":  platform.ErrURLUserinfo, // the credentials rule answers first (0.29.57)
		"ftp://github.com/chaoss/repo": ErrInvalidOrgURL,
	} {
		out, err := store.AddOrgToGroup(ctx, userID, groupID, raw, "", "")
		if !errors.Is(err, want) || out.Registered {
			t.Errorf("AddOrgToGroup(%q) = (%+v, %v); want %v and nothing registered", raw, out, err, want)
		}
	}
	var n int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM aveloxis_ops.user_org_requests WHERE user_id = $1`, userID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d org registration(s) exist for the refused URLs (%v); want 0", n, err)
	}
}
