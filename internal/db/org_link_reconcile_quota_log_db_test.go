// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/capacity"
)

// reconcileWarnLine returns the "not linked" WARN line for user, or "".
func reconcileWarnLine(logs, user string) string {
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "not linked") && strings.Contains(line, "user_id="+user+" ") {
			return line
		}
	}
	return ""
}

// Copilot review 5559632343 (PR #229, r4304130873): the reconcile's warning
// said "the account's repository quota is reached" and logged today's
// additions as "reach" whenever linkOrgFill refused — but an auto-approved
// organization's links count toward repo_links_per_day (v0.29.91), and that
// quota refuses through the same path. The line must name the quota that
// refused and label the number generically.
func TestReconcileNamesTheQuotaThatRefused(t *testing.T) {
	cases := []struct {
		name        string
		reposAllow  int
		linksAllow  int
		wantQuota   string
		wantLinked  int
		forbidWords []string
	}{
		{"daily additions refuse", 100, 1, capacity.QuotaRepoLinksPerDay, 1, []string{"repository quota is reached", "reach="}},
		{"repository allocation refuses", 1, 100, capacity.QuotaReposPerAccount, 1, []string{"reach="}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAllocFixture(t, false, tc.reposAllow, 3, capacity.Enforce)
			setLinksQuota(t, f, tc.linksAllow, capacity.Enforce)
			registerOrgForTest(t, f, f.group, 0) // auto-approved: its links count
			var logs strings.Builder
			f.store.logger = slog.New(slog.NewTextHandler(&lockedWriter{w: &logs}, nil))
			if _, err := f.store.ReconcileOrgRepoLinks(f.ctx); err != nil {
				t.Fatal(err)
			}
			if got := f.linked(t); got != tc.wantLinked {
				t.Errorf("reach after the reconcile = %d; want %d (the fill links what fits)", got, tc.wantLinked)
			}
			line := reconcileWarnLine(logs.String(), strconv.Itoa(f.user))
			if line == "" {
				t.Fatalf("no 'not linked' warning for user %d in:\n%s", f.user, logs.String())
			}
			if !strings.Contains(line, "quota="+tc.wantQuota) {
				t.Errorf("the warning does not name the quota that refused (want quota=%s):\n%s", tc.wantQuota, line)
			}
			if !strings.Contains(line, " used=") {
				t.Errorf("the warning must carry the refused quota's usage as used=:\n%s", line)
			}
			for _, w := range tc.forbidWords {
				if strings.Contains(line, w) {
					t.Errorf("the warning claims %q, which is wrong for a %s refusal:\n%s", w, tc.wantQuota, line)
				}
			}
		})
	}
}

// The once-a-day key is per account AND per quota: a daily-additions
// refusal in the morning must not silence a repository-allocation refusal
// of the same account in the afternoon.
func TestReconcileWarnsOncePerQuotaPerDay(t *testing.T) {
	f := newAllocFixture(t, false, 2, 3, capacity.Enforce)
	setLinksQuota(t, f, 1, capacity.Enforce)
	registerOrgForTest(t, f, f.group, 0)
	var logs strings.Builder
	f.store.logger = slog.New(slog.NewTextHandler(&lockedWriter{w: &logs}, nil))
	if _, err := f.store.ReconcileOrgRepoLinks(f.ctx); err != nil {
		t.Fatal(err)
	}
	first := reconcileWarnLine(logs.String(), strconv.Itoa(f.user))
	if !strings.Contains(first, "quota="+capacity.QuotaRepoLinksPerDay) {
		t.Fatalf("first refusal should be the daily quota (1 of 3 fits today):\n%s", logs.String())
	}
	// A new day for the daily counter: the allocation (2) is now the one
	// that refuses the third repository.
	cleanupExecRetry(f.ctx, f.store, `DELETE FROM aveloxis_ops.request_counts WHERE subject = $1`, linksTodayKey(f.user))
	logs.Reset()
	if _, err := f.store.ReconcileOrgRepoLinks(f.ctx); err != nil {
		t.Fatal(err)
	}
	second := reconcileWarnLine(logs.String(), strconv.Itoa(f.user))
	if !strings.Contains(second, "quota="+capacity.QuotaReposPerAccount) {
		t.Errorf("the allocation refusal was not logged (the daily refusal's once-a-day key silenced it):\n%s", logs.String())
	}
}
