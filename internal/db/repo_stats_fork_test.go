// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

// v0.27.79: the repo-page stats payload carries fork lineage so the
// GUI can render the "Forked from X" chip (operator request
// 2026-08-02, enabled by the v0.27.78 fork capture).

package db

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRepoStatsCarriesForkedFrom(t *testing.T) {
	b, err := os.ReadFile("repo_stats.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, `ForkedFrom string `+"`"+`json:"forked_from,omitempty"`+"`") {
		t.Error("RepoStats must carry ForkedFrom with the forked_from JSON tag")
	}
	idx := strings.Index(src, "func (s *PostgresStore) GetRepoStatsBatch(")
	if idx < 0 {
		t.Fatal("GetRepoStatsBatch missing")
	}
	// v0.28.1 (A6): repo_gone_at rides the same repos read, so the
	// needle covers the widened SELECT. v0.29.70 (item 82) widened it again
	// (the forge notice columns sit between the two), so the pin asserts
	// the one statement reads both, not their adjacency.
	// Stage 1b made the batch the one reader, so both ride its queue JOIN
	// repos statement (and the queueless fallback's).
	stmt := regexp.MustCompile(`r\.repo_gone_at,[^;` + "`" + `]*COALESCE\(r\.forked_from, ''\)[^;` + "`" + `]*FROM aveloxis_ops\.collection_queue q\s+JOIN aveloxis_data\.repos r`)
	if !stmt.MatchString(src[idx:]) {
		t.Error("GetRepoStatsBatch must read repos.forked_from and repo_gone_at in its queue-joined read — without the read the GUI chips are a permanent no-op")
	}
	if strings.Count(src, "COALESCE(r.forked_from, '')") != 2 {
		t.Error("the queueless fallback reads forked_from too (one answer shape for both cohorts)")
	}
}
