// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/queueview"
)

// monitorPageSize is the web monitor's page size and its maximum: a
// smaller page_size is honoured and rides along in the page's links
// (whole-branch review of 0.29.109: the rows honoured it while the pager
// counted at 200).
const monitorPageSize = 200

// monitorSortKeys are the columns the page sorts by, in header order; each
// is a queueSorts allowlist key (the store's), so the link and the ORDER
// BY cannot drift apart.
var monitorSortKeys = []string{"repo", "status", "priority", "due", "last_run", "issues", "meta_issues", "prs", "meta_prs", "commits", "meta_commits"}

// handleMonitor is the web GUI's collection-queue page (administrators
// only since v0.29.91). Since 0.29.109 (stage 3d of summary/59) it reads
// the queue through the one view model, queueview: the parameters with
// their effective sort (D-22), the page's rows with the enrichment's
// failure mode, the pager, and the status counts from a cache rather than
// a GROUP BY per tab refresh.
func (s *Server) handleMonitor(w http.ResponseWriter, r *http.Request, sess *Session) {
	p := queueview.ParseParams(r.URL.Query(), monitorPageSize, monitorPageSize)
	stats, statsAt, statsNext, err := s.queueStats.Get(r.Context(), s.store.QueueStats)
	if err != nil {
		s.serverError(w, r, "handleMonitor", err)
		return
	}
	page, err := queueview.Load(r.Context(), s.store, p, s.logger)
	if err != nil {
		s.serverError(w, r, "handleMonitor", err)
		return
	}
	rows, total := page.Rows, page.Total
	totalPages := queueview.TotalPages(total, p.PageSize)
	now := time.Now()
	view := make([]monitorRow, 0, len(rows))
	for _, j := range rows {
		row := monitorRow{Row: j, Due: j.DueAt.Format("Jan 2 15:04"), LastRun: "-"}
		if j.DueAt.Before(now) && j.Status == "queued" {
			row.Due = "now"
		}
		if j.LastCollected != nil {
			row.LastRun = j.LastCollected.Format("Jan 2 15:04")
			if j.LastDurationMs > 0 {
				row.LastRun += fmt.Sprintf(" (%ds)", j.LastDurationMs/1000)
			}
		}
		if j.LockedBy != nil {
			row.Worker = *j.LockedBy
		}
		if j.LastError != nil {
			row.ErrInfo = *j.LastError
		}
		view = append(view, row)
	}
	s.render(w, r, "monitor", map[string]any{
		"Session":    sess,
		"Stats":      stats,
		"StatsAt":    statsAt.UTC().Format("15:04:05"),
		"StatsNext":  statsNext.UTC().Format("15:04:05"),
		"Warnings":   page.Warnings,
		"Jobs":       view,
		"Page":       p.Page,
		"TotalPages": totalPages,
		"Total":      total,
		"Query":      p.Search,
		"Sort":       p.Sort,
		"Dir":        p.Dir,
		"PageSize":   pageSizeParam(p),
		"SortHref":   queueview.SortHrefs("/monitor", p, monitorSortKeys),
		"PageWindow": queueview.Window(p.Page, totalPages),
	})
}

func (s *Server) handleMonitorPrioritize(w http.ResponseWriter, r *http.Request, sess *Session) {
	repoID, ok := pathID(r, "repoID")
	if !ok {
		http.Error(w, "invalid repo_id", http.StatusBadRequest)
		return
	}
	if err := s.store.PrioritizeRepo(r.Context(), repoID); err != nil {
		if errors.Is(err, db.ErrRepoNotInQueue) {
			http.Error(w, "repo not found in queue", http.StatusNotFound)
			return
		}
		s.serverError(w, r, "handleMonitorPrioritize", err)
		return
	}
	// Redirect back to the monitor page.
	http.Redirect(w, r, "/monitor", http.StatusFound)
}

// monitorRow is one queue row as the page renders it: the shared row plus
// the page's own words for the dates, the worker and the error.
type monitorRow struct {
	queueview.Row
	Due     string
	LastRun string
	Worker  string
	ErrInfo string
}

// pageSizeParam is the page_size a page's links carry: the given size
// when it differs from the page's default, else 0 (the template omits it).
func pageSizeParam(p queueview.Params) int {
	if p.SizeGiven {
		return p.PageSize
	}
	return 0
}
