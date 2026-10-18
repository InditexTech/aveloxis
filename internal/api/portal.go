// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

// v0.27.3 — portal + admin JSON endpoints backing the aveloxis-gui
// pages (group.html, monitor.html, pending-groups.html, users.html).
//
// Unlike the read-analytics endpoints (gated by api.require_auth
// during rollout), EVERY endpoint here demands a valid Bearer token
// unconditionally — they carry user context (whose groups? who
// approves?) that cannot exist without identity, and admin routes
// additionally require the admin flag. Operator, 2026-07-10:
// "Obviously only admin users will see the /users urls etc."
// Reuses the v0.19.0 group-approval machinery verbatim — no new
// approval system.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aveloxis/aveloxis/internal/admin"
	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/httpserver"
	"github.com/aveloxis/aveloxis/internal/safego"
)

// requireUser demands a validated Bearer identity regardless of the
// api.require_auth rollout flag (user-context endpoints are
// meaningless without one). Writes the 401 (or, on a store failure, the 503) itself on failure.
// On success it marks the answer per-caller (`private, no-store`; PR #226
// review 5408306640 on /me, then its class — the groups, home, admin and
// account routes carried no cache directive at all): this is the layer
// that knows the answer belongs to one caller (SR-18). No route behind
// requireUser is shareable today; the shareable routes (cachedRepoGET's,
// the metrics catalog, the authz route) do not pass through it. A
// shareable route added behind it must set its own headers AFTER the
// call, which replace these. Routes that answer differently WITH an
// identity but need none (search, compare, entity search) read it through
// callerIdentity, which marks the same way.
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (authInfo, bool) {
	if info, ok := r.Context().Value(authCtxKey{}).(authInfo); ok {
		setNoStoreHeaders(w.Header())
		return info, true
	}
	// The global middleware may not have resolved a token (auth off /
	// exempt LAN without a header). Resolve here so LAN callers with a
	// token still work, and everyone else gets a clean 401 — or, when
	// the store failed to resolve a presented token, the 503.
	if tok := bearerToken(r); tok != "" {
		info, err := s.auth.resolveToken(r.Context(), tok)
		if err == nil && info.APITokenID != 0 {
			// The middleware attaches every resolved token's identity (with
			// the request-level admin drop, withIdentity), so this path is
			// not expected; an API token here could not carry that drop, so
			// it is refused (ASVS review G9).
			s.logger.Error("an API token reached requireUser without an identity — refused", "path", r.URL.Path, "token_id", info.APITokenID)
			writeAuthError(w, http.StatusForbidden, "an API token cannot be used here")
			return authInfo{}, false
		}
		if err == nil {
			setNoStoreHeaders(w.Header())
			return info, true
		}
		if !errors.Is(err, errInvalidToken) {
			// The store failed: 503, not "sign in again" (follow-up 6).
			s.auth.refuseStoreError(w, r, err)
			return authInfo{}, false
		}
	}
	writeAuthError(w, http.StatusUnauthorized, "this endpoint requires a signed-in session (Bearer token)")
	return authInfo{}, false
}

// requireSession is requireUser for account settings: an API token is
// refused (ASVS review G2: a token could change its owner's account e-mail,
// redirecting the notification address to whoever held it). Same shape as
// requireAdmin's token refusal.
func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) (authInfo, bool) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return authInfo{}, false
	}
	if info.APITokenID != 0 {
		s.logRefusal("token_cannot_change_account", info, "path", r.URL.Path)
		writeAuthError(w, http.StatusForbidden, "an API token cannot change account settings; sign in to change them")
		return authInfo{}, false
	}
	return info, true
}

// callerIdentity is the read for routes that serve everyone but answer
// differently for a signed-in caller (the star column of a search, a
// compare keyed on the caller's scope, the in_scope class of an entity
// search): when an identity is present the answer is per-caller and is
// marked `private, no-store` like requireUser's (L10 round 2 of 0.29.75,
// the class sweep); without one nothing is marked. Every direct read of
// the identity in a handler goes through this or requireUser
// (TestIdentityReadsGoThroughTheMarkingHelpers).
func callerIdentity(w http.ResponseWriter, r *http.Request) (authInfo, bool) {
	info, ok := r.Context().Value(authCtxKey{}).(authInfo)
	if ok {
		setNoStoreHeaders(w.Header())
	}
	return info, ok
}

// requireAdmin layers the admin check on requireUser.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (authInfo, bool) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return authInfo{}, false
	}
	// An API token authenticates as its owner for data, never to administer
	// (OWASP ASVS V8.2.1, the 0.29.82 review A1): a leaked admin-owned token
	// could otherwise grant itself replacements or change roles. Only a
	// signed-in session administers. Checked FIRST: since 0.29.86 a token
	// never carries IsAdmin, so after the role check this refusal could not
	// be reached and an admin's token was told it lacked admin access.
	if info.APITokenID != 0 {
		s.logRefusal("token_cannot_administer", info, "path", r.URL.Path)
		writeAuthError(w, http.StatusForbidden, "an API token cannot administer; sign in to use admin routes")
		return authInfo{}, false
	}
	if !info.IsAdmin {
		writeAuthError(w, http.StatusForbidden, "administrator access required")
		return authInfo{}, false
	}
	return info, true
}

// --- /api/v1/me ---

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	// v0.27.77: carry the display identity so the SPA nav can show
	// the REAL signed-in user (it rendered a hardcoded placeholder
	// before). v0.27.84: name + avatar_url added — the home greeting
	// and nav avatar render from here, never from mocks. Best-effort —
	// a lookup failure yields empty strings, never an error for /me.
	var login, name, avatarURL string
	if s.accounts != nil { // a Server without a store answers the identity it has: none
		login, name, avatarURL, _ = s.accounts.GetUserIdentity(r.Context(), info.UserID)
	}
	// 2026-10-04: the account fields the profile page shows — the forge
	// signed in with, the confirmed email (the forge's own, or one confirmed
	// through the link) and a confirmation still pending. Best-effort like
	// the identity, but a failed read is logged.
	provider, email, pending := s.accountFields(r, info.UserID)
	// Account data, with the addresses: never stored by a browser or an
	// intermediary — requireUser marked the answer `private, no-store`
	// (PR #226 review 5408306640; pinned in TestMeCarriesTheAccountFields).
	s.writeJSON(w, r, map[string]any{
		"user_id":       info.UserID,
		"login":         login,
		"name":          name,
		"avatar_url":    avatarURL,
		"provider":      provider,
		"email":         email,
		"email_pending": pending,
		"is_admin":      info.IsAdmin,
		// v0.29.89: the account's fair-use numbers (summary/53); null when
		// the store cannot answer.
		"capacity": s.meCapacity(r, info),
		"scope_repo_count": func() int {
			if info.IsAdmin {
				return -1 // unscoped
			}
			return len(info.Scope)
		}(),
	})
}

// --- groups (per-user) ---

type groupJSON struct {
	GroupID   int64  `json:"group_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	RepoCount int    `json:"repo_count"`
	Favorited bool   `json:"favorited"`
	// v0.27.84: pending collection_add_requests — the GUI renders
	// "N additions awaiting approval" from this (the group itself is
	// approved; the approval unit is the addition, v0.27.20).
	PendingAdds int `json:"pending_adds"`
}

func (s *Server) handleGroupsList(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	groups, err := s.store.GetUserGroups(r.Context(), info.UserID)
	if err != nil {
		s.serverError(w, r, "handleGroupsList", err)
		return
	}
	out := make([]groupJSON, 0, len(groups))
	for _, g := range groups {
		status := g.Status
		if status == "" {
			status = "approved"
		}
		out = append(out, groupJSON{g.GroupID, g.Name, status, g.RepoCount, g.Favorited, g.PendingAdds})
	}
	s.writeJSON(w, r, map[string]any{"groups": out})
}

// Bounds on the group writes (ASVS review G7: the out-of-scope refusal points
// API tokens at these routes, which read unbounded bodies).
const (
	// groupCreateBodyLimit holds the longest name (admin.MaxGroupNameLength)
	// in UTF-8 with room for the JSON around it.
	groupCreateBodyLimit int64 = 1 << 12
	// groupAddBodyLimit holds admin.MaxPasteURLs URLs of the longest
	// length the store accepts, plus the JSON around them.
	groupAddBodyLimit int64 = admin.MaxPasteURLs*(db.MaxAddURLBytes+8) + 1<<16
)

func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, groupCreateBodyLimit)).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeAuthError(w, http.StatusBadRequest, "body must be {\"name\": \"...\"}")
		return
	}
	id, err := s.admin.CreateGroup(r.Context(), s.actor(info), req.Name)
	if err != nil {
		if !s.refuse(w, info, err) {
			s.serverError(w, r, "handleGroupCreate", err)
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"group_id": id})
}

// parsePortalPage reads the v0.27.14 pagination params: page
// (default 1) and page_size (default 50, capped at 100). Garbage,
// zero, and negative inputs collapse to the defaults.
func parsePortalPage(r *http.Request) (page, pageSize int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ = strconv.Atoi(r.URL.Query().Get("page_size"))
	p := db.NormalizeListingPage(page, pageSize, "", "") // the one clamp (DBS-2)
	return p.Page, p.PageSize
}

func (s *Server) handleGroupRepos(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	page, pageSize := parsePortalPage(r)
	sortKey := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	// v0.27.75: the store fills the whole row — cached queue counts,
	// last_collected, forge-reported last_activity, and the caller's
	// star state — and applies the allowlisted server-side sort (the
	// collections table grammar, shared allowlist). No per-page
	// annotation loop remains here.
	// Who may read the group is the service's one rule (an owner, or an
	// administrator's session; N11: a stranger learns nothing).
	if _, err := s.admin.GroupAccess(r.Context(), s.actor(info), groupID, admin.Read); err != nil {
		if !s.refuse(w, info, err) {
			s.serverError(w, r, "handleGroupRepos", err)
		}
		return
	}
	repos, total, err := s.store.GetPortalGroupRepos(r.Context(), info.UserID, groupID, page, pageSize, sortKey, sortDir)
	if err != nil {
		s.serverError(w, r, "handleGroupRepos", err)
		return
	}
	if repos == nil {
		repos = []db.PortalGroupRepo{}
	}
	// Echo the EFFECTIVE values: the store's one normaliser (58 §7 item
	// 20: `dir` is "asc" or "desc", never the caller's casing).
	p := db.NormalizeListingPage(page, pageSize, sortKey, sortDir)
	s.writeJSON(w, r, map[string]any{
		"repos": repos, "total": total, "page": p.Page, "page_size": p.PageSize,
		"sort": p.Sort, "dir": p.Dir,
	})
}

// handleGroupOrgs lists the organizations tracked in a group
// (operator report 2026-07-21: the SPA group page showed only
// repositories — a group's tracked orgs were invisible even though
// the backend has carried them in aveloxis_ops.user_org_requests
// since v0.19.x). Display-only read; ownership-checked for
// non-admins exactly like the repos listing. Org REGISTRATION stays
// on POST /groups/{id}/repos with kind=org.
func (s *Server) handleGroupOrgs(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	if _, err := s.admin.GroupAccess(r.Context(), s.actor(info), groupID, admin.Read); err != nil {
		if !s.refuse(w, info, err) {
			s.serverError(w, r, "handleGroupOrgs", err)
		}
		return
	}
	orgs, err := s.store.GetGroupOrgs(r.Context(), groupID)
	if err != nil {
		s.serverError(w, r, "handleGroupOrgs", err)
		return
	}
	type orgJSON struct {
		OrgRequestID int64      `json:"org_request_id"`
		URL          string     `json:"url"`
		Name         string     `json:"name"`
		Platform     string     `json:"platform"`
		LastScanned  *time.Time `json:"last_scanned,omitempty"`
	}
	out := make([]orgJSON, 0, len(orgs))
	for _, o := range orgs {
		out = append(out, orgJSON{o.OrgRequestID, o.OrgURL, o.OrgName, o.Platform, o.LastScanned})
	}
	s.writeJSON(w, r, map[string]any{"orgs": out})
}

// handleGroupAddRepo is the "request access / request collection"
// affordance the compare picker's §2b classes point at. kind=repo
// (default) or org. Ownership is enforced inside the store methods.
//
// v0.27.20 (per-add approval, summary/15): already-tracked repos link
// instantly; a non-admin's NOT-yet-tracked repo or org registration
// creates a pending add-request instead of enqueueing — the response
// carries pending_approval + request_id so the GUI can say "awaiting
// administrator approval" rather than pretending collection started.
//
// 2026-07-21 bulk paste: the body also accepts {"urls": ["...", ...]}
// so a newline-separated paste lands in ONE AddReposToGroup call (one
// approval unit, per v0.27.20) instead of N sequential requests. The
// legacy single-url body stays accepted — `urls` wins when present.
// Orgs remain one per request: an org is already an unbounded add.
func (s *Server) handleGroupAddRepo(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	var req struct {
		URL  string   `json:"url"`
		URLs []string `json:"urls"`
		Kind string   `json:"kind"`
	}
	const bodyShape = "body must be {\"url\": \"...\"} or {\"urls\": [\"...\", ...]}, plus \"kind\": \"repo\"|\"org\""
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, groupAddBodyLimit)).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, bodyShape)
		return
	}
	urls := make([]string, 0, len(req.URLs)+1)
	for _, u := range req.URLs {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		if u := strings.TrimSpace(req.URL); u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		writeAuthError(w, http.StatusBadRequest, bodyShape)
		return
	}
	resp := map[string]any{"ok": true, "submitted": len(urls)}
	a := s.actor(info)
	if req.Kind == "org" {
		if len(urls) > 1 {
			writeAuthError(w, http.StatusBadRequest, "add organizations one at a time — bulk paste is for repositories")
			return
		}
		out, err := s.admin.AddOrgToGroup(r.Context(), a, groupID, urls[0])
		if err != nil {
			if !s.refuse(w, info, err) {
				s.serverError(w, r, "handleGroupAddRepo", fmt.Errorf("group %d, kind org: %w", groupID, err))
			}
			return
		}
		if out.Registered {
			// v0.27.84: lets the GUI say "tracked now" (admin add OR
			// the already-registered-org auto-approve) vs "pending".
			resp["registered"] = 1
		} else {
			resp["pending_approval"] = 1
			resp["request_id"] = out.RequestID
		}
		s.writeJSON(w, r, resp)
		return
	}
	// The service parses every line (a missing scheme supplied; a line
	// that is not a repository URL reported in "invalid" and skipped),
	// applies the per-add approval rule through the store, and drops the
	// caller's cached scope when anything was linked or queued.
	out, err := s.admin.AddToGroup(r.Context(), a, groupID, urls)
	if err != nil {
		if !s.refuse(w, info, err) {
			s.serverError(w, r, "handleGroupAddRepo", fmt.Errorf("group %d, kind repo: %w", groupID, err))
		}
		return
	}
	resp["linked"] = out.Linked
	resp["enqueued"] = out.Enqueued
	if out.Pending > 0 {
		resp["pending_approval"] = out.Pending
		resp["request_id"] = out.RequestID
	}
	if len(out.Invalid) > 0 {
		resp["invalid"] = out.Invalid
	}
	s.writeJSON(w, r, resp)
}

// handleGroupPendingAdds lists the group's own awaiting-approval
// content (v0.27.20) so the GUI's group page can show what's still in
// review. Ownership-checked for non-admins.
func (s *Server) handleGroupPendingAdds(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	if _, err := s.admin.GroupAccess(r.Context(), s.actor(info), groupID, admin.Read); err != nil {
		if !s.refuse(w, info, err) {
			s.serverError(w, r, "handleGroupPendingAdds", err)
		}
		return
	}
	items, err := s.store.GetPendingAddItemsForGroup(r.Context(), groupID)
	if err != nil {
		// No database text in the body (round-25 review: the 403 showed a
		// non-admin the database user, name, host and port during an outage).
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelWarn, err, "group pending adds failed", "group_id", groupID, "user_id", info.UserID, "error", err)
		writeAuthError(w, http.StatusInternalServerError, "could not load the group's pending additions right now; try again")
		return
	}
	type itemJSON struct {
		RequestID int64     `json:"request_id"`
		Kind      string    `json:"kind"`
		URL       string    `json:"url"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := make([]itemJSON, 0, len(items))
	for _, it := range items {
		out = append(out, itemJSON{it.RequestID, it.Kind, it.URL, it.CreatedAt})
	}
	s.writeJSON(w, r, map[string]any{"pending": out})
}

// handleAdminAddRequests lists the pending per-add approval queue
// (v0.27.20) for the GUI's approvals page.
func (s *Server) handleAdminAddRequests(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	pending, err := s.store.ListPendingAddRequests(r.Context())
	if err != nil {
		s.serverError(w, r, "handleAdminAddRequests", err)
		return
	}
	type reqJSON struct {
		RequestID  int64     `json:"request_id"`
		UserID     int       `json:"user_id"`
		UserLogin  string    `json:"user_login"`
		UserEmail  string    `json:"user_email"`
		GroupID    int64     `json:"group_id"`
		GroupName  string    `json:"group_name"`
		Kind       string    `json:"kind"`
		OrgURL     string    `json:"org_url,omitempty"`
		ItemCount  int       `json:"item_count"`
		SampleURLs []string  `json:"sample_urls,omitempty"`
		CreatedAt  time.Time `json:"created_at"`
	}
	out := make([]reqJSON, 0, len(pending))
	for _, p := range pending {
		out = append(out, reqJSON{p.RequestID, p.UserID, p.UserLogin, p.UserEmail,
			p.GroupID, p.GroupName, p.Kind, p.OrgURL, p.ItemCount, p.SampleURLs, p.CreatedAt})
	}
	s.writeJSON(w, r, map[string]any{"pending": out})
}

// handleAdminAddRequestDecision approves/rejects one add-request.
// Approval processing (UpsertRepo + enqueue + link per item) runs in
// the background so a large batch doesn't block the admin's request;
// re-approving resumes an interrupted pass (items are stamped as they
// process), and re-approving an org request re-runs its registration
// (DecideAddRequest). Org approvals register tracking here; the actual repo
// scan happens on the scheduler's next refreshUserOrgs tick — the api
// process deliberately has no platform API keys.
func (s *Server) handleAdminAddRequestDecision(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	requestID, err := strconv.ParseInt(r.PathValue("requestID"), 10, 64)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid request id")
		return
	}
	var approve bool
	switch r.PathValue("decision") {
	case "approve":
		approve = true
	case "reject":
		approve = false
	default:
		writeAuthError(w, http.StatusBadRequest, "decision must be approve or reject")
		return
	}
	changed, err := s.admin.DecideAddRequest(r.Context(), s.actor(info), requestID, approve)
	if err != nil {
		if !s.refuse(w, info, err) {
			// One failure, one line (PR #218 review C15): serverError logs it
			// at ERROR, carrying the request and decision in the wrapped error.
			s.serverError(w, r, "handleAdminAddRequestDecision", fmt.Errorf("request %d, decision %s: %w", requestID, r.PathValue("decision"), err))
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"ok": true, "changed": changed})
}

// --- admin ---

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.serverError(w, r, "handleAdminUsers", err)
		return
	}
	type userJSON struct {
		UserID    int       `json:"user_id"`
		Login     string    `json:"login"`
		Email     string    `json:"email"`
		Provider  string    `json:"provider"`
		IsAdmin   bool      `json:"is_admin"`
		CreatedAt time.Time `json:"created_at"` // real join date (v0.27.89)
		LastSeen  time.Time `json:"last_seen"`  // re-stamped on every login
	}
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		out = append(out, userJSON{u.UserID, u.Login, u.Email, u.Provider, u.IsAdmin, u.CreatedAt, u.LastSeen})
	}
	s.writeJSON(w, r, map[string]any{"users": out})
}

func (s *Server) handleAdminSetUserAdmin(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	targetID, err := admin.ParseUserID(r.PathValue("userID")) // one rule for a users.user_id (0.29.103 F2)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var req struct {
		Admin *bool `json:"admin"`
	}
	// The field is required (58 §7 item 18: `{}` demoted the account); the
	// body is bounded (item 11).
	if err := decodeJSONBody(w, r, adminJSONBodyLimit, &req); err != nil || req.Admin == nil {
		writeAuthError(w, http.StatusBadRequest, "body must be {\"admin\": true|false}")
		return
	}
	// The service refuses self-demotion and, atomically in the store, the
	// last administrator's; its RoleChanged hook drops the token cache so
	// the target's own /me reflects the new role at once.
	if err := s.admin.SetUserAdmin(r.Context(), s.actor(info), targetID, *req.Admin); err != nil {
		if !s.refuse(w, info, err) {
			s.serverError(w, r, "handleAdminSetUserAdmin", err)
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"ok": true})
}

// handleAdminGroupReject — POST /admin/groups/{groupID}/reject: rejects a
// group (the group-level abuse lever since v0.27.20: it takes no further
// additions and its organizations stop being listed; its repositories
// stay). The legacy approve arm and GET /admin/groups/pending are gone
// since 0.29.100 (N9, stage 1g of summary/59): groups are born approved and
// the additions queue (/admin/add-requests) is the approval unit.
func (s *Server) handleAdminGroupReject(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	if err := s.admin.RejectGroup(r.Context(), s.actor(info), groupID); err != nil {
		if !s.refuse(w, info, err) {
			// One failure, one line (PR #218 review C15): serverError logs it at
			// ERROR, carrying the group in the wrapped error.
			s.serverError(w, r, "handleAdminGroupReject", fmt.Errorf("group %d, decision reject: %w", groupID, err))
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) handleAdminMonitorStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	stats, err := s.store.QueueStats(r.Context())
	if err != nil {
		s.serverError(w, r, "handleAdminMonitorStats", err)
		return
	}
	s.writeJSON(w, r, map[string]any{"queue": stats})
}

func (s *Server) handleAdminMonitorQueue(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	const pageSize = 100
	sortKey := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	jobs, total, err := s.store.ListQueuePage(r.Context(), pageSize, (page-1)*pageSize, r.URL.Query().Get("q"), sortKey, sortDir)
	// Echo the EFFECTIVE sort values (the handleGroupRepos contract —
	// the log-the-effective-value rule applied to response envelopes):
	// an unknown key means the store used the default composite, and
	// the envelope must say so, never parrot the caller's input.
	if !db.QueueSortValid(sortKey) {
		// Copilot round 8 on PR #193 (suppressed #1): when the key
		// falls back, the store used the collecting-first COMPOSITE and
		// ignored dir entirely — echoing the caller's "desc" would
		// claim a direction that was never applied. Both echo empty.
		sortKey = ""
		sortDir = ""
	} else if strings.EqualFold(sortDir, "desc") {
		sortDir = "desc"
	} else {
		sortDir = "asc"
	}
	if err != nil {
		s.serverError(w, r, "handleAdminMonitorQueue", err)
		return
	}
	// Attach repo names — ids alone mean nothing to users (operator) —
	// and the repo_info meta counts so gathered-vs-metadata pairs render
	// side by side (operator, 2026-07-15).
	ids := make([]int64, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.RepoID)
	}
	// A failed enrichment read leaves the ids bare, logged (58 §7 item 10:
	// both errors were discarded).
	repos, rerr := s.store.GetReposBatch(r.Context(), ids)
	if rerr != nil {
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelWarn, rerr, "monitor queue: repository names unreadable — ids shown bare", "error", rerr)
	}
	stats, serr := s.store.GetRepoStatsBatch(r.Context(), ids)
	if serr != nil {
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelWarn, serr, "monitor queue: repository stats unreadable — metadata counts shown as 0", "error", serr)
	}
	type jobJSON struct {
		RepoID        int64      `json:"repo_id"`
		Repo          string     `json:"repo"`
		Status        string     `json:"status"`
		Priority      int        `json:"priority"`
		DueAt         time.Time  `json:"due_at"`
		LastCollected *time.Time `json:"last_collected,omitempty"`
		LastError     *string    `json:"last_error,omitempty"`
		Issues        int        `json:"issues"`
		PRs           int        `json:"prs"`
		Commits       int        `json:"commits"`
		MetaIssues    int        `json:"meta_issues"`
		MetaPRs       int        `json:"meta_prs"`
		MetaCommits   int        `json:"meta_commits"`
	}
	out := make([]jobJSON, 0, len(jobs))
	for _, j := range jobs {
		label := strconv.FormatInt(j.RepoID, 10)
		if rp, ok := repos[j.RepoID]; ok && rp != nil {
			label = rp.Owner + "/" + rp.Name
		}
		row := jobJSON{RepoID: j.RepoID, Repo: label, Status: j.Status, Priority: j.Priority,
			DueAt: j.DueAt, LastCollected: j.LastCollected, LastError: j.LastError,
			Issues: j.LastIssues, PRs: j.LastPRs, Commits: j.LastCommits}
		if st, ok := stats[j.RepoID]; ok && st != nil {
			row.MetaIssues, row.MetaPRs, row.MetaCommits = st.MetadataIssues, st.MetadataPRs, st.MetadataCommits
		}
		out = append(out, row)
	}
	s.writeJSON(w, r, map[string]any{
		"jobs": out, "total": total, "page": page, "page_size": pageSize,
		"sort": sortKey, "dir": sortDir,
	})
}

// handleAdminPrioritizeRepo (v0.27.14) is the SPA monitor's "Boost"
// button: pure reuse of store.PrioritizeRepo — the identical call the
// legacy :5555 monitor's POST /api/prioritize/{repoID} makes (push to
// priority 0, due now, back to 'queued').
func (s *Server) handleAdminPrioritizeRepo(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	repoID, err := parseRepoID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.PrioritizeRepo(r.Context(), repoID); err != nil {
		if errors.Is(err, db.ErrRepoNotInQueue) {
			http.Error(w, "repo not found in queue", http.StatusNotFound)
			return
		}
		s.serverError(w, r, "handleAdminPrioritizeRepo", err)
		return
	}
	s.writeJSON(w, r, map[string]any{"ok": true, "repo_id": repoID})
}

// --- v0.27.4: home tab (stars + activity) ---

// handleStarRepo stars (PUT) or unstars (DELETE) a repo for the
// signed-in user. Requires identity unconditionally — stars are
// per-user state. The repo must be in the caller's scope.
func (s *Server) handleStarRepo(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	repoID, err := parseRepoID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A repository that does not exist is a 404 for every caller (D5),
	// through the prelude's existence read: before stage 1b's review an
	// administrator's PUT reached the stars table's foreign key (a 500) and
	// DELETE answered 200 for nothing. Only a repository that exists is
	// linked below (user_repos has no FK on repo_id; the other implicit-link
	// sites check too — 0.29.82 L10 round 3).
	if !s.repoKnown(w, r, repoID) {
		return
	}
	// v0.27.4 (operator decision): starring an out-of-scope repo
	// auto-adds it to the user's implicit "Starred" group instead of
	// 403ing. Search only surfaces already-collected repos, so this
	// can never trigger new collection — and approval exists to gate
	// new collection (the 50,000-repo bulk-add case), never to gate
	// access to data we already have. Unstar needs no scope at all.
	addedToGroup := ""
	if r.Method != http.MethodDelete && !info.IsAdmin && !info.Scope[repoID] {
		// An API token never writes a group link (operator decision
		// 2026-10-09): refused, with the way to add the repository.
		if info.APITokenID != 0 {
			s.refuseTokenOutOfScope(w, r, info, repoID)
			return
		}
		// A signed-in session is not rate limited (0.29.82): the implicit
		// link is capped per user before it writes (the ASVS review's A2).
		var slot autoAddSlot // the window a refund goes back to
		if s.autoAdds != nil {
			var ok bool
			var refused *capacity.Exceeded
			if slot, ok, refused = s.autoAdds.reserve(info.UserID); !ok {
				s.refuseAutoAdd(w, info, refused)
				return
			}
		}
		linked := false
		gid, gerr := s.store.FindOrCreateStarredGroup(r.Context(), info.UserID)
		if gerr == nil {
			linked, gerr = s.store.AddRepoToGroupByID(r.Context(), gid, repoID)
		}
		// Nothing linked (an error, or already linked behind a stale cached
		// scope or a concurrent star: Copilot review 5472987053 on PR #228)
		// gives the slot back; the cached scope is dropped per settleAutoAdd.
		s.settleAutoAdd(info.UserID, slot, linked, gerr)
		if s.refuseCapacity(w, info, gerr) {
			return
		}
		if gerr != nil {
			s.serverError(w, r, "handleStarRepo", gerr)
			return
		}
		if linked {
			addedToGroup = db.StarredGroupName
		}
	}
	if r.Method == http.MethodDelete {
		err = s.store.UnstarRepo(r.Context(), info.UserID, repoID)
	} else {
		err = s.store.StarRepo(r.Context(), info.UserID, repoID)
	}
	if err != nil {
		s.serverError(w, r, "handleStarRepo", err)
		return
	}
	s.homeCache.invalidate(info.UserID)
	resp := map[string]any{"ok": true, "starred": r.Method != http.MethodDelete}
	if addedToGroup != "" {
		resp["added_to_group"] = addedToGroup
	}
	s.writeJSON(w, r, resp)
}

// handleRepoStarState reports whether the signed-in user has starred
// the repo (v0.27.85 — the repo page's star toggle needs the current
// state; every other starred source is list-shaped). Same requireUser
// posture as PUT/DELETE: starred state is caller-personal, never
// repo-scoped, so authorizeRepo does not apply.
func (s *Server) handleRepoStarState(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	repoID, err := parseRepoID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.repoKnown(w, r, repoID) { // a 404 for a repository that does not exist (D5)
		return
	}
	starred, err := s.store.IsRepoStarred(r.Context(), info.UserID, repoID)
	if err != nil {
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelError, err, "star state read failed", "user_id", info.UserID, "repo_id", repoID, "error", err)
		http.Error(w, "could not read star state", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, r, map[string]any{"starred": starred})
}

// handleHomeRepos returns the signed-in user's home-tab repo list:
// starred repos first (always shown), then the most active repos from
// their own groups over the trailing 90 days.
func (s *Server) handleHomeRepos(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	s.serveHomeRepos(w, r, info.UserID, limit)
}

// serveHomeRepos implements the v0.29.0 stale-while-revalidate
// contract: a fresh cache entry serves as a hit; an EXPIRED entry
// serves immediately as stale while ONE background refresh (per user)
// recomputes it; only a genuinely-empty cache blocks on the loader —
// and the queue-cached activity ranking makes that path fast too.
// A 90-day ranking does not meaningfully change inside the staleness
// window, so serving the expired body costs nothing the user can see.
func (s *Server) serveHomeRepos(w http.ResponseWriter, r *http.Request, userID, limit int) {
	if s.homeLoader == nil {
		// Fail CLOSED like the sharedWithMeStore precedent — a bare
		// Server without the seam must never nil-panic (review
		// 2026-08-31 #7).
		s.serverError(w, r, "serveHomeRepos", errors.New("home loader not configured"))
		return
	}
	if limit <= 0 {
		limit = 50 // mirror the store's clamp so the cache key is the EFFECTIVE limit
	}
	if limit > homeReposMaxLimit {
		// Copilot round 14 on PR #193: with entries keyed (user, limit),
		// an unbounded limit let an authenticated caller both force
		// full-scope loads AND retain up to 10,000 distinct large
		// payload variants in the cache. Clamp BEFORE the cache lookup
		// and the loader so every oversized request shares one entry
		// (bare comparison per the v0.27.65/66 rules).
		limit = homeReposMaxLimit
	}
	body, state := s.homeCache.get(userID, limit)
	switch state {
	case homeCacheFresh:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "hit")
		_, _ = w.Write(body)
		return
	case homeCacheStale:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "stale")
		s.refreshHomeRepos(userID, limit)
		_, _ = w.Write(body)
		return
	}
	// Miss (empty, limit mismatch, or past the stale bound): blocking
	// load — fast now that the ranking is the queue-cached column.
	gen := s.homeCache.generation(userID)
	repos, err := s.homeLoader(r.Context(), userID, limit)
	if err != nil {
		s.serverError(w, r, "serveHomeRepos", err)
		return
	}
	body, marshaled := s.marshalBody(w, r, "serveHomeRepos", map[string]any{"repos": repos})
	if !marshaled {
		return
	}
	s.homeCache.setIfGen(userID, limit, body, gen)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "miss")
	_, _ = w.Write(body)
}

// refreshHomeRepos recomputes one user's cached payload in the
// background. Single-flight per user: concurrent stale hits must not
// stampede the loader. A failed refresh keeps serving the stale body
// (stale beats an error page); the next stale hit retries. The
// generation captured at begin makes the write conditional: an
// invalidate() that lands mid-flight (star toggle, shared-with-me add,
// collection copy) wins — the refresh must never resurrect a
// pre-mutation body past it (review 2026-08-31 #1).
func (s *Server) refreshHomeRepos(userID, limit int) {
	gen, ok := s.homeCache.tryBeginRefresh(userID, limit)
	if !ok {
		return
	}
	go func() {
		defer safego.Recover(s.logger, "home-repos-refresh")
		defer s.homeCache.endRefresh(userID, limit)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		repos, err := s.homeLoader(ctx, userID, limit)
		if err != nil {
			s.logger.Warn("home repos background refresh failed — stale body keeps serving", "user_id", userID, "error", err)
			return
		}
		body, err := json.Marshal(map[string]any{"repos": repos})
		if err != nil {
			s.logger.Error("home repos background refresh: body not marshaled — stale body keeps serving", "user_id", userID, "error", err)
			return
		}
		s.homeCache.setIfGen(userID, limit, body, gen)
	}()
}

// homeReposCache bounds the cost of GetHomeRepos. v0.29.0: entries
// past the TTL are served STALE while a background refresh runs
// (production measured the cold query at mean 8.1s / max 48.2s for a
// fleet-scope admin — the 5-minute TTL meant nearly every human visit
// paid it). Invalidated per-user on star/unstar so toggles reorder on
// the very next (now-fast) blocking fetch.
type homeReposCache struct {
	mu sync.Mutex
	// Entries and the refresh single-flight are keyed (userID, limit)
	// (Copilot round 13 on PR #193): keying by user alone made a
	// ?limit=5 store overwrite the same user's default-limit entry, so
	// alternating valid limits turned EVERY request into a blocking
	// miss — the SWR machinery held only the correctness half (a
	// foreign-limit body was never served) while the caching half was
	// defeated. gen stays per-USER: an invalidate must kill every
	// limit's entry at once.
	entries    map[homeCacheKey]homeCacheEntry
	refreshing map[homeCacheKey]bool
	// gen is bumped by invalidate(); a conditional set carrying an
	// older generation is dropped, so no in-flight load can resurrect
	// a pre-invalidate body (review 2026-08-31 #1).
	gen map[int]uint64
}

// homeCacheKey scopes a cached body to the (user, effective limit)
// pair it was built for.
// homeReposMaxLimit caps ?limit on the home list (the collection-
// detail page-size grammar: default 50, cap 100). The store clamps
// too (v0.27.65: the backstop lives with the SQL LIMIT).
const homeReposMaxLimit = 100

type homeCacheKey struct {
	user  int
	limit int
}

type homeCacheEntry struct {
	body    []byte
	expires time.Time
}

const homeReposCacheTTL = 5 * time.Minute

// homeReposStaleMax bounds how old a served-stale body may be past its
// TTL: 12×TTL (one hour) keeps the "never block mid-session" property
// while a returning user after hours away gets a genuinely fresh
// blocking load — which the queue-cached ranking makes fast anyway
// (review 2026-08-31 #6; the pre-SWR hard cap was the TTL itself).
const homeReposStaleMax = 12 * homeReposCacheTTL

type homeCacheState int

const (
	homeCacheMiss homeCacheState = iota
	homeCacheFresh
	homeCacheStale
)

// get returns the cached body for the EFFECTIVE limit. A body built
// with a different limit is a miss (serving a 5-row body to the
// default-limit page — or vice versa — would be wrong for 5 minutes;
// review 2026-08-31 #2). Past homeReposStaleMax the entry is a miss
// too.
func (c *homeReposCache) get(userID, limit int) ([]byte, homeCacheState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[homeCacheKey{userID, limit}]
	if !ok {
		return nil, homeCacheMiss
	}
	now := time.Now()
	if !now.After(e.expires) {
		return e.body, homeCacheFresh
	}
	if now.After(e.expires.Add(homeReposStaleMax)) {
		return nil, homeCacheMiss
	}
	return e.body, homeCacheStale
}

func (c *homeReposCache) generation(userID int) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen[userID]
}

// setIfGen stores the body only when no invalidate() landed since gen
// was captured. Both writers (blocking miss + background refresh) use
// it, so the pre-existing miss-path arm of the resurrection race is
// closed too.
func (c *homeReposCache) setIfGen(userID, limit int, body []byte, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen[userID] != gen {
		return // an invalidate won; the next request reloads
	}
	if c.entries == nil {
		c.entries = map[homeCacheKey]homeCacheEntry{}
	}
	if len(c.entries) > 10000 {
		c.entries = map[homeCacheKey]homeCacheEntry{}
	}
	c.entries[homeCacheKey{userID, limit}] = homeCacheEntry{body: body, expires: time.Now().Add(homeReposCacheTTL)}
}

// tryBeginRefresh claims the per-user refresh slot and returns the
// generation the refresh is conditioned on; ok=false means a refresh
// is already in flight.
func (c *homeReposCache) tryBeginRefresh(userID, limit int) (gen uint64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing == nil {
		c.refreshing = map[homeCacheKey]bool{}
	}
	k := homeCacheKey{userID, limit}
	if c.refreshing[k] {
		return 0, false
	}
	c.refreshing[k] = true
	return c.gen[userID], true
}

func (c *homeReposCache) endRefresh(userID, limit int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.refreshing, homeCacheKey{userID, limit})
}

func (c *homeReposCache) invalidate(userID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k.user == userID {
			delete(c.entries, k)
		}
	}
	if c.gen == nil {
		c.gen = map[int]uint64{}
	}
	c.gen[userID]++ // defeats any in-flight load's conditional set
}
