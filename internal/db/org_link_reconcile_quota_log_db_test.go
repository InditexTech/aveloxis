// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/srctest"
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
			for _, field := range []string{" used=", " allowed=", " wanted="} {
				if !strings.Contains(line, field) {
					t.Errorf("the warning must carry the shared refusal vocabulary (%s):\n%s", field, line)
				}
			}
			if strings.Contains(line, "new_repositories_wanted=") {
				t.Errorf("the warning uses its own field name for what the quota was asked for:\n%s", line)
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

// Every site that logs a refusal's numbers spells them through
// capacity.Exceeded.LogAttrs (L18 / SR-17): one vocabulary for log
// filters. Two pins: the known sites carry the call, and no non-test
// source of the four packages that handle refusals puts an Exceeded
// field behind a log key of its own (`"<key>", ex.<Field>`), whatever
// the key.
func TestCapacityRefusalLogsShareOneVocabulary(t *testing.T) {
	store := srctest.StripGoComments(srctest.Read(t, "internal/db/capacity_store.go"))
	reconcile := srctest.StripGoComments(srctest.Read(t, "internal/db/org_link_reconcile.go"))
	api := srctest.StripGoComments(srctest.Read(t, "internal/api/capacity_limits.go"))
	autoAdd := srctest.StripGoComments(srctest.Read(t, "internal/api/auto_add_limit.go"))
	for _, site := range []struct{ src, sig string }{
		{store, "func (s *PostgresStore) noteShadowAllocation("},
		{store, "func (s *PostgresStore) noteShadowLinks("},
		{store, "func (s *PostgresStore) noteLinksRefused("},
		{store, "func (s *PostgresStore) noteAllocationRefused("},
		{store, "func (s *PostgresStore) admitSignup("},
		{reconcile, "func (s *PostgresStore) ReconcileOrgRepoLinks("},
		{api, "func (s *Server) refuseCapacity("},
		{autoAdd, "func (s *Server) refuseAutoAdd("},
	} {
		body := srctest.FuncBody(t, site.src, site.sig)
		if !strings.Contains(body, "ex.LogAttrs()") {
			t.Errorf("%s logs a refusal without ex.LogAttrs()", site.sig)
		}
		for _, own := range []string{`"reach"`, `"added_today"`, `"adding"`, `"signups_today"`, `"used"`, `"wanted"`} {
			if strings.Contains(body, own) {
				t.Errorf("%s spells a refusal field itself (%s)", site.sig, own)
			}
		}
	}
	// Any key, any variable (round 3 F3: the API holds a refusal as
	// `refused` or `res.Refusal`, so `ex.` alone was the author's spelling).
	ownKey := regexp.MustCompile(`"[A-Za-z_]+",\s*(\w+\.)+(Used|Allowed|Wanted)\b`)
	// The lines left apart, by file and exact text, each with its reason
	// and each required to still match (a permit entry nothing exercises
	// pins nothing; round 4 F2). The startup line logs every quota's
	// allowance from an EffectiveQuota — no refusal. logQuotaOver logs a
	// per-request rate quota's Decision once per window: under Enforce
	// that IS the refusal of requests_per_hour and the token quotas, and
	// the line carries quota, allowed, used and the window's end instead
	// of wanted (always one request). Its keys agree with LogAttrs; it is
	// left apart because it logs the Decision, not an Exceeded (round 4
	// F1: the L18 record states this boundary).
	leftApart := map[string]string{
		`internal/db/capacity_store.go: "allowed", q.Allowed`:         "LogCapacityInForce: the startup 'capacity quota in force' line",
		`internal/api/capacity_limits.go: "allowed", d.Quota.Allowed`: "logQuotaOver's Decision",
		`internal/api/capacity_limits.go: "used", d.Used`:             "logQuotaOver's Decision",
	}
	seen := map[string]bool{}
	for _, pkg := range []struct {
		dir   string
		floor int
	}{{"internal/db", 100}, {"internal/api", 24}, {"internal/web", 3}, {"internal/scheduler", 16}} {
		for name, src := range srctest.PackageFiles(t, pkg.dir, pkg.floor) {
			for _, m := range ownKey.FindAllString(srctest.StripGoComments(src), -1) {
				key := name + ": " + m
				if _, ok := leftApart[key]; ok {
					seen[key] = true
					continue
				}
				t.Errorf("%s logs a refusal field under a key of its own (%s); use ex.LogAttrs()", name, m)
			}
		}
	}
	for key, why := range leftApart {
		if !seen[key] {
			t.Errorf("the left-apart entry %q (%s) matches nothing any more: the line moved or was respelled, so the entry excuses nothing — update or remove it", key, why)
		}
	}
}
