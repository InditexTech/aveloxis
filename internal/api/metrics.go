// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

// Package api — metrics.go implements Augur-compatible REST API endpoints
// for CHAOSS metrics. These endpoints follow the swagger.json specification
// ported from Augur's Python API.
//
// Endpoint patterns:
//
//	/api/v1/repos/{repoID}/{metric}           — per-repo metric
//	/api/v1/repo-groups/{groupID}/{metric}     — per-group metric (aggregated)
//
// Common query parameters:
//
//	begin_date (YYYY-MM-DD) — first day of the window, default a year before today (UTC)
//	end_date   (YYYY-MM-DD) — last day of the window, inclusive (D4), default today (UTC)
//	period     (day|week|month|year) — aggregation period, default month
//
// A date that does not parse is a 400 (D7); an empty result is [] (D6);
// an unknown repository is a 404 and one outside an API token's groups a
// 403 (D5, the prelude repoFromPath). Stage 1b, summary/59 §5b.
//
// Excluded: Login endpoints, DEI Badging endpoints (per task spec).
package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
)

// registerMetricRoutes registers all Augur-compatible metric endpoints.
func (s *Server) registerMetricRoutes() {
	// === Utility / Lookup ===
	s.mux.HandleFunc("GET /api/v1/repo-groups", s.handleRepoGroups)
	s.mux.HandleFunc("GET /api/v1/repos", s.handleAllRepos)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}", s.handleRepoByID)
	s.mux.HandleFunc("GET /api/v1/repo-groups/{groupID}/repos", s.handleReposByGroup)
	s.mux.HandleFunc("GET /api/v1/owner/{owner}/repo/{repo}", s.handleRepoByOwnerName)
	s.mux.HandleFunc("GET /api/v1/rg-name/{rgName}", s.handleRepoGroupByName)
	s.mux.HandleFunc("GET /api/v1/rg-name/{rgName}/repo-name/{repoName}", s.handleRepoByGroupAndName)

	// === Issue Metrics ===
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/issues-new", s.handleIssuesNew)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/issues-closed", s.handleIssuesClosed)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/issues-active", s.handleIssuesActive)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/issue-backlog", s.handleIssueBacklog)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/issue-throughput", s.handleIssueThroughput)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/issue-duration", s.handleIssueDuration)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/average-issue-resolution-time", s.handleAvgIssueResolution)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/abandoned-issues", s.handleAbandonedIssues)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/open-issues-count", s.handleOpenIssuesCount)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/closed-issues-count", s.handleClosedIssuesCount)

	// === Pull Request Metrics ===
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/pull-requests-new", s.handlePRsNew)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/reviews", s.handleReviews)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/reviews-accepted", s.handleReviewsAccepted)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/reviews-declined", s.handleReviewsDeclined)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/review-duration", s.handleReviewDuration)

	// === Commit Metrics ===
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/committers", s.handleCommitters)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/code-changes", s.handleCodeChanges)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/code-changes-lines", s.handleCodeChangesLines)

	// === Contributor Metrics ===
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/contributors", s.handleContributors)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/contributors-new", s.handleContributorsNew)

	// === Repo Metadata ===
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/stars", s.handleStars)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/stars-count", s.handleStarsCount)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/forks", s.handleForks)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/fork-count", s.handleForkCount)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/watchers", s.handleWatchers)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/watchers-count", s.handleWatchersCount)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/languages", s.handleLanguages)

	// === Dependencies ===
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/deps", s.cachedRepoGET(pageExact, s.handleDeps))
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/libyear", s.cachedRepoGET(pageExact, s.handleDeps)) // Same as deps — libyear data is in the same table.

	// === Messages ===
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/repo-messages", s.handleRepoMessages)

	// === Releases ===
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/releases", s.handleReleases)

	// === Complexity (SCC) ===
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/project-languages", s.handleProjectLanguages)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/project-files", s.handleProjectLanguages)
	s.mux.HandleFunc("GET /api/v1/repos/{repoID}/project-lines", s.handleProjectLanguages)
}

// ============================================================
// Common parameter parsing
// ============================================================

func parseGroupID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("groupID"), 10, 64)
}

// parsePeriod extracts the aggregation period from query params.
// Valid: day, week, month, year. Default: month.
func parsePeriod(r *http.Request) string {
	p := r.URL.Query().Get("period")
	switch p {
	case "day", "week", "month", "year":
		return p
	default:
		return "month"
	}
}

// ============================================================
// Utility / Lookup handlers
// ============================================================

// catalogScope is what the caller may see of the Augur catalog (stage 1b,
// D1): an API token sees its owner's groups only; a session (which is
// linked to a repository on view everywhere else), an administrator and a
// caller without an identity see the fleet, as the search route does.
func catalogScope(w http.ResponseWriter, r *http.Request) db.RepoScope {
	info, ok := callerIdentity(w, r)
	if !ok || info.IsAdmin || info.APITokenID == 0 {
		return db.RepoScope{All: true}
	}
	ids := make([]int64, 0, len(info.Scope))
	for id := range info.Scope {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return db.RepoScope{IDs: ids}
}

// catalogLookupRefused answers a by-name lookup's typed failures: 404 for
// no such repository, the structured 403 (naming the repository) when the
// name exists only outside the caller's scope (N21), and reports whether
// it answered. Any other failure is the handler's 500.
func (s *Server) catalogLookupRefused(w http.ResponseWriter, r *http.Request, err error) bool {
	var oos *db.RepoOutOfScopeError
	switch {
	case errors.Is(err, db.ErrRepoNotFound):
		refuseRepoNotFound(w)
	case errors.As(err, &oos):
		info, _ := callerIdentity(w, r)
		s.refuseTokenOutOfScope(w, r, info, oos.RepoID)
	default:
		return false
	}
	return true
}

func (s *Server) handleRepoGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListRepoGroups(r.Context(), catalogScope(w, r))
	if err != nil {
		s.serverError(w, r, "handleRepoGroups", err)
		return
	}
	s.writeJSON(w, r, groups)
}

func (s *Server) handleAllRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := s.store.ListRepos(r.Context(), catalogScope(w, r))
	if err != nil {
		s.serverError(w, r, "handleAllRepos", err)
		return
	}
	s.writeJSON(w, r, repos)
}

func (s *Server) handleRepoByID(w http.ResponseWriter, r *http.Request) {
	repoID, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	repo, err := s.store.GetRepoByID(r.Context(), repoID)
	if errors.Is(err, db.ErrRepoNotFound) {
		refuseRepoNotFound(w) // deleted between the prelude and this read
		return
	}
	if err != nil {
		s.serverError(w, r, "handleRepoByID", err)
		return
	}
	s.writeJSON(w, r, repo)
}

func (s *Server) handleReposByGroup(w http.ResponseWriter, r *http.Request) {
	groupID, err := parseGroupID(r)
	if err != nil {
		http.Error(w, "invalid repo_group_id", http.StatusBadRequest)
		return
	}
	repos, err := s.store.GetReposByGroup(r.Context(), groupID, catalogScope(w, r))
	if err != nil {
		s.serverError(w, r, "handleReposByGroup", err)
		return
	}
	s.writeJSON(w, r, repos)
}

func (s *Server) handleRepoByOwnerName(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	name := r.PathValue("repo")
	repo, err := s.store.GetRepoByOwnerName(r.Context(), owner, name, catalogScope(w, r))
	if s.catalogLookupRefused(w, r, err) {
		return
	}
	if err != nil {
		s.serverError(w, r, "handleRepoByOwnerName", err)
		return
	}
	s.writeJSON(w, r, repo)
}

func (s *Server) handleRepoGroupByName(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("rgName")
	group, err := s.store.GetRepoGroupByName(r.Context(), name, catalogScope(w, r))
	if errors.Is(err, db.ErrRepoGroupNotFound) {
		http.Error(w, "repo group not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.serverError(w, r, "handleRepoGroupByName", err)
		return
	}
	s.writeJSON(w, r, group)
}

func (s *Server) handleRepoByGroupAndName(w http.ResponseWriter, r *http.Request) {
	// The group first (scoped: a group with nothing in scope is not
	// found), then its repository by name — without regard to letter
	// case, like the owner/name lookup (stage 1b).
	rgName := r.PathValue("rgName")
	repoName := r.PathValue("repoName")
	scope := catalogScope(w, r)
	group, err := s.store.GetRepoGroupByName(r.Context(), rgName, scope)
	if errors.Is(err, db.ErrRepoGroupNotFound) {
		http.Error(w, "repo group not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.serverError(w, r, "handleRepoByGroupAndName", err)
		return
	}
	repo, err := s.store.GetRepoInGroupByName(r.Context(), group.ID, repoName, scope)
	if s.catalogLookupRefused(w, r, err) {
		return
	}
	if err != nil {
		s.serverError(w, r, "handleRepoByGroupAndName", err)
		return
	}
	s.writeJSON(w, r, repo)
}

// ============================================================
// Metric helpers (stage 1b, eval-4 item 3)
// ============================================================

// metricWindow is what a windowed metric reads from its query: Augur's
// period, and the half-open [lo, hi) window parseDateRange returns (D4:
// end_date is inclusive, so hi is the midnight after it; D7: an
// unparseable date is a 400 before anything is read).
type metricWindow struct {
	period string
	lo, hi time.Time
}

// serveRepoSeries serves a windowed metric in one order: the prelude
// (repoFromPath: 400, 404, 403), the window (400 on a bad date), the fetch,
// nil → [] (D6), one JSON writer. Every metric handler is one call to a
// serve helper, with inferred type arguments: the openapi reach analysis
// (scripts/openapi_routes_test.go) follows identifier calls, never
// f[T](...), so a handler's reachable statuses and query reads stay
// visible to it.
func serveRepoSeries[T any](s *Server, w http.ResponseWriter, r *http.Request, name string, fetch func(ctx context.Context, repoID int64, mw metricWindow) ([]T, error)) {
	repoID, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	lo, hi, err := parseDateRange(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := fetch(r.Context(), repoID, metricWindow{period: parsePeriod(r), lo: lo, hi: hi})
	if err != nil {
		s.serverError(w, r, name, err)
		return
	}
	if rows == nil {
		rows = []T{}
	}
	s.writeJSON(w, r, rows)
}

// serveRepoWindow serves a windowed list that is not bucketed (durations,
// contributor summaries): as serveRepoSeries, without reading period.
func serveRepoWindow[T any](s *Server, w http.ResponseWriter, r *http.Request, name string, fetch func(ctx context.Context, repoID int64, lo, hi time.Time) ([]T, error)) {
	repoID, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	lo, hi, err := parseDateRange(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := fetch(r.Context(), repoID, lo, hi)
	if err != nil {
		s.serverError(w, r, name, err)
		return
	}
	if rows == nil {
		rows = []T{}
	}
	s.writeJSON(w, r, rows)
}

// serveRepoList serves a list metric without a window: the prelude, the
// fetch, nil → [] (D6), one JSON writer.
func serveRepoList[T any](s *Server, w http.ResponseWriter, r *http.Request, name string, fetch func(ctx context.Context, repoID int64) ([]T, error)) {
	repoID, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	rows, err := fetch(r.Context(), repoID)
	if err != nil {
		s.serverError(w, r, name, err)
		return
	}
	if rows == nil {
		rows = []T{}
	}
	s.writeJSON(w, r, rows)
}

// serveRepoValue serves a single-value metric (a count, a ratio, a
// record): the prelude, the fetch, one JSON writer.
func serveRepoValue[T any](s *Server, w http.ResponseWriter, r *http.Request, name string, fetch func(ctx context.Context, repoID int64) (T, error)) {
	repoID, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	v, err := fetch(r.Context(), repoID)
	if err != nil {
		s.serverError(w, r, name, err)
		return
	}
	s.writeJSON(w, r, v)
}

// ============================================================
// Issue metric handlers
// ============================================================

func (s *Server) handleIssuesNew(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handleIssuesNew", func(ctx context.Context, id int64, mw metricWindow) ([]db.MetricRow, error) {
		return s.store.IssuesNew(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

func (s *Server) handleIssuesClosed(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handleIssuesClosed", func(ctx context.Context, id int64, mw metricWindow) ([]db.MetricRow, error) {
		return s.store.IssuesClosed(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

func (s *Server) handleIssuesActive(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handleIssuesActive", func(ctx context.Context, id int64, mw metricWindow) ([]db.MetricRow, error) {
		return s.store.IssuesActive(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

func (s *Server) handleIssueBacklog(w http.ResponseWriter, r *http.Request) {
	serveRepoValue(s, w, r, "handleIssueBacklog", func(ctx context.Context, id int64) (map[string]int, error) {
		count, err := s.store.IssueBacklog(ctx, id)
		return map[string]int{"issue_backlog": count}, err
	})
}

func (s *Server) handleIssueThroughput(w http.ResponseWriter, r *http.Request) {
	serveRepoValue(s, w, r, "handleIssueThroughput", func(ctx context.Context, id int64) (map[string]float64, error) {
		throughput, err := s.store.IssueThroughput(ctx, id)
		return map[string]float64{"throughput": throughput}, err
	})
}

func (s *Server) handleIssueDuration(w http.ResponseWriter, r *http.Request) {
	serveRepoWindow(s, w, r, "handleIssueDuration", s.store.IssueDuration)
}

func (s *Server) handleAvgIssueResolution(w http.ResponseWriter, r *http.Request) {
	serveRepoValue(s, w, r, "handleAvgIssueResolution", func(ctx context.Context, id int64) (map[string]float64, error) {
		avg, err := s.store.AverageIssueResolutionTime(ctx, id)
		return map[string]float64{"avg_issue_resolution_days": avg}, err
	})
}

func (s *Server) handleAbandonedIssues(w http.ResponseWriter, r *http.Request) {
	serveRepoList(s, w, r, "handleAbandonedIssues", s.store.AbandonedIssues)
}

func (s *Server) handleOpenIssuesCount(w http.ResponseWriter, r *http.Request) {
	serveRepoValue(s, w, r, "handleOpenIssuesCount", func(ctx context.Context, id int64) (map[string]int, error) {
		count, err := s.store.IssueBacklog(ctx, id)
		return map[string]int{"open_count": count}, err
	})
}

func (s *Server) handleClosedIssuesCount(w http.ResponseWriter, r *http.Request) {
	// v0.27.36: a DB error must be a 500, never {"closed_count": 0}
	// served as real data (summary/18 Phase 0b).
	serveRepoValue(s, w, r, "handleClosedIssuesCount", func(ctx context.Context, id int64) (map[string]int, error) {
		count, err := s.store.ClosedIssueCount(ctx, id)
		return map[string]int{"closed_count": count}, err
	})
}

// ============================================================
// PR metric handlers
// ============================================================

func (s *Server) handlePRsNew(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handlePRsNew", func(ctx context.Context, id int64, mw metricWindow) ([]db.MetricRow, error) {
		return s.store.PRsNew(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	// "reviews" in Augur is equivalent to PRs created.
	s.handlePRsNew(w, r)
}

func (s *Server) handleReviewsAccepted(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handleReviewsAccepted", func(ctx context.Context, id int64, mw metricWindow) ([]db.MetricRow, error) {
		return s.store.ReviewsAccepted(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

func (s *Server) handleReviewsDeclined(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handleReviewsDeclined", func(ctx context.Context, id int64, mw metricWindow) ([]db.MetricRow, error) {
		return s.store.ReviewsDeclined(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

func (s *Server) handleReviewDuration(w http.ResponseWriter, r *http.Request) {
	serveRepoWindow(s, w, r, "handleReviewDuration", s.store.ReviewDuration)
}

// ============================================================
// Commit metric handlers
// ============================================================

func (s *Server) handleCommitters(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handleCommitters", func(ctx context.Context, id int64, mw metricWindow) ([]db.MetricRow, error) {
		return s.store.Committers(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

func (s *Server) handleCodeChanges(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handleCodeChanges", func(ctx context.Context, id int64, mw metricWindow) ([]db.MetricRow, error) {
		return s.store.CodeChanges(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

func (s *Server) handleCodeChangesLines(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handleCodeChangesLines", func(ctx context.Context, id int64, mw metricWindow) ([]db.CodeChangesLinesRow, error) {
		return s.store.CodeChangesLines(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

// ============================================================
// Contributor metric handlers
// ============================================================

func (s *Server) handleContributors(w http.ResponseWriter, r *http.Request) {
	serveRepoWindow(s, w, r, "handleContributors", s.store.Contributors)
}

func (s *Server) handleContributorsNew(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handleContributorsNew", func(ctx context.Context, id int64, mw metricWindow) ([]db.MetricRow, error) {
		return s.store.ContributorsNew(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

// ============================================================
// Repo metadata handlers
// ============================================================

// latestCountOf is the body of the three *-count routes: the latest
// snapshot's column under key, with the repository's name.
func (s *Server) latestCountOf(column, key string) func(ctx context.Context, id int64) (map[string]any, error) {
	return func(ctx context.Context, id int64) (map[string]any, error) {
		count, name, err := s.store.LatestCount(ctx, id, column)
		return map[string]any{"repo_name": name, key: count}, err
	}
}

func (s *Server) handleStars(w http.ResponseWriter, r *http.Request) {
	serveRepoList(s, w, r, "handleStars", func(ctx context.Context, id int64) ([]db.MetricRow, error) {
		return s.store.SnapshotSeries(ctx, id, "star_count")
	})
}

func (s *Server) handleStarsCount(w http.ResponseWriter, r *http.Request) {
	serveRepoValue(s, w, r, "handleStarsCount", s.latestCountOf("star_count", "stars"))
}

func (s *Server) handleForks(w http.ResponseWriter, r *http.Request) {
	serveRepoList(s, w, r, "handleForks", func(ctx context.Context, id int64) ([]db.MetricRow, error) {
		return s.store.SnapshotSeries(ctx, id, "fork_count")
	})
}

func (s *Server) handleForkCount(w http.ResponseWriter, r *http.Request) {
	serveRepoValue(s, w, r, "handleForkCount", s.latestCountOf("fork_count", "forks"))
}

// watcherRow is /watchers' row: Augur's own key, `watchers`, where the
// stars and forks series carry `value` (D3, the one exception to D9).
type watcherRow struct {
	Date     time.Time `json:"date"`
	Watchers int       `json:"watchers"`
	Name     string    `json:"repo_name"`
}

func (s *Server) handleWatchers(w http.ResponseWriter, r *http.Request) {
	// The same snapshot history as stars and forks (stage 1b: this route
	// read only the latest snapshot, so it had at most one point).
	serveRepoList(s, w, r, "handleWatchers", func(ctx context.Context, id int64) ([]watcherRow, error) {
		rows, err := s.store.SnapshotSeries(ctx, id, "watcher_count")
		if err != nil {
			return nil, err
		}
		out := make([]watcherRow, 0, len(rows))
		for _, m := range rows {
			out = append(out, watcherRow{Date: m.Date, Watchers: m.Value, Name: m.Name})
		}
		return out, nil
	})
}

func (s *Server) handleWatchersCount(w http.ResponseWriter, r *http.Request) {
	serveRepoValue(s, w, r, "handleWatchersCount", s.latestCountOf("watcher_count", "watchers"))
}

func (s *Server) handleLanguages(w http.ResponseWriter, r *http.Request) {
	serveRepoValue(s, w, r, "handleLanguages", func(ctx context.Context, id int64) (map[string]any, error) {
		lang, err := s.store.Languages(ctx, id)
		return map[string]any{"repo_id": id, "primary_language": lang}, err
	})
}

// ============================================================
// Dependency handlers
// ============================================================

func (s *Server) handleDeps(w http.ResponseWriter, r *http.Request) {
	repoID, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	// v0.28.1 (item 7a): ?license=<canonical>&scope=runtime|all turn
	// this into the licenses-table drill-down (DepsFiltered mirrors
	// the aggregate's row universe). With NEITHER param, the legacy
	// full raw list is served unchanged for existing consumers.
	licFilter := r.URL.Query().Get("license")
	scope := r.URL.Query().Get("scope")
	if scope != "" && scope != "all" && scope != "runtime" {
		http.Error(w, `scope must be "all" or "runtime"`, http.StatusBadRequest)
		return
	}
	var data []db.DepRow
	var err error
	if licFilter == "" && scope == "" {
		data, err = s.store.Deps(r.Context(), repoID)
	} else {
		data, err = s.store.DepsFiltered(r.Context(), repoID, licFilter, scope == "runtime")
	}
	if err != nil {
		s.serverError(w, r, "handleDeps", err)
		return
	}
	s.writeJSON(w, r, data)
}

// ============================================================
// Message handlers
// ============================================================

func (s *Server) handleRepoMessages(w http.ResponseWriter, r *http.Request) {
	serveRepoSeries(s, w, r, "handleRepoMessages", func(ctx context.Context, id int64, mw metricWindow) ([]db.MetricRow, error) {
		return s.store.RepoMessages(ctx, id, mw.period, mw.lo, mw.hi)
	})
}

// ============================================================
// Release handlers
// ============================================================

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	serveRepoList(s, w, r, "handleReleases", s.store.Releases)
}

// ============================================================
// Complexity handlers
// ============================================================

func (s *Server) handleProjectLanguages(w http.ResponseWriter, r *http.Request) {
	serveRepoList(s, w, r, "handleProjectLanguages", s.store.ProjectLanguages)
}
