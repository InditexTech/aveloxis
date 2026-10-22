// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"strings"
	"testing"
)

// E2 of summary/59 (stage 3e, 0.29.109): the compare page reads the API's
// one comparison — GET /api/v1/compare — and not per-repository
// time series it normalises itself. The series' definition (entity
// resolution, the window clamped to first activity, the cache, the
// Comparisons record) is the API's, the same for aveloxis-gui. The page
// keeps the display transform aveloxis-gui has: raw, or a z-score.
func TestComparePageReadsTheAPIsComparison(t *testing.T) {
	js := pageScripts(t)["compare.js"]
	if !strings.Contains(js, "/api/v1/compare?") {
		t.Error("compare.js must read /api/v1/compare")
	}
	if strings.Contains(js, "/timeseries") || strings.Contains(js, "/stats") {
		t.Error("compare.js fetches per-repository series itself; the API's comparison is the one definition")
	}
	for _, metric := range []string{"code_change_commits", "change_requests", "change_requests_merged", "issues"} {
		if !strings.Contains(js, "metric: '"+metric+"'") {
			t.Errorf("compare.js must chart the catalog metric %q", metric)
		}
	}
	if !strings.Contains(js, "entities=") || !strings.Contains(js, "'repo:' + r.id") || !strings.Contains(js, "bucket=week") {
		t.Error("compare.js must name the repositories as repo:<id> entities on a weekly bucket")
	}
	// The modes: raw and z-score, as aveloxis-gui; the "100% of max" mode
	// had no counterpart there and no definition in the API.
	tmpl := allTemplates
	start := strings.Index(tmpl, `{{define "compare"}}`)
	end := strings.Index(tmpl[start:], "{{end}}") + start
	page := tmpl[start:end]
	for _, want := range []string{`data-mode="raw"`, `data-mode="zscore"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the compare page lacks the %s mode button", want)
		}
	}
	// A stale answer never overwrites a newer selection (round-2 review
	// B-F3, the v0.29.66 class): refetchAll captures a generation and
	// drops answers whose generation moved on, before they are drawn.
	refetch := between(t, js, "function refetchAll()", "\n}\n")
	if !strings.Contains(refetch, "++fetchGeneration") {
		t.Error("refetchAll must capture a generation")
	}
	settle := between(t, refetch, ")).then(() => {", "});") // the block that assigns and draws
	guard, assign := strings.Index(settle, "generation !== fetchGeneration"), strings.Index(settle, "seriesByMetric = series")
	if guard < 0 || assign < 0 || guard > assign {
		t.Errorf("the settle block must drop an older generation BEFORE it assigns seriesByMetric (guard at %d, assignment at %d)", guard, assign)
	}
	catchBlock := between(t, refetch, ".catch(err => {", "})")
	cguard, message := strings.Index(catchBlock, "generation !== fetchGeneration"), strings.Index(catchBlock, "pageMessage(")
	if cguard < 0 || message < 0 || cguard > message {
		t.Errorf("a stale failure must not set the page message: the catch block needs the generation guard BEFORE pageMessage (guard at %d, message at %d)", cguard, message)
	}
	// A definitive refusal is answered in the API's words, not "try again"
	// (round-4 review): the refusal body travels with the thrown error.
	if !strings.Contains(catchBlock, "refusalMessage(err)") || !strings.Contains(js, "body.error === 'entity_out_of_scope'") {
		t.Error("the catch block must print refusalMessage(err), which reads the API's entity_out_of_scope body")
	}
	// One fetch path: the URL's repos= ids render their tags first and are
	// named from refetchAll's answers (round-3 review: a separate first
	// request left ids selected with no tag to remove them when it failed).
	prepop := between(t, js, "if (urlRepos.length > 0) {", "\n}\n")
	if strings.Contains(prepop, "fetch(") || !strings.Contains(prepop, "renderTags();") || !strings.Contains(prepop, "refetchAll();") {
		t.Error("the pre-populated selection must render its tags and go through refetchAll, never a fetch of its own")
	}
	if !strings.Contains(refetch, "adoptLabels(series)") {
		t.Error("refetchAll must name pre-populated repositories from its answers (adoptLabels)")
	}
	if strings.Contains(page, `data-mode="pct"`) || strings.Contains(js, "'pct'") {
		t.Error("the 100% mode is gone (E2): the page shows the API's comparison raw or as a z-score")
	}
}

// between returns src from the ONE start marker to the next end marker
// after it, failing (never panicking, never skipping) when the start is
// missing or not unique, or the end is missing: the subject moved, and the
// pin must say so. Uniqueness is the point (round-5 review of 0.29.109):
// a chain edit added a second ")).then(" ahead of the settle block, the
// first match moved the anchor onto the catch, and the ordering pin
// passed on the wrong block.
func between(t *testing.T, src, start, end string) string {
	t.Helper()
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("marker %q not found", start)
	}
	if n := strings.Count(src, start); n != 1 {
		t.Fatalf("marker %q matches %d places; the anchor must be unique", start, n)
	}
	j := strings.Index(src[i:], end)
	if j < 0 {
		t.Fatalf("marker %q not found after %q", end, start)
	}
	return src[i : i+j+len(end)]
}
