// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

// Package web — admin.go holds the administrator console's handlers
// (stage 3 of summary/59, 0.29.109): the Approvals page (the per-add
// approval queue and the recorded forge-ID changes) and the Users page.
// Every page renders through the template set; every decision goes
// through the admin service. The routes are gated by Server.requireAdmin
// (session.go), which reads the privilege from the users row on every
// request: a non-admin gets 403, an anonymous visitor /login.

package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/aveloxis/aveloxis/internal/admin"
)

// handleApproveAddRequest and handleRejectAddRequest decide a pending
// add request through the service (one decision flow for the web GUI and
// the API: the flip, the background pass, the requester's mail). A refusal
// is answered from refusePage's table; a failure is logged with a generic
// body (PR #218 review C6 — the body carried the store's text).
func (s *Server) handleApproveAddRequest(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.decideAddRequestPage(w, r, sess, true)
}

func (s *Server) handleRejectAddRequest(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.decideAddRequestPage(w, r, sess, false)
}

func (s *Server) decideAddRequestPage(w http.ResponseWriter, r *http.Request, sess *Session, approve bool) {
	requestID, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "invalid request id", http.StatusBadRequest)
		return
	}
	if _, err := s.admin.DecideAddRequest(r.Context(), sessionActor(sess), requestID, approve); err != nil {
		if !s.refusePage(w, r, err) {
			s.serverError(w, r, "decideAddRequestPage", fmt.Errorf("add-request %d approve=%t: %w", requestID, approve, err))
		}
		return
	}
	http.Redirect(w, r, "/admin/groups/pending", http.StatusFound)
}

// handleAdminPendingGroups is the Approvals page: the per-add approval
// queue (v0.27.20; the legacy pending-groups table is gone since 0.29.100,
// N9) and the adoptable forge-ID changes (D-24). Both lists are the
// store's; the decisions are the forms' POSTs through the service.
func (s *Server) handleAdminPendingGroups(w http.ResponseWriter, r *http.Request, sess *Session) {
	requests, err := s.store.ListPendingAddRequests(r.Context())
	if err != nil {
		s.serverError(w, r, "handleAdminPendingGroups", fmt.Errorf("list pending add-requests: %w", err))
		return
	}
	changes, err := s.store.ListForgeIDChanges(r.Context(), true)
	if err != nil {
		s.serverError(w, r, "handleAdminPendingGroups", fmt.Errorf("list forge-ID changes: %w", err))
		return
	}
	s.render(w, r, "admin_approvals", map[string]any{
		"Session":  sess,
		"Requests": requests,
		"Changes":  changes,
		"NoteMax":  admin.AdoptNoteMaxLength,
	})
}

// handleAdoptForgeID adopts the forge-ID change the administrator chose
// (the form names the pair, as the API's body does: adopting "the
// repository's newest change" once adopted a pair the admin had not
// clicked). Bounds, the adopter's label, the write and the audit line are
// the service's; a refusal is answered from refusePage's table.
func (s *Server) handleAdoptForgeID(w http.ResponseWriter, r *http.Request, sess *Session) {
	repoID, ok := pathID(r, "repoID")
	if !ok {
		http.Error(w, "invalid repo_id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}
	oldID := strings.TrimSpace(r.FormValue("old_forge_id"))
	newID := strings.TrimSpace(r.FormValue("new_forge_id"))
	if err := s.admin.AdoptForgeID(r.Context(), sessionActor(sess), repoID, oldID, newID, r.FormValue("note")); err != nil {
		if !s.refusePage(w, r, err) {
			s.serverError(w, r, "handleAdoptForgeID", fmt.Errorf("adopt forge-ID change of repo %d: %w", repoID, err))
		}
		return
	}
	http.Redirect(w, r, "/admin/groups/pending", http.StatusFound)
}

func (s *Server) handleRejectGroup(w http.ResponseWriter, r *http.Request, sess *Session) {
	groupID, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "invalid group id", http.StatusBadRequest)
		return
	}
	if err := s.admin.RejectGroup(r.Context(), sessionActor(sess), groupID); err != nil {
		if !s.refusePage(w, r, err) {
			s.serverError(w, r, "handleRejectGroup", fmt.Errorf("reject group %d: %w", groupID, err)) // PR #218 review C6
		}
		return
	}
	http.Redirect(w, r, "/admin/groups/pending", http.StatusFound)
}

// handleAdminUsers is the Users page: every account with its role, when
// it joined and when it was last seen, and the promote/demote form (never
// for the signed-in administrator: the service refuses self-demotion and
// the page shows no button for it).
func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request, sess *Session) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.serverError(w, r, "handleAdminUsers", fmt.Errorf("list users: %w", err))
		return
	}
	s.render(w, r, "admin_users", map[string]any{
		"Session": sess,
		"Users":   users,
		"Notice":  adminNotice(r),
	})
}

func (s *Server) handleSetUserAdmin(w http.ResponseWriter, r *http.Request, sess *Session) {
	userID, ok := pathUserID(r, "id")
	if !ok {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}
	// The field is required and boolean, as the API twin's is (58 §7 item
	// 18: `{}` demoted the account; 0.29.106 review A2 for this form).
	isAdmin, ok := map[string]bool{"true": true, "false": false}[strings.TrimSpace(r.FormValue("admin"))]
	if !ok {
		http.Error(w, "the admin field must be true or false", http.StatusBadRequest)
		return
	}
	// The service refuses self-demotion (the page shows no Demote button
	// for the signed-in admin, so only a crafted POST gets here) and,
	// atomically in the store, the last administrator's; both are answered
	// from refusePage's table. Any other store error is logged with a
	// generic 500 body.
	if err := s.admin.SetUserAdmin(r.Context(), sessionActor(sess), userID, isAdmin); err != nil {
		if !s.refusePage(w, r, err) {
			s.serverError(w, r, "handleSetUserAdmin", fmt.Errorf("set admin=%t for user %d: %w", isAdmin, userID, err))
		}
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}
