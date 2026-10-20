// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

// Package web — admin.go provides the v0.19.0 admin pages:
//   - /admin/groups/pending  list + approve/reject of pending submissions
//   - /admin/users           list + toggle admin role
//
// All routes here are gated by Server.requireAdmin (session.go),
// which checks Session.IsAdmin. Non-admin authenticated users get a 403;
// unauthenticated users get redirected to /login.

package web

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aveloxis/aveloxis/internal/httpserver"
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

// handleAdminPendingGroups is the approvals page: the per-add approval
// queue (v0.27.20). The legacy pending-groups table is gone since
// 0.29.100 (N9, stage 1g of summary/59): the migrate converted every
// pending group at v0.27.20 and groups are born approved.
func (s *Server) handleAdminPendingGroups(w http.ResponseWriter, r *http.Request, sess *Session) {
	tok := s.csrfToken(w, r) // the form token for this page's forms (stage 2f); before any write
	requests, err := s.store.ListPendingAddRequests(r.Context())
	if err != nil {
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelWarn, err, "failed to list pending add-requests", "error", err)
		http.Error(w, "Failed to list pending additions", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Approvals</title>
<link rel="stylesheet" href="/static/app.css">
<style>
  body { font-family: system-ui, sans-serif; margin: 2rem; background: #f5f5f5; color: #333; }
  h1 { margin-bottom: 0.5rem; }
  h2 { margin-top: 2rem; }
  .sub { color: #666; margin-bottom: 1.5rem; }
  table { border-collapse: collapse; width: 100%; background: white; border-radius: 8px; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
  th, td { padding: 0.6rem 0.8rem; text-align: left; border-bottom: 1px solid #eee; font-size: 0.9rem; }
  th { background: #f8f8f8; font-weight: 600; }
  .btn { padding: 0.3rem 0.8rem; border: 1px solid #ddd; border-radius: 4px; cursor: pointer; font-size: 0.85rem; margin-right: 0.4rem; }
  .approve { background: #059669; color: white; border-color: #047857; }
  .reject { background: #dc2626; color: white; border-color: #b91c1c; }
  .empty { color: #6b7280; font-style: italic; padding: 2rem; text-align: center; }
  .navlinks a { margin-right: 1rem; }
  .urls { color: #6b7280; font-size: 0.8rem; overflow-wrap: anywhere; }
</style></head><body>
<h1>Approvals</h1>
<div class="sub navlinks">
  <a href="/dashboard">Back to dashboard</a>
  <a href="/admin/users">Users</a>
</div>
<h2>Pending additions</h2>
`)

	if len(requests) == 0 {
		fmt.Fprint(w, `<div class="empty">No pending additions awaiting review.</div>`)
	} else {
		fmt.Fprint(w, `<table class="tbl-stack"><thead><tr><th>#</th><th>Requested by</th><th>Group</th><th>Kind</th><th>Items</th><th>Sample</th><th>Action</th></tr></thead><tbody>`)
		for _, q := range requests {
			var sample string
			count := q.ItemCount
			if q.Kind == "org" {
				sample = template.HTMLEscapeString(q.OrgURL)
				count = 1
			} else {
				escaped := make([]string, 0, len(q.SampleURLs))
				for _, u := range q.SampleURLs {
					escaped = append(escaped, template.HTMLEscapeString(u))
				}
				sample = strings.Join(escaped, "<br>")
			}
			fmt.Fprintf(w, `<tr><td data-label="#">%d</td><td data-label="Requested by">%s</td><td data-label="Group">%s</td><td data-label="Kind">%s</td><td data-label="Items">%d</td><td class="urls" data-label="Sample">%s</td><td data-label="Action">
<form method="POST" action="/admin/add-requests/%d/approve" style="display:inline"><input type="hidden" name="csrf_token" value="%s"><button class="btn approve" type="submit">Approve</button></form>
<form method="POST" action="/admin/add-requests/%d/reject" style="display:inline"><input type="hidden" name="csrf_token" value="%s"><button class="btn reject" type="submit">Reject</button></form>
</td></tr>`,
				q.RequestID,
				template.HTMLEscapeString(q.UserLogin),
				template.HTMLEscapeString(q.GroupName),
				template.HTMLEscapeString(q.Kind),
				count, sample,
				q.RequestID, tok, q.RequestID, tok)
		}
		fmt.Fprint(w, `</tbody></table>`)
	}

	fmt.Fprint(w, `</body></html>`)
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

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request, sess *Session) {
	tok := s.csrfToken(w, r) // the form token for this page's forms (stage 2f); before any write
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelWarn, err, "failed to list users", "error", err)
		http.Error(w, "Failed to list users", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Users</title>
<link rel="stylesheet" href="/static/app.css">
<style>
  body { font-family: system-ui, sans-serif; margin: 2rem; background: #f5f5f5; color: #333; }
  h1 { margin-bottom: 0.5rem; }
  .sub { color: #666; margin-bottom: 1.5rem; }
  table { border-collapse: collapse; width: 100%; background: white; border-radius: 8px; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
  th, td { padding: 0.6rem 0.8rem; text-align: left; border-bottom: 1px solid #eee; font-size: 0.9rem; }
  th { background: #f8f8f8; font-weight: 600; }
  .btn { padding: 0.3rem 0.8rem; border: 1px solid #ddd; border-radius: 4px; cursor: pointer; font-size: 0.85rem; }
  .promote { background: #059669; color: white; border-color: #047857; }
  .demote { background: #dc2626; color: white; border-color: #b91c1c; }
  .badge { display: inline-block; padding: 0.15rem 0.5rem; border-radius: 4px; font-size: 0.75rem; background: #fef3c7; color: #78350f; }
  .badge.admin { background: #dbeafe; color: #1e40af; }
  .self { color: #6b7280; font-size: 0.8rem; }
  .navlinks a { margin-right: 1rem; }
</style></head><body>
<h1>Users</h1>
<div class="sub navlinks">
  <a href="/dashboard">Back to dashboard</a>
  <a href="/admin/groups/pending">Approvals</a>
</div>
<table class="tbl-stack"><thead><tr><th>ID</th><th>Login</th><th>Email</th><th>Provider</th><th>Role</th><th>Action</th></tr></thead><tbody>`)

	for _, u := range users {
		role := `<span class="badge">user</span>`
		if u.IsAdmin {
			role = `<span class="badge admin">admin</span>`
		}
		action := ""
		if u.UserID == sess.UserID {
			action = `<span class="self">(you)</span>`
		} else if u.IsAdmin {
			action = fmt.Sprintf(`<form method="POST" action="/admin/users/%d/admin" style="display:inline"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="admin" value="false"><button class="btn demote" type="submit">Demote</button></form>`, u.UserID, tok)
		} else {
			action = fmt.Sprintf(`<form method="POST" action="/admin/users/%d/admin" style="display:inline"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="admin" value="true"><button class="btn promote" type="submit">Promote to admin</button></form>`, u.UserID, tok)
		}
		fmt.Fprintf(w, `<tr><td data-label="ID">%d</td><td data-label="Login">%s</td><td data-label="Email">%s</td><td data-label="Provider">%s</td><td data-label="Role">%s</td><td data-label="Action">%s</td></tr>`,
			u.UserID,
			template.HTMLEscapeString(u.Login),
			template.HTMLEscapeString(u.Email),
			template.HTMLEscapeString(u.Provider),
			role, action)
	}
	fmt.Fprint(w, `</tbody></table></body></html>`)
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
