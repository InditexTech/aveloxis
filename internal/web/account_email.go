// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aveloxis/aveloxis/internal/admin"
	"github.com/aveloxis/aveloxis/internal/httpserver"
)

// The account e-mail pages. The flow itself — whether a confirmation link
// can be mailed, what is stored, the token's classification — is the admin
// service's (admin/account.go, moved there in stage 1 of summary/59 so the
// API and this process share one decision); these handlers render it.

// handleAccountEmail renders (GET) and processes (POST) the
// email-collection form. This is the v0.19.10 fallback when both /user
// and /user/emails came back empty during OAuth callback. A submitted
// address is stored as email_pending, a click-to-confirm link is mailed
// (v0.20.4), and the user redirects to /dashboard; users.email changes
// only when the link is followed.
func (s *Server) handleAccountEmailSubmit(w http.ResponseWriter, r *http.Request, sess *Session) {
	// The recovery address changes only after a recent sign-in (ASVS
	// V7.5.1, 0.29.113): an older session is sent to sign in again and
	// come back here.
	if !s.recentSignIn(sess) {
		s.render(w, r, "account_email", map[string]any{
			"Session": sess,
			"Error":   fmt.Sprintf("To change the address, sign in again first: this session signed in more than %d minutes ago.", int(s.cfg.SessionIdle().Minutes())),
			"Reauth":  true,
		})
		return
	}
	if msg := s.admin.SubmitAccountEmail(r.Context(), sessionActor(sess), sess.LoginName, r.FormValue("email"), linkOrigin(r)); msg != "" {
		s.render(w, r, "account_email", map[string]any{
			"Session": sess,
			"Error":   msg,
		})
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

// handleAccountEmailForm shows the form (GET /account/email).
func (s *Server) handleAccountEmailForm(w http.ResponseWriter, r *http.Request, sess *Session) {

	// If the user already has a confirmed email,
	// they don't need to be here — bounce them back to dashboard.
	email, err := s.store.GetUserEmail(r.Context(), sess.UserID)
	if err != nil {
		// Showing the form is the safe fallback; the error is still logged.
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelWarn, err, "account email form: could not read the user's email", "user_id", sess.UserID, "error", err)
	}
	if strings.TrimSpace(email) != "" {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	// The confirm handler sends users here with a reason; say it.
	var notice string
	switch {
	case r.URL.Query().Get("expired") == "1":
		notice = "That confirmation link has expired, was already used, or belongs to another account. Enter your email to get a new one."
	case r.URL.Query().Get("error") == "1":
		notice = "We couldn't confirm your email just now. Try the link again, or enter your email to get a new one."
	}
	s.render(w, r, "account_email", map[string]any{
		"Session": sess,
		"Error":   notice,
	})
}

// handleEmailConfirm confirms the v0.20.4 click-to-confirm link for the
// logged-in user and redirects to the dashboard. A link that is not a live
// token for THIS account — unknown, expired, used, or another account's —
// redirects to /account/email?expired=1 and changes nothing (the owner's
// link stays usable; another account's still-live link is logged at WARN);
// a database failure redirects to ?error=1 with the transaction rolled
// back, so the link still works. The form explains both.
func (s *Server) handleEmailConfirm(w http.ResponseWriter, r *http.Request, sess *Session) {
	switch s.admin.ConfirmAccountEmail(r.Context(), sessionActor(sess), r.URL.Query().Get("token")) {
	case admin.ConfirmConfirmed:
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	case admin.ConfirmError:
		http.Redirect(w, r, "/account/email?error=1", http.StatusFound)
	default:
		http.Redirect(w, r, "/account/email?expired=1", http.StatusFound)
	}
}
