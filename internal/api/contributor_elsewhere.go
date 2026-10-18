// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/httpserver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// v0.27.64 — the read API over the v0.27.58 contributor daily
// history. Two surfaces:
//
//   - GET /repos/{repoID}/contributors/elsewhere — "where else are
//     this repo's top contributors active?" (authorizeRepo: it hangs
//     off a repo view).
//   - GET /contributors/{cntrbID}/activity — one person's cross-repo
//     monthly view. requireUser, NOT repo scope: person-level data
//     spans repos this instance doesn't even track, so repo scope
//     cannot express it — and anonymous access can't either.
//
// Both ride the shared 60s response cache and both carry
// backfilled_at so the frontend can render "history pending" instead
// of lying with zeros.

const elsewhereWindowDays = 180 // the v0.27.58 default history window

func (s *Server) handleContributorsElsewhere(w http.ResponseWriter, r *http.Request) {
	repoID, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	since := db.UTCDay(time.Now()).AddDate(0, 0, -elsewhereWindowDays) // a UTC midnight: the daily commit table serves it (summary/49)
	if t, given, err := queryDay("since", r.URL.Query().Get("since")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest) // D7 (stage 1b); before the cache, so a bad date is never a key
		return
	} else if given {
		since = t
	}
	limit := 10
	if lp := r.URL.Query().Get("limit"); lp != "" {
		if n, err := strconv.Atoi(lp); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 25 {
		limit = 25
	}
	// v0.27.69: keep the cohort consistent with /contributors/top when
	// the caller hides bots there.
	excludeBots := r.URL.Query().Get("bots") == "hide"

	// The cache holds the full answer; an API token gets it scoped to its
	// owner's groups (ASVS review G3), filtered after the cache.
	info, _ := callerIdentity(w, r)
	key := fmt.Sprintf("elsewhere|%d|%s|%d|%t", repoID, since.Format("2006-01-02"), limit, excludeBots)
	body, ok := s.respCache.get(key)
	if !ok {
		rows, err := s.store.ContributorsElsewhere(r.Context(), repoID, since, limit, 10, excludeBots)
		if err != nil {
			s.serverError(w, r, "handleContributorsElsewhere", err)
			return
		}
		if body, ok = s.marshalBody(w, r, "handleContributorsElsewhere", elsewhereAnswer{Since: since.Format("2006-01-02"), Limit: limit, Contributors: rows}); !ok {
			return
		}
		s.respCache.put(key, body)
	}
	if info.APITokenID != 0 {
		var a elsewhereAnswer
		if err := json.Unmarshal(body, &a); err != nil {
			s.serverError(w, r, "handleContributorsElsewhere", err)
			return
		}
		a.Contributors = scopeElsewhere(a.Contributors, info.Scope)
		if body, ok = s.marshalBody(w, r, "handleContributorsElsewhere", a); !ok {
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleContributorActivity(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	cntrbID := r.PathValue("cntrbID")
	if _, err := uuid.Parse(cntrbID); err != nil {
		http.Error(w, "invalid cntrb_id (must be a UUID)", http.StatusBadRequest)
		return
	}
	months := 24
	if mp := r.URL.Query().Get("months"); mp != "" {
		if n, err := strconv.Atoi(mp); err == nil && n > 0 {
			months = n
		}
	}
	if months > 60 {
		months = 60
	}
	// bucket is accepted for forward compatibility; only month exists.
	if b := r.URL.Query().Get("bucket"); b != "" && b != "month" {
		http.Error(w, "unsupported bucket (only 'month')", http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("cactivity|%s|%d", cntrbID, months)
	if body, ok := s.respCache.get(key); ok {
		s.writeActivity(w, r, info, body)
		return
	}

	view, err := s.store.ContributorActivity(r.Context(), cntrbID, months)
	if err != nil {
		// Copilot review, PR #171: only a genuine no-rows lookup is
		// "not found" — the id parsed but nobody has it. Every other
		// error is an operational failure (DB down, query bug) and
		// must surface as a logged 500, never masquerade as a 404.
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "contributor not found", http.StatusNotFound)
			return
		}
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelError, err, "contributor activity lookup failed", "cntrb_id", logSafe(cntrbID), "error", err)
		http.Error(w, "contributor activity lookup failed", http.StatusInternalServerError)
		return
	}
	body, ok := s.marshalBody(w, r, "handleContributorActivity", view)
	if !ok {
		return
	}
	s.respCache.put(key, body)
	s.writeActivity(w, r, info, body)
}

// elsewhereAnswer is the /contributors/elsewhere body (cached whole).
type elsewhereAnswer struct {
	Since        string                    `json:"since"`
	Limit        int                       `json:"limit"`
	Contributors []db.ElsewhereContributor `json:"contributors"`
}

// writeActivity writes a contributor-activity body, scoped for an API token
// (ASVS review G3; the cache holds the full answer).
func (s *Server) writeActivity(w http.ResponseWriter, r *http.Request, info authInfo, body []byte) {
	if info.APITokenID != 0 {
		var v db.ContributorActivityView
		if err := json.Unmarshal(body, &v); err != nil {
			s.serverError(w, r, "handleContributorActivity", err)
			return
		}
		var ok bool
		if body, ok = s.marshalBody(w, r, "handleContributorActivity", scopeActivity(&v, info.Scope)); !ok {
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
