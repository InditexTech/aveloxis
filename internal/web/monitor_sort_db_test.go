// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/queueview"
)

// The monitor page sorts through the store's allowlist and echoes the
// EFFECTIVE sort (D-22, stage 3d): a known key shows its arrow and flips
// its own link, an unknown key shows no sort at all (never the caller's
// word), and the status counts come with the time they were read.
func TestMonitorPageSortsThroughTheAllowlistAndShowsCachedCounts(t *testing.T) {
	f := newConsoleFixture(t)
	w := f.get("/monitor?sort=repo&dir=desc&q=zzz", "admin-tok")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /monitor sorted = %d\n%s", w.Code, w.Body.String())
	}
	page := w.Body.String()
	for _, want := range []string{
		`href="/monitor?dir=asc&amp;q=zzz&amp;sort=repo">Repository</a> <span class="sort-arrow">&#9662;</span>`, // sorted desc: its link flips to asc
		`href="/monitor?dir=asc&amp;q=zzz&amp;sort=status">Status</a>`,                                           // another column: ascending, with the search
		"counts as of ",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the sorted monitor page lacks %q", want)
		}
	}
	w = f.get("/monitor?sort=evil&dir=desc", "admin-tok")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "sort-arrow") || strings.Contains(w.Body.String(), "evil") {
		t.Errorf("an unknown sort key must show no sort and never its word (%d)", w.Code)
	}
	// A given page size rides along in every link, so the pager counts at
	// the size the rows show (whole-branch review of 0.29.109); the page
	// needs two queue rows for a pager to exist.
	f.seedQueueRows(t, 2)
	w = f.get("/monitor?page_size=1&sort=repo", "admin-tok")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `href="/monitor?page=2&sort=repo&dir=asc&page_size=1">Next</a>`) || !strings.Contains(w.Body.String(), `href="/monitor?dir=desc&amp;page_size=1&amp;sort=repo">Repository</a>`) {
		t.Errorf("GET /monitor?page_size=1 = %d; want the pager's Next and the sort links carrying page_size=1\n%s", w.Code, w.Body.String())
	}
	if w := f.get("/monitor?q="+strings.Repeat("a", queueview.MaxSearchBytes+1), "admin-tok"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "longer than any repository name") {
		t.Errorf("an over-long search = %d; want 200 saying nothing matches", w.Code)
	}
}

// seedQueueRows inserts n repositories with queue rows for the fixture's
// duration and returns their ids.
func (f *consoleFixture) seedQueueRows(t *testing.T, n int) []int64 {
	t.Helper()
	ctx := context.Background()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		url := fmt.Sprintf("https://github.com/_avmon%d/r%d", time.Now().UnixNano(), i)
		var id int64
		if err := f.store.Pool().QueryRow(ctx, `INSERT INTO aveloxis_data.repos (repo_git, repo_owner, repo_name, platform_id, repo_group_id) VALUES ($1, 'o', 'r', 1, 1) RETURNING repo_id`, url).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.Pool().Exec(ctx, `INSERT INTO aveloxis_ops.collection_queue (repo_id, status, priority, due_at) VALUES ($1, 'queued', 0, NOW())`, id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_ops.collection_queue WHERE repo_id = ANY($1)`, ids)
		_, _ = f.store.Pool().Exec(context.Background(), `DELETE FROM aveloxis_data.repos WHERE repo_id = ANY($1)`, ids)
	})
	return ids
}
