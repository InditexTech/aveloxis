// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/db"
)

// Stage 3c of summary/59 (0.29.109): the API-tokens, Capacity and
// Collections pages, each a template plus thin handlers over the service.
// These tests drive every form through the real Handler (the CSRF guard,
// the admin gate, the service, the store) and read the result back from
// the store.

func TestAPITokensPageGrantsRevokesAndSetsDefaults(t *testing.T) {
	f := newConsoleFixture(t)
	ctx := context.Background()
	if w := f.get("/admin/api-tokens", "user-tok"); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin's GET /admin/api-tokens = %d; want 403", w.Code)
	}
	w := f.get("/admin/api-tokens", "admin-tok")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Grant a token") {
		t.Fatalf("GET /admin/api-tokens = %d\n%s", w.Code, w.Body.String())
	}

	// The grant answers the page that shows the token once, never a
	// redirect, and never cached.
	w = f.post("/admin/api-tokens", "admin-tok", url.Values{"user_id": {strconv.Itoa(f.userID)}, "label": {"reporting job"}, "lifetime_days": {"7"}})
	if w.Code != http.StatusOK {
		t.Fatalf("grant = %d\n%s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("the token page's Cache-Control = %q; want no-store", cc)
	}
	tokens, err := f.store.ListAPITokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var granted *db.APIToken
	for i := range tokens {
		if tokens[i].Label == "reporting job" && tokens[i].UserID == f.userID {
			granted = &tokens[i]
		}
	}
	if granted == nil {
		t.Fatal("the grant stored no token for the account")
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.api_tokens WHERE token_id = $1`, granted.TokenID)
	})
	if n := strings.Count(w.Body.String(), db.APITokenPrefix); n != 1 {
		t.Errorf("the token page shows the token prefix %d times; want once (the raw token, shown once)", n)
	}
	if lifetime := granted.ExpiresAt.Sub(granted.CreatedAt); lifetime < 6*24*time.Hour || lifetime > 8*24*time.Hour {
		t.Errorf("lifetime %v; want the form's 7 days", lifetime)
	}
	// A refused grant is the service's words, not a 500.
	if w := f.post("/admin/api-tokens", "admin-tok", url.Values{"user_id": {strconv.Itoa(f.userID)}, "label": {" "}}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "label is required") {
		t.Errorf("grant without a label = %d %q; want 400 with the service's rule", w.Code, strings.TrimSpace(w.Body.String()))
	}
	// The form's user_id goes through the one users.user_id rule (0.29.103;
	// round-1 review of 0.29.109, F4): zero and an int8 are refused before
	// the service, as the API refuses them.
	for _, id := range []string{"0", "2147483648", "x"} {
		if w := f.post("/admin/api-tokens", "admin-tok", url.Values{"user_id": {id}, "label": {"x"}}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "user_id must be a positive integer") {
			t.Errorf("grant for user_id %q = %d %q; want 400 from the one user-id rule", id, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}

	page := f.get("/admin/api-tokens", "admin-tok").Body.String()
	revoke := fmt.Sprintf("/admin/api-tokens/%d/revoke", granted.TokenID)
	if !strings.Contains(page, "reporting job") || !strings.Contains(page, `href="`+revoke+`"`) {
		t.Error("the tokens page does not list the granted token with its revoke link")
	}
	if w := f.get(revoke, "admin-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="`+revoke+`"`) {
		t.Errorf("the revoke confirm page = %d; want 200 with the form", w.Code)
	}
	if w := f.post(revoke, "admin-tok", url.Values{}); w.Code != http.StatusFound {
		t.Errorf("revoke = %d; want 302\n%s", w.Code, w.Body.String())
	}
	tokens, _ = f.store.ListAPITokens(ctx)
	for _, tok := range tokens {
		if tok.TokenID == granted.TokenID && tok.RevokedAt == nil {
			t.Error("the token is not revoked")
		}
	}

	before, err := f.store.GetAPITokenSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.SetAPITokenSettings(context.Background(), before, f.adminID) })
	if w := f.post("/admin/api-token-settings", "admin-tok", url.Values{"default_rate_limit_per_hour": {"555"}, "default_rate_limit_per_day": {"5555"}, "default_lifetime_days": {"11"}}); w.Code != http.StatusFound {
		t.Errorf("defaults = %d; want 302\n%s", w.Code, w.Body.String())
	}
	after, _ := f.store.GetAPITokenSettings(ctx)
	if after.DefaultRateLimitPerHour != 555 || after.DefaultRateLimitPerDay != 5555 || after.DefaultLifetimeDays != 11 {
		t.Errorf("defaults after the form: %+v", after)
	}
}

func TestCapacityPageSavesQuotaContactOverrideAndAllowlist(t *testing.T) {
	f := newConsoleFixture(t)
	ctx := context.Background()
	if w := f.get("/admin/capacity", "user-tok"); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin's GET /admin/capacity = %d; want 403", w.Code)
	}
	w := f.get("/admin/capacity", "admin-tok")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Quotas") || !strings.Contains(w.Body.String(), "Sign-up allowlist") {
		t.Fatalf("GET /admin/capacity = %d\n%s", w.Code, w.Body.String())
	}

	name := capacity.QuotaNames()[0]
	quotas, err := f.store.EffectiveCapacityQuotas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	was := quotas[name]
	t.Cleanup(func() { _ = f.store.SetCapacityQuota(context.Background(), name, was.Allowed, was.Mode, f.adminID) })
	if w := f.post("/admin/capacity/quotas/"+name, "admin-tok", url.Values{"allowed": {"77"}, "mode": {"shadow"}}); w.Code != http.StatusFound {
		t.Errorf("quota save = %d\n%s", w.Code, w.Body.String())
	}
	quotas, _ = f.store.EffectiveCapacityQuotas(ctx)
	if q := quotas[name]; q.Allowed != 77 || q.Mode != capacity.Shadow {
		t.Errorf("quota after the form: %+v", q)
	}
	// The page marks the quota's own mode (round-1 review of 0.29.109, F1:
	// no option was selected, so saving a value alone flipped the mode).
	if w := f.get("/admin/capacity", "admin-tok"); !strings.Contains(w.Body.String(), `<option value="shadow" selected>`) {
		t.Error("the quotas table does not select the saved mode; a value-only save would change the mode")
	}
	// The lookup echoes only what parsed (round-2 review B-F1): an
	// unbounded user_id or an over-long login is refused and never carried
	// back into the page.
	junk := strings.Repeat("x", 600)
	for _, q := range []string{"user_id=" + junk, "user_id=0", "login=" + junk} {
		w := f.get("/admin/capacity?"+q, "admin-tok")
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), junk) || strings.Contains(w.Body.String(), strings.Repeat("x", 255)) {
			t.Errorf("GET /admin/capacity?%s… = %d; want 200 without the value echoed", q[:12], w.Code)
		}
		if !strings.Contains(w.Body.String(), "user_id must be a positive integer") && !strings.Contains(w.Body.String(), "no such account") {
			t.Errorf("GET /admin/capacity?%s… does not say why there is no account", q[:12])
		}
	}
	if w := f.get("/admin/capacity?user_id="+strconv.Itoa(f.userID), "admin-tok"); !strings.Contains(w.Body.String(), "id "+strconv.Itoa(f.userID)+" ") || strings.Contains(w.Body.String(), `name="login" value="`+strconv.Itoa(f.userID)+`"`) {
		t.Error("a parsed user_id shows the account it found and never fills the login field (round-3: the next Look up would search the login \"7\")")
	}
	// The allowlist confirm page carries the normalised network, or refuses.
	if w := f.get("/admin/capacity/signup-allowlist/remove?cidr=nope", "admin-tok"); w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "nope") {
		t.Errorf("confirm for a non-network = %d; want 400 without the value", w.Code)
	}
	if w := f.get("/admin/capacity/signup-allowlist/remove?cidr=203.0.113.7/24", "admin-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `value="203.0.113.0/24"`) {
		t.Errorf("confirm for a network = %d; want 200 carrying the masked network", w.Code)
	}
	if w := f.post("/admin/capacity/quotas/"+name, "admin-tok", url.Values{"allowed": {"0"}, "mode": {"shadow"}}); w.Code != http.StatusBadRequest {
		t.Errorf("quota 0 = %d; want 400", w.Code)
	}
	if w := f.post("/admin/capacity/quotas/nope", "admin-tok", url.Values{"allowed": {"1"}, "mode": {"off"}}); w.Code != http.StatusNotFound {
		t.Errorf("unknown quota = %d; want 404", w.Code)
	}

	contactWas, _ := f.store.GetCapacityContact(ctx)
	t.Cleanup(func() { _ = f.store.SetCapacityContact(context.Background(), contactWas, f.adminID) })
	if w := f.post("/admin/capacity/contact", "admin-tok", url.Values{"contact_email": {"ops@example.org"}}); w.Code != http.StatusFound {
		t.Errorf("contact save = %d\n%s", w.Code, w.Body.String())
	}
	if got, _ := f.store.GetCapacityContact(ctx); got != "ops@example.org" {
		t.Errorf("contact after the form = %q", got)
	}

	acct, err := f.store.CapacityAccountByID(ctx, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	w = f.get("/admin/capacity?login="+url.QueryEscape(acct.Login), "admin-tok")
	override := fmt.Sprintf("/admin/capacity/accounts/%d", f.userID)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="`+override+`"`) {
		t.Fatalf("account lookup = %d; want 200 with the override form\n%s", w.Code, w.Body.String())
	}
	// A padded login is that login (one normaliser, round-3 review) and is
	// echoed trimmed.
	if w := f.get("/admin/capacity?login=%20%20"+url.QueryEscape(acct.Login)+"%20", "admin-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="`+override+`"`) || !strings.Contains(w.Body.String(), `name="login" value="`+acct.Login+`"`) {
		t.Errorf("a padded login = %d; want the account found and the login echoed trimmed", w.Code)
	}
	t.Cleanup(func() {
		_ = f.store.SetCapacityOverride(context.Background(), db.CapacityOverride{UserID: f.userID}, f.adminID)
	})
	if w := f.post(override, "admin-tok", url.Values{"repos_allowed": {"3"}, "note": {"pilot"}}); w.Code != http.StatusFound {
		t.Errorf("override save = %d\n%s", w.Code, w.Body.String())
	}
	acct, _ = f.store.CapacityAccountByID(ctx, f.userID)
	if acct.Override.ReposAllowed == nil || *acct.Override.ReposAllowed != 3 || acct.Override.Note != "pilot" {
		t.Errorf("override after the form: %+v", acct.Override)
	}
	if w := f.get(override+"/clear", "admin-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="`+override+`/clear"`) {
		t.Errorf("clear confirm = %d", w.Code)
	}
	if w := f.post(override+"/clear", "admin-tok", url.Values{}); w.Code != http.StatusFound {
		t.Errorf("clear = %d\n%s", w.Code, w.Body.String())
	}
	acct, _ = f.store.CapacityAccountByID(ctx, f.userID)
	if acct.Override.ReposAllowed != nil {
		t.Errorf("override after the clear: %+v", acct.Override)
	}

	cidr := "192.0.2.0/24"
	t.Cleanup(func() { _, _ = f.store.RemoveSignupAllowlist(context.Background(), cidr) })
	if w := f.post("/admin/capacity/signup-allowlist", "admin-tok", url.Values{"cidr": {cidr}, "note": {"office"}}); w.Code != http.StatusFound {
		t.Errorf("allowlist add = %d\n%s", w.Code, w.Body.String())
	}
	listed := func() bool {
		entries, _ := f.store.ListSignupAllowlist(ctx)
		for _, e := range entries {
			if e.CIDR == cidr {
				return true
			}
		}
		return false
	}
	if !listed() {
		t.Fatal("the network is not on the allowlist after the form")
	}
	if w := f.get("/admin/capacity/signup-allowlist/remove?cidr="+url.QueryEscape(cidr), "admin-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="cidr" value="`+cidr+`"`) {
		t.Errorf("remove confirm = %d", w.Code)
	}
	if w := f.post("/admin/capacity/signup-allowlist/remove", "admin-tok", url.Values{"cidr": {cidr}}); w.Code != http.StatusFound {
		t.Errorf("allowlist remove = %d\n%s", w.Code, w.Body.String())
	}
	if listed() {
		t.Error("the network is still on the allowlist after the remove")
	}
	if w := f.post("/admin/capacity/signup-allowlist/remove", "admin-tok", url.Values{"cidr": {cidr}}); w.Code != http.StatusNotFound {
		t.Errorf("removing an absent network = %d; want 404", w.Code)
	}
}

func TestCollectionsPagesManageCollections(t *testing.T) {
	f := newConsoleFixture(t)
	ctx := context.Background()
	name := fmt.Sprintf("Console %d", time.Now().UnixNano())
	t.Cleanup(func() { // the one this test creates, by its exact name (the id is parsed below)
		_, _ = f.store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.collections WHERE name = $1`, name)
	})
	if w := f.get("/admin/collections", "user-tok"); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin's GET /admin/collections = %d; want 403", w.Code)
	}
	w := f.post("/admin/collections", "admin-tok", url.Values{"name": {name}, "description": {"d"}, "position": {"5"}})
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/admin/collections/") {
		t.Fatalf("create = %d %q\n%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	detail, _, _ := strings.Cut(w.Header().Get("Location"), "?") // the page with its notice flag
	id, err := strconv.ParseInt(strings.TrimPrefix(detail, "/admin/collections/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if w := f.post("/admin/collections", "admin-tok", url.Values{"name": {name}}); w.Code != http.StatusConflict {
		t.Errorf("a second collection with the name = %d; want 409", w.Code)
	}
	if w := f.get("/admin/collections", "admin-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `href="`+detail+`"`) {
		t.Errorf("the list does not link the new collection (%d)", w.Code)
	}
	if w := f.get(detail, "admin-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `value="`+name+`"`) {
		t.Fatalf("detail = %d\n%s", w.Code, w.Body.String())
	}
	if w := f.post(detail, "admin-tok", url.Values{"name": {name + " renamed"}, "description": {"dd"}, "position": {"2"}}); w.Code != http.StatusFound {
		t.Errorf("rename = %d\n%s", w.Code, w.Body.String())
	}
	cols, _ := f.store.ListCollections(ctx, f.adminID)
	var renamed bool
	for _, c := range cols {
		if c.CollectionID == id && c.Name == name+" renamed" && c.Position == 2 {
			renamed = true
		}
	}
	if !renamed {
		t.Error("the rename/reorder did not reach the store")
	}

	groupID, err := f.store.CreateUserGroup(ctx, f.adminID, name+" group")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.user_groups WHERE group_id = $1`, groupID)
	})
	if w := f.post(detail+"/groups", "admin-tok", url.Values{"group_id": {strconv.FormatInt(groupID, 10)}}); w.Code != http.StatusFound {
		t.Errorf("add group = %d\n%s", w.Code, w.Body.String())
	}
	groups, _ := f.store.GetCollectionGroups(ctx, id)
	if len(groups) != 1 || groups[0].GroupID != groupID {
		t.Fatalf("members after the add: %+v", groups)
	}
	remove := fmt.Sprintf("%s/groups/%d/remove", detail, groupID)
	if w := f.get(remove, "admin-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="`+remove+`"`) {
		t.Errorf("remove confirm = %d", w.Code)
	}
	if w := f.post(remove, "admin-tok", url.Values{}); w.Code != http.StatusFound {
		t.Errorf("remove group = %d\n%s", w.Code, w.Body.String())
	}
	if groups, _ = f.store.GetCollectionGroups(ctx, id); len(groups) != 0 {
		t.Errorf("members after the remove: %+v", groups)
	}
	if w := f.get(detail+"/delete", "admin-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="`+detail+`/delete"`) {
		t.Errorf("delete confirm = %d", w.Code)
	}
	if w := f.post(detail+"/delete", "admin-tok", url.Values{}); w.Code != http.StatusFound || w.Header().Get("Location") != "/admin/collections?ok=deleted" {
		t.Errorf("delete = %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := f.get(detail, "admin-tok"); w.Code != http.StatusNotFound {
		t.Errorf("the deleted collection's page = %d; want 404", w.Code)
	}
}
