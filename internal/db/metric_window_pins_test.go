// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"regexp"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// windowedMetricReaders are the Augur metric readers that take a window
// (stage 1b item 4, eval-4): each is found by its declaration, wherever it
// lives.
var windowedMetricReaders = []string{
	"IssuesNew", "IssuesClosed", "IssuesActive", "IssueDuration",
	"PRsNew", "ReviewsAccepted", "ReviewsDeclined", "ReviewDuration",
	"Committers", "CodeChanges", "CodeChangesLines",
	"Contributors", "ContributorsNew", "RepoMessages",
}

// The windowed readers compare half-open (`>= $lo AND < $hi`) and never
// BETWEEN (an inclusive end double-counts the midnight instant), and never
// read the TEXT cmt_author_date (the empty-string row that failed the
// query, 22007; and the author's local day, which /timeseries does not
// use). Scoped by semantics: every `>= $n` in a reader has its `< $n` twin.
// The denominator is the readers examined and the predicates found.
func TestMetricReadersUseHalfOpenWindows(t *testing.T) {
	// `col >= $n` or `col >= ($n::…` (ContributorsNew compares a DATE to the
	// bound's UTC day).
	lower := regexp.MustCompile(`(\w+(?:\.\w+)?) >= \(?\$(\d+)`)
	strict := regexp.MustCompile(`(\w+(?:\.\w+)?) > \(?\$(\d+)`) // an exclusive lower bound drops the window's first instant (review round 1)
	predicates := 0
	for _, fn := range windowedMetricReaders {
		body := srctest.StripGoComments(srctest.PackageFunc(t, "internal/db", "func (s *PostgresStore) "+fn+"("))
		if strings.Contains(body, "BETWEEN") {
			t.Errorf("%s uses BETWEEN: a window is `>= $lo AND < $hi`", fn)
		}
		if strings.Contains(body, "cmt_author_date") {
			t.Errorf("%s reads cmt_author_date: the commit readers count by cmt_author_timestamp's UTC day (N19)", fn)
		}
		if m := strict.FindStringSubmatch(body); m != nil {
			t.Errorf("%s: `%s > $%s` — a window's lower bound is inclusive (`>=`)", fn, m[1], m[2])
		}
		found := lower.FindAllStringSubmatch(body, -1)
		if len(found) == 0 {
			t.Errorf("%s has no `>= $n` window predicate: the reader is not windowed as listed", fn)
		}
		for _, m := range found {
			predicates++
			if !strings.Contains(body, m[1]+" < $") && !strings.Contains(body, m[1]+" < ($") {
				t.Errorf("%s: `%s >= $%s` has no `%s < $n` twin: a window's upper bound is exclusive", fn, m[1], m[2], m[1])
			}
		}
	}
	srctest.MinCount(t, "windowed predicates in the metric readers", predicates, 14)
	// Package-wide: nothing casts the TEXT author date to a DATE any more.
	if src := srctest.StripGoComments(srctest.PackageSource(t, "internal/db", 100)); strings.Contains(src, "cmt_author_date::") {
		t.Error("internal/db casts cmt_author_date: the TEXT date is never read as a DATE (its empty rows fail the query)")
	}
}
