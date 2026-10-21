// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/aveloxis/aveloxis/internal/admin"
	"github.com/aveloxis/aveloxis/internal/db"
)

// Collections API (v0.27.63). Reads are for every authenticated user
// (collections are the curated discovery surface on the home page);
// mutation is admin-only. Mutations are POST-everywhere — the CORS
// Allow-Methods header only advertises GET/POST (plus the star PUT/
// DELETE special case), so DELETE/PATCH verbs would break the SPA.

// handleCollectionsList — GET /collections: every collection with its
// live group/repo counts. The caller's starred collections sort first
// (v0.27.70); everything else stays position-ordered.
func (s *Server) handleCollectionsList(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	cols, err := s.store.ListCollections(r.Context(), info.UserID)
	if err != nil {
		s.serverError(w, r, "handleCollectionsList", err)
		return
	}
	s.writeJSON(w, r, map[string]any{"collections": cols})
}

// handleStarCollection stars (PUT) or unstars (DELETE) a collection
// for the signed-in user (v0.27.70). Stars are a per-user SORT
// preference — starred collections list first for the starrer — never
// a visibility control, so no scope check applies (collections are
// visible to every authenticated user by design). Mirrors the repo
// star pattern (handleStarRepo) minus the scope/auto-add machinery.
func (s *Server) handleStarCollection(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	collID, err := strconv.ParseInt(r.PathValue("collectionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid collection_id", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodDelete {
		err = s.store.UnstarCollection(r.Context(), info.UserID, collID)
	} else {
		// Guard against starring a nonexistent collection: the FK
		// rejects it, but surface a clean 404 instead of a 500.
		err = s.store.StarCollection(r.Context(), info.UserID, collID)
		if err != nil && errors.Is(err, db.ErrCollectionNotFound) {
			http.Error(w, "collection not found", http.StatusNotFound)
			return
		}
	}
	if err != nil {
		s.serverError(w, r, "handleStarCollection", err)
		return
	}
	s.writeJSON(w, r, map[string]any{"ok": true, "starred": r.Method != http.MethodDelete})
}

// handleCollectionDetail — GET
// /collections/{collectionID}?page&page_size&sort&dir: the member
// groups + one page of the DEDUPED repo set. v0.27.74: rows carry
// cached issue/PR/commit counts, last_collected, the forge-reported
// last_activity, and the caller's starred flag; sort/dir select the
// server-side ordering (allowlisted in the store — unknown values
// fall back to name asc).
func (s *Server) handleCollectionDetail(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	collID, err := strconv.ParseInt(r.PathValue("collectionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid collection_id", http.StatusBadRequest)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	sortKey := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")

	groups, err := s.store.GetCollectionGroups(r.Context(), collID)
	if err != nil {
		if !s.refuseCollection(w, err) {
			s.serverError(w, r, "handleCollectionDetail", err)
		}
		return
	}
	repos, total, err := s.store.GetCollectionRepos(r.Context(), collID, info.UserID, page, pageSize, sortKey, sortDir)
	if err != nil {
		if !s.refuseCollection(w, err) {
			s.serverError(w, r, "handleCollectionDetail", err)
		}
		return
	}
	// Echo the EFFECTIVE paging/sort values: the store's one normaliser
	// (the v0.27.65 log-the-effective-value rule applied to envelopes;
	// 58 §7 item 20: `dir` is "asc" or "desc", never the caller's casing).
	p := db.NormalizeListingPage(page, pageSize, sortKey, sortDir)
	s.writeJSON(w, r, map[string]any{
		"collection_id": collID,
		"groups":        groups,
		"repos":         repos,
		"total":         total,
		"page":          p.Page,
		"page_size":     p.PageSize,
		"sort":          p.Sort,
		"dir":           p.Dir,
	})
}

// handleCollectionCopy — POST /collections/{collectionID}/copy
// {group_id} or {group_name}: link every repo of the collection into
// the caller's group. group_name find-or-creates a group for the
// caller (normal creation rules); group_id must already be theirs.
//
// NEVER enqueues collection (pinned at the store layer): collection
// repos are already tracked; approval gates NEW collection only.
// Post-copy the auth scope cache and the caller's home cache are
// invalidated so the new repos are visible on the very next request.
func (s *Server) handleCollectionCopy(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	collID, err := strconv.ParseInt(r.PathValue("collectionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid collection_id", http.StatusBadRequest)
		return
	}
	var req struct {
		GroupID   int64  `json:"group_id"`
		GroupName string `json:"group_name"`
	}
	if err := decodeJSONBody(w, r, adminJSONBodyLimit, &req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	groupID := req.GroupID
	if groupID == 0 {
		if req.GroupName == "" {
			http.Error(w, "group_id or group_name required", http.StatusBadRequest)
			return
		}
		// The caller-named destination is created by the admin service (the
		// one group writer: the name bound, the audit line); CreateUserGroup
		// is a find-or-create (ON CONFLICT (user_id, name)), so an existing
		// group of that name is reused (round-2 review of 0.29.100).
		groupID, err = s.admin.CreateGroup(r.Context(), s.actor(info), req.GroupName)
		if err != nil {
			if !s.refuse(w, info, err) {
				s.serverError(w, r, "handleCollectionCopy", err)
			}
			return
		}
	}
	added, err := s.store.CopyCollectionToGroup(r.Context(), collID, info.UserID, groupID)
	if err != nil {
		if errors.Is(err, db.ErrNotGroupOwner) {
			http.Error(w, "target group is not yours", http.StatusForbidden)
			return
		}
		if s.refuseCapacity(w, info, err) {
			return // the copy does not fit the allocation; nothing was linked
		}
		if !s.refuseCollection(w, err) {
			s.serverError(w, r, "handleCollectionCopy", err)
		}
		return
	}
	// The copy widened the caller's repo scope — bust the caller's caches.
	s.auth.invalidateUser(info.UserID)
	s.homeCache.invalidate(info.UserID)
	s.writeJSON(w, r, map[string]any{"added": added, "group_id": groupID})
}

// ─── Admin mutations (requireAdmin, POST-everywhere) ────────────
//
// Since 0.29.109 (stage 3b of summary/59) the rules are the admin
// service's; these handlers decode, call it and answer. The bodies are the
// ones v0.27.70 answered: an input refusal is plain text (http.Error), the
// store's refusals JSON, so clients see what they saw.

// collectionFields decodes {name, description, position}; a body that is
// not that JSON answers the route's own "name required".
func collectionFields(w http.ResponseWriter, r *http.Request) (admin.CollectionFields, bool) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Position    int    `json:"position"`
	}
	if err := decodeJSONBody(w, r, adminJSONBodyLimit, &req); err != nil {
		http.Error(w, "name required", http.StatusBadRequest)
		return admin.CollectionFields{}, false
	}
	return admin.CollectionFields{Name: req.Name, Description: req.Description, Position: req.Position}, true
}

// refuseCollectionOp answers the admin operations' refusals in the words
// v0.27.70 used: an input refusal as plain text, no such group as its own
// 404, everything else from the one table. false for a failure (the
// caller's serverError).
func (s *Server) refuseCollectionOp(w http.ResponseWriter, info authInfo, err error) bool {
	switch {
	case errors.Is(err, admin.ErrInvalidInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	case errors.Is(err, db.ErrGroupNotFound):
		writeAuthError(w, http.StatusNotFound, "no group with that id")
		return true
	case errors.Is(err, db.ErrGroupNotAdminOwned):
		// The store's sentinel, not an inputError: it has its own plain-text
		// arm so the body stays what v0.27.70 answered (round-1 review F1).
		http.Error(w, db.ErrGroupNotAdminOwned.Error(), http.StatusBadRequest)
		return true
	}
	return s.refuse(w, info, err)
}

// refuseCollection answers the collection store's refusals on the read
// routes (58 §7 item 8: an unknown id or a duplicate name answered 500;
// the detail and the copy answered 200 for an unknown id): 404 for no such
// collection, 409 for a taken name. false for anything else.
func (s *Server) refuseCollection(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, db.ErrCollectionNotFound):
		writeAuthError(w, http.StatusNotFound, "no collection with that id")
	case errors.Is(err, db.ErrCollectionNameTaken):
		writeAuthError(w, http.StatusConflict, "a collection with that name already exists")
	default:
		return false
	}
	return true
}

func collectionID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("collectionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid collection_id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// handleAdminCollectionCreate — POST /admin/collections
// {name, description, position}.
func (s *Server) handleAdminCollectionCreate(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	f, ok := collectionFields(w, r)
	if !ok {
		return
	}
	id, err := s.admin.CreateCollection(r.Context(), s.actor(info), f)
	if err != nil {
		if !s.refuseCollectionOp(w, info, err) {
			s.serverError(w, r, "handleAdminCollectionCreate", err)
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"collection_id": id})
}

// handleAdminCollectionUpdate — POST /admin/collections/{collectionID}
// {name, description, position}: the rename/reorder operation the
// console's collections page also calls (stage 3c).
func (s *Server) handleAdminCollectionUpdate(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	collID, ok := collectionID(w, r)
	if !ok {
		return
	}
	f, ok := collectionFields(w, r)
	if !ok {
		return
	}
	if err := s.admin.UpdateCollection(r.Context(), s.actor(info), collID, f); err != nil {
		if !s.refuseCollectionOp(w, info, err) {
			s.serverError(w, r, "handleAdminCollectionUpdate", err)
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"ok": true})
}

// handleAdminCollectionDelete — POST /admin/collections/{collectionID}/delete.
func (s *Server) handleAdminCollectionDelete(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	collID, ok := collectionID(w, r)
	if !ok {
		return
	}
	if err := s.admin.DeleteCollection(r.Context(), s.actor(info), collID); err != nil {
		if !s.refuseCollectionOp(w, info, err) {
			s.serverError(w, r, "handleAdminCollectionDelete", err)
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"ok": true})
}

// handleAdminCollectionAddGroup — POST /admin/collections/{collectionID}/groups
// {group_id}: link an admin-owned group into the collection.
func (s *Server) handleAdminCollectionAddGroup(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	collID, ok := collectionID(w, r)
	if !ok {
		return
	}
	var req struct {
		GroupID int64 `json:"group_id"`
	}
	if err := decodeJSONBody(w, r, adminJSONBodyLimit, &req); err != nil {
		http.Error(w, "group_id required", http.StatusBadRequest)
		return
	}
	if err := s.admin.AddGroupToCollection(r.Context(), s.actor(info), collID, req.GroupID); err != nil {
		if !s.refuseCollectionOp(w, info, err) {
			s.serverError(w, r, "handleAdminCollectionAddGroup", err)
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"ok": true})
}

// handleAdminCollectionRemoveGroup — POST
// /admin/collections/{collectionID}/groups/{groupID}/remove.
func (s *Server) handleAdminCollectionRemoveGroup(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	collID, ok := collectionID(w, r)
	if !ok {
		return
	}
	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid group_id", http.StatusBadRequest)
		return
	}
	if err := s.admin.RemoveGroupFromCollection(r.Context(), s.actor(info), collID, groupID); err != nil {
		if !s.refuseCollectionOp(w, info, err) {
			s.serverError(w, r, "handleAdminCollectionRemoveGroup", err)
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"ok": true})
}
