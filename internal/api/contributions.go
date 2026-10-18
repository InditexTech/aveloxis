// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"net/http"
)

// handleRepoContributors returns all distinct contributors who made any
// kind of contribution to the repo in the requested window. See the
// store-side GetRepoContributors docstring and docs/guide/api.md for
// the full inclusion list and known limitations.
func (s *Server) handleRepoContributors(w http.ResponseWriter, r *http.Request) {
	repoID, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	since, until, err := parseWindow(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	contribs, err := s.store.GetRepoContributors(r.Context(), repoID, since, until)
	if err != nil {
		s.serverError(w, r, "handleRepoContributors", err)
		return
	}

	s.writeJSON(w, r, contribs)
}

// handleRepoContributionsCoverage returns the enrichment-state
// snapshot for the same cohort as /identities and /affiliations: how
// many contributors have been enriched / have a canonical email / have
// a resolved gh_user_id / have an affiliation. The "are my numbers
// trustworthy?" endpoint — operators read this before drawing
// conclusions from /affiliations. See store-side
// GetRepoContributionsCoverage and docs/guide/api.md for the full
// semantics of each field.
func (s *Server) handleRepoContributionsCoverage(w http.ResponseWriter, r *http.Request) {
	repoID, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	since, until, err := parseWindow(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cov, err := s.store.GetRepoContributionsCoverage(r.Context(), repoID, since, until)
	if err != nil {
		s.serverError(w, r, "handleRepoContributionsCoverage", err)
		return
	}

	s.writeJSON(w, r, cov)
}

// handleRepoAffiliations returns the per-affiliation contributor count
// for the same window. The "(unknown)" bucket is included in the
// response so the caller can decide whether to surface or hide it.
func (s *Server) handleRepoAffiliations(w http.ResponseWriter, r *http.Request) {
	repoID, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	since, until, err := parseWindow(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	counts, err := s.store.GetRepoAffiliationCounts(r.Context(), repoID, since, until)
	if err != nil {
		s.serverError(w, r, "handleRepoAffiliations", err)
		return
	}

	s.writeJSON(w, r, counts)
}
