// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// Stage 3d of summary/59 (0.29.109, WEB-10/WEB-11): every page of the
// collection queue reads it through internal/queueview — the parameters
// with their effective echo, the loader with its enrichment failure mode,
// and the pager. These pins keep the three consumers on it and keep a
// second copy of the pager arithmetic from growing back.
func TestQueuePagesShareTheViewModel(t *testing.T) {
	consumers := []struct{ dir, sig string }{
		{"internal/web", "func (s *Server) handleMonitor("},
		{"internal/monitor", "func (s *Server) handleDashboard("},
		{"internal/api", "func (s *Server) handleAdminMonitorQueue("},
	}
	for _, c := range consumers {
		body := srctest.StripGoComments(srctest.PackageFunc(t, c.dir, c.sig))
		if !strings.Contains(body, "queueview.Load(") {
			t.Errorf("%s %s must list and enrich the queue through queueview.Load", c.dir, c.sig)
		}
		if strings.Contains(body, "GetReposBatch(") || strings.Contains(body, "GetRepoStatsBatch(") || strings.Contains(body, "ListQueuePage(") {
			t.Errorf("%s %s reads the queue or its enrichment itself; queueview.Load is the one reader", c.dir, c.sig)
		}
	}
	// The parameters: the web page and the API parse through the one
	// parser; the serve dashboard keeps its own name over it (pinned in
	// its package to delegate).
	web := srctest.StripGoComments(srctest.PackageFunc(t, "internal/web", "func (s *Server) handleMonitor("))
	if !strings.Contains(web, "queueview.ParseParams(") {
		t.Error("internal/web handleMonitor must parse its parameters through queueview.ParseParams (a local parsePageParams would be a second parser)")
	}
	dashboard := srctest.StripGoComments(srctest.PackageFunc(t, "internal/monitor", "func (s *Server) handleDashboard("))
	if !strings.Contains(dashboard, "parsePageParams(") {
		t.Error("internal/monitor handleDashboard must parse through its parsePageParams, which delegates below")
	}
	delegate := srctest.StripGoComments(srctest.PackageFunc(t, "internal/monitor", "func parsePageParams("))
	if !strings.Contains(delegate, "queueview.ParseParams(") {
		t.Error("internal/monitor parsePageParams must delegate to queueview.ParseParams")
	}
	// The pager: one arithmetic (queueview.Window / TotalPages); the two
	// copies it replaced spelled a sliding window by hand.
	group := srctest.StripGoComments(srctest.PackageFunc(t, "internal/web", "func (s *Server) handleGroup("))
	if !strings.Contains(group, "queueview.Window(") || !strings.Contains(group, "queueview.TotalPages(") {
		t.Error("the group page must page through queueview.Window and queueview.TotalPages")
	}
	if !strings.Contains(group, "queueview.ParseParams(") || strings.Contains(group, "strconv.Atoi(") {
		t.Error("the group page must parse page, page_size and q through queueview.ParseParams (a bare Atoi overflowed the offset)")
	}
	examined := 0
	for dir, minFiles := range map[string]int{"internal/web": 10, "internal/monitor": 1, "internal/api": 10} {
		src := srctest.StripGoComments(srctest.PackageSource(t, dir, minFiles))
		examined++
		for _, needle := range []string{"winStart", "windowSize", "winEnd"} {
			if strings.Contains(src, needle) {
				t.Errorf("%s spells the pager window itself (%q); queueview.Window is the one pager", dir, needle)
			}
		}
	}
	srctest.MinCount(t, "packages swept for a second pager", examined, 3)
}
