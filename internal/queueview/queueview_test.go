// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package queueview

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/model"
)

func TestParseParamsEchoesEffectiveValues(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want Params
	}{
		{"defaults", "", Params{Page: 1, PageSize: 200, Offset: 0}},
		{"page and size", "page=3&page_size=50", Params{Page: 3, PageSize: 50, Offset: 100, SizeGiven: true}},
		{"size clamped", "page_size=9999", Params{Page: 1, PageSize: 500, SizeGiven: true}},
		{"the default size given is not a given size", "page_size=200", Params{Page: 1, PageSize: 200}},
		{"bad numbers fall back", "page=-2&page_size=x", Params{Page: 1, PageSize: 200}},
		{"search trimmed", "q=%20kube%20", Params{Page: 1, PageSize: 200, Search: "kube"}},
		{"allowlisted sort, default direction", "sort=repo", Params{Page: 1, PageSize: 200, Sort: "repo", Dir: "asc"}},
		{"desc in any case", "sort=repo&dir=DESC", Params{Page: 1, PageSize: 200, Sort: "repo", Dir: "desc"}},
		{"unknown sort echoes no sort and no direction", "sort=evil&dir=desc", Params{Page: 1, PageSize: 200}},
		{"a direction without a sort is nothing", "dir=desc", Params{Page: 1, PageSize: 200}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, err := url.ParseQuery(c.q)
			if err != nil {
				t.Fatal(err)
			}
			if got := ParseParams(q, 200, 500); got != c.want {
				t.Errorf("ParseParams(%q) = %+v, want %+v", c.q, got, c.want)
			}
		})
	}
}

func TestTotalPagesAndWindow(t *testing.T) {
	for _, c := range []struct{ total, size, want int }{{0, 10, 1}, {10, 10, 1}, {11, 10, 2}, {500, 0, 1}, {-1, 10, 1}} {
		if got := TotalPages(c.total, c.size); got != c.want {
			t.Errorf("TotalPages(%d, %d) = %d, want %d", c.total, c.size, got, c.want)
		}
	}
	for _, c := range []struct {
		page, total int
		want        []int
	}{
		{1, 1, []int{1}}, {1, 3, []int{1, 2, 3}}, {1, 10, []int{1, 2, 3, 4, 5}}, {5, 10, []int{3, 4, 5, 6, 7}},
		{10, 10, []int{6, 7, 8, 9, 10}}, {9, 10, []int{6, 7, 8, 9, 10}}, {2, 0, []int{1}},
	} {
		if got := Window(c.page, c.total); !slices.Equal(got, c.want) {
			t.Errorf("Window(%d, %d) = %v, want %v", c.page, c.total, got, c.want)
		}
	}
}

func TestSortHrefsToggleTheCurrentKey(t *testing.T) {
	p := Params{Sort: "repo", Dir: "asc", Search: "kube"}
	h := SortHrefs("/monitor", p, []string{"repo", "status"})
	if h["repo"] != "/monitor?dir=desc&q=kube&sort=repo" {
		t.Errorf("repo (sorted asc) = %q; want the descending link with the search", h["repo"])
	}
	if h["status"] != "/monitor?dir=asc&q=kube&sort=status" {
		t.Errorf("status (not sorted) = %q; want ascending", h["status"])
	}
}

// fakeStore is one page of two jobs; the enrichment reads can be made to
// fail to prove the loader's failure mode.
type fakeStore struct {
	reposErr, statsErr error
	got                []any
}

func (f *fakeStore) ListQueuePage(_ context.Context, limit, offset int, search, sortKey, sortDir string) ([]db.QueueJob, int, error) {
	f.got = []any{limit, offset, search, sortKey, sortDir}
	return []db.QueueJob{{RepoID: 1, Status: "queued"}, {RepoID: 2, Status: "collecting"}}, 7, nil
}
func (f *fakeStore) GetReposBatch(context.Context, []int64) (map[int64]*model.Repo, error) {
	if f.reposErr != nil {
		return nil, f.reposErr
	}
	return map[int64]*model.Repo{1: {Owner: "example", Name: "golden", GitURL: "https://github.com/example/golden", Platform: model.PlatformGitHub}}, nil
}
func (f *fakeStore) GetRepoStatsBatch(context.Context, []int64) (map[int64]*db.RepoStats, error) {
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	return map[int64]*db.RepoStats{1: {GatheredIssues: 3, MetadataIssues: 4}}, nil
}

func TestLoadNumbersEnrichesAndPassesTheEffectiveSort(t *testing.T) {
	st := &fakeStore{}
	logs := &sink{}
	page, err := Load(context.Background(), st, Params{Page: 2, PageSize: 2, Offset: 2, Search: "gold", Sort: "repo", Dir: "desc"}, slog.New(slog.NewTextHandler(logs, nil)))
	rows, total := page.Rows, page.Total
	if err != nil || total != 7 || len(rows) != 2 || len(page.Warnings) != 0 {
		t.Fatalf("Load = %d rows, total %d, warnings %v, %v", len(rows), total, page.Warnings, err)
	}
	if got := st.got; got[0] != 2 || got[1] != 2 || got[2] != "gold" || got[3] != "repo" || got[4] != "desc" {
		t.Errorf("the store received %v; want the page's effective values", got)
	}
	if rows[0].RowNum != 3 || rows[1].RowNum != 4 {
		t.Errorf("row numbers %d, %d; want 3, 4 (numbered from the page's offset)", rows[0].RowNum, rows[1].RowNum)
	}
	if rows[0].Label() != "example/golden" || rows[0].Plat != "GitHub" || rows[0].GatheredIssues != 3 || rows[0].MetaIssues != 4 {
		t.Errorf("row 1 not enriched: %+v", rows[0])
	}
	if rows[1].Label() != "2" || rows[1].Owner != "" {
		t.Errorf("a repository without a row shows its id: %+v", rows[1])
	}
	if logs.String() != "" {
		t.Errorf("nothing failed, yet a line was logged:\n%s", logs.String())
	}
}

// Both enrichment reads fail: the page still answers, its rows bare (ids,
// zero counts) and its two warnings set so no page shows zeros as the
// truth, each failure logged once at WARN (D-17; 58 §7 item 10); a request
// that ended logs at DEBUG, never WARN.
func TestLoadLogsEachEnrichmentFailureAndStillAnswers(t *testing.T) {
	st := &fakeStore{reposErr: errors.New("names: closed pool"), statsErr: errors.New("stats: closed pool")}
	logs := &sink{}
	page, err := Load(context.Background(), st, Params{PageSize: 2}, slog.New(slog.NewTextHandler(logs, nil)))
	rows := page.Rows
	if err != nil || len(rows) != 2 || rows[0].Label() != "1" || rows[0].GatheredIssues != 0 {
		t.Fatalf("Load with failed enrichment = %+v, %v", rows, err)
	}
	if len(page.Warnings) != 2 || page.Warnings[0] != WarnNamesUnreadable || page.Warnings[1] != WarnCountsUnreadable {
		t.Errorf("warnings = %v; want both sentences", page.Warnings)
	}
	if strings.Count(logs.String(), "level=WARN") != 2 {
		t.Errorf("want two WARN lines:\n%s", logs.String())
	}
	for _, want := range []string{"repository names unreadable", "repository stats unreadable", "names: closed pool", "stats: closed pool"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the failure is not logged (%q missing):\n%s", want, logs.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logs = &sink{}
	st = &fakeStore{reposErr: ctx.Err(), statsErr: ctx.Err()}
	if _, err := Load(ctx, st, Params{PageSize: 2}, slog.New(slog.NewTextHandler(logs, nil))); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("an ended request's enrichment failure was logged at WARN:\n%s", logs.String())
	}
}

func TestStatsCacheServesStaleOnError(t *testing.T) {
	var logs sink
	c := NewStatsCache(time.Nanosecond, slog.New(slog.NewTextHandler(&logs, nil)))
	calls := 0
	ok := func(context.Context) (map[string]int, error) { calls++; return map[string]int{"total": calls}, nil }
	bad := func(context.Context) (map[string]int, error) { return nil, errors.New("closed pool") }
	if _, _, _, err := c.Get(context.Background(), bad); err == nil {
		t.Fatal("an empty cache with a failing loader must return the error")
	}
	if logs.String() != "" {
		t.Errorf("an empty cache returns the error to the caller (who logs it) and logs nothing itself: %q", logs.String())
	}
	got, filled, next, err := c.Get(context.Background(), ok)
	if err != nil || got["total"] != 1 || next.Sub(filled) != time.Nanosecond {
		t.Fatalf("first fill = %v, %v, %v, %v", got, filled, next, err)
	}
	time.Sleep(time.Millisecond) // past the TTL: the next Get refills
	if got, _, _, err := c.Get(context.Background(), bad); err != nil || got["total"] != 1 {
		t.Errorf("a failing refill must serve the held value: %v, %v", got, err)
	}
	// The failure is said (round-2 review of 0.29.109): WARN, with the
	// error and when the served counts were read.
	if line := logs.String(); !strings.Contains(line, "level=WARN") || !strings.Contains(line, "queue stats refresh failed") || !strings.Contains(line, "closed pool") || !strings.Contains(line, "filled_at=") {
		t.Errorf("a failed refill that serves stale counts must be logged at WARN with the error and the fill time; got %q", line)
	}
	c = NewStatsCache(time.Hour, slog.New(slog.NewTextHandler(&logs, nil)))
	calls = 0
	if _, _, _, err := c.Get(context.Background(), ok); err != nil {
		t.Fatal(err)
	}
	if got, _, _, _ := c.Get(context.Background(), ok); got["total"] != 1 || calls != 1 {
		t.Errorf("within the TTL the loader must not run again (calls %d)", calls)
	}
}

// sink is this package's one log sink, safe under -race (the api
// package's lockedBuffer of 0.29.105, same shape): tests read it while a
// handler may still write.
type sink struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *sink) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *sink) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// A page beyond what an int offset can address is clamped: the three
// queue pages answered 500 ("OFFSET must not be negative") for
// page=92233720368547760, and serve's dashboard is unauthenticated.
func TestParseParamsOffsetNeverOverflows(t *testing.T) {
	for _, page := range []string{"92233720368547760", "9223372036854775807", "99999999999999999999"} {
		p := ParseParams(url.Values{"page": {page}, "page_size": {"100"}}, 100, 100)
		if p.Offset < 0 || p.Page < 1 || (p.Page-1)*p.PageSize != p.Offset {
			t.Errorf("page=%s: page %d offset %d; want a non-negative offset of the clamped page", page, p.Page, p.Offset)
		}
	}
	if p := ParseParams(url.Values{"page": {"3"}, "page_size": {"25"}}, 100, 100); p.Page != 3 || p.Offset != 50 {
		t.Errorf("an ordinary page must not be clamped: %+v", p)
	}
	// A caller's non-positive bound is a precondition the parser keeps, not
	// a divide by zero (round-3 review): one row a page at least, the
	// maximum never below the default.
	for _, c := range []struct{ def, max, size, want int }{{0, 0, 0, 1}, {0, 0, 7, 1}, {-5, 100, 30, 30}, {50, 0, 80, 50}, {50, 10, 80, 50}} {
		q := url.Values{}
		if c.size > 0 {
			q.Set("page_size", strconv.Itoa(c.size))
		}
		if p := ParseParams(q, c.def, c.max); p.PageSize != c.want || p.Offset != 0 {
			t.Errorf("ParseParams(def %d, max %d, page_size %d) = %+v; want page size %d", c.def, c.max, c.size, p, c.want)
		}
	}
}

// An over-long search names nothing: no query runs (the serve dashboard
// is unauthenticated), the page is empty and says why; a given page size
// rides along in the sort links so the pager and the rows agree
// (whole-branch review of 0.29.109).
func TestOverLongSearchAnswersAnEmptyPageWithoutAQuery(t *testing.T) {
	p := ParseParams(url.Values{"q": {strings.Repeat("a", MaxSearchBytes+1)}}, 100, 100)
	if !p.SearchTooLong || p.Search != "" {
		t.Fatalf("ParseParams(over-long q) = %+v; want SearchTooLong with an empty Search", p)
	}
	if p := ParseParams(url.Values{"q": {strings.Repeat("a", MaxSearchBytes)}}, 100, 100); p.SearchTooLong || len(p.Search) != MaxSearchBytes {
		t.Errorf("a search at the bound is a search: %+v", p)
	}
	st := &fakeStore{}
	page, err := Load(context.Background(), st, p, slog.New(slog.NewTextHandler(&sink{}, nil)))
	if err != nil || page.Total != 0 || len(page.Rows) != 0 || len(page.Warnings) != 1 || page.Warnings[0] != WarnSearchTooLong {
		t.Errorf("Load(over-long search) = %+v, %v; want an empty page saying so", page, err)
	}
	if st.got != nil {
		t.Errorf("an over-long search must not reach the store; the store was asked %v", st.got)
	}
	given := ParseParams(url.Values{"page_size": {"25"}}, 100, 100)
	if !given.SizeGiven || strings.Contains(SortHrefs("/m", ParseParams(url.Values{"page_size": {"100"}}, 100, 100), []string{"repo"})["repo"], "page_size") {
		t.Error("page_size rides along only when it differs from the default")
	}
	if href := SortHrefs("/m", given, []string{"repo"})["repo"]; !strings.Contains(href, "page_size=25") {
		t.Errorf("a given page size must ride along in the sort links: %s", href)
	}
}
