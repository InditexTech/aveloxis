// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/srctest"
	"github.com/aveloxis/aveloxis/internal/srctest/sqlscan"
)

// Copilot review 5485402444 (PR #229): SetAPITokenSettings saved each
// changed quota through SetCapacityQuota, which commits its own
// transaction, and then the lifetime in a third statement. A failure
// after the first write left defaults the administrator never submitted.
// The three writes are one transaction: all saved, or nothing.

// apiTokenSettingsSnapshot is everything SetAPITokenSettings writes: the
// two quota rows, the lifetime, and the 0.29.82-0.29.88 rollback mirror.
type apiTokenSettingsSnapshot struct {
	hour, day int
	settings  APITokenSettings
	mirror    int
}

func readAPITokenSettingsSnapshot(ctx context.Context, t *testing.T, store *PostgresStore) apiTokenSettingsSnapshot {
	t.Helper()
	var snap apiTokenSettingsSnapshot
	q, err := store.GetCapacityQuotas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap.hour = q[capacity.QuotaTokenRequestsPerHour].Allowed
	snap.day = q[capacity.QuotaTokenRequestsPerDay].Allowed
	if snap.settings, err = store.GetAPITokenSettings(ctx); err != nil {
		t.Fatal(err)
	}
	snap.settings.UpdatedAt = snap.settings.UpdatedAt.Truncate(0) // compare the fields a write changes
	if err := store.pool.QueryRow(ctx, `SELECT default_rate_limit_per_hour FROM aveloxis_ops.api_token_settings WHERE id = 1`).Scan(&snap.mirror); err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestAPITokenSettingsUpdateIsAtomic(t *testing.T) {
	store, ctx := tokenTestStore(t)
	admin := tokenTestUser(t, store, ctx, "avx-it-apitok-atomic")
	before := readAPITokenSettingsSnapshot(ctx, t, store)
	t.Cleanup(func() {
		store.apiTokenSettingsWrite = nil
		_ = store.SetAPITokenSettings(context.Background(), APITokenSettings{DefaultRateLimitPerHour: before.hour,
			DefaultRateLimitPerDay: before.day, DefaultLifetimeDays: before.settings.DefaultLifetimeDays}, 0)
	})

	injected := errors.New("injected settings write failure")
	store.apiTokenSettingsWrite = func() error { return injected }
	err := store.SetAPITokenSettings(ctx, APITokenSettings{
		DefaultRateLimitPerHour: before.hour + 1,
		DefaultRateLimitPerDay:  before.day + 1,
		DefaultLifetimeDays:     before.settings.DefaultLifetimeDays + 1,
	}, admin)
	if !errors.Is(err, injected) {
		t.Fatalf("SetAPITokenSettings with the write seam failing = %v; want the injected failure", err)
	}
	after := readAPITokenSettingsSnapshot(ctx, t, store)
	if after.hour != before.hour || after.day != before.day {
		t.Errorf("a failed settings update changed the quotas: hour %d→%d, day %d→%d; the update must be all or nothing",
			before.hour, after.hour, before.day, after.day)
	}
	if after.mirror != before.mirror {
		t.Errorf("a failed settings update changed the rollback mirror default_rate_limit_per_hour %d→%d", before.mirror, after.mirror)
	}
	if after.settings.DefaultLifetimeDays != before.settings.DefaultLifetimeDays || after.settings.UpdatedBy != before.settings.UpdatedBy {
		t.Errorf("a failed settings update changed the lifetime row: %+v → %+v", before.settings, after.settings)
	}

	// The seam cleared, the same update lands whole.
	store.apiTokenSettingsWrite = nil
	if err := store.SetAPITokenSettings(ctx, APITokenSettings{
		DefaultRateLimitPerHour: before.hour + 1,
		DefaultRateLimitPerDay:  before.day + 1,
		DefaultLifetimeDays:     before.settings.DefaultLifetimeDays + 1,
	}, admin); err != nil {
		t.Fatal(err)
	}
	got := readAPITokenSettingsSnapshot(ctx, t, store)
	if got.hour != before.hour+1 || got.day != before.day+1 || got.mirror != before.hour+1 ||
		got.settings.DefaultLifetimeDays != before.settings.DefaultLifetimeDays+1 || got.settings.UpdatedBy != admin {
		t.Errorf("after the update: %+v; want every value one higher and updated_by=%d", got, admin)
	}
}

// TestConcurrentSettingsSavesNeverDeadlock (L10 round 3 F1): a save that
// changes the hour and the day, interleaved with a save that changes only
// the day. With the rollback mirror written BETWEEN the two quota rows, A
// held hour + api_token_settings and waited for day while B held day and
// waited for api_token_settings: 40P01, and a 500 for a legitimate save.
// Every writer now takes the quota rows in name order and
// api_token_settings last, so the two saves wait on each other at most
// once. The seam parks A after its first quota row; B runs meanwhile; A
// is released once B has finished or is waiting on a lock.
func TestConcurrentSettingsSavesNeverDeadlock(t *testing.T) {
	store, ctx := tokenTestStore(t)
	admin := tokenTestUser(t, store, ctx, "avx-it-apitok-deadlock")
	before := readAPITokenSettingsSnapshot(ctx, t, store)
	t.Cleanup(func() {
		store.capacityQuotaWritten = nil
		_ = store.SetAPITokenSettings(context.Background(), APITokenSettings{DefaultRateLimitPerHour: before.hour,
			DefaultRateLimitPerDay: before.day, DefaultLifetimeDays: before.settings.DefaultLifetimeDays}, 0)
	})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	parked, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unpark := func() { releaseOnce.Do(func() { close(release) }) }
	var wg sync.WaitGroup
	// Registered AFTER the restore cleanup, so it runs BEFORE it (LIFO): a
	// Fatal while A is parked must unpark A and let its transaction end,
	// or the restore above waits forever on the row A holds and the
	// package hangs to go test's timeout (L10 round 4 F2).
	t.Cleanup(func() { unpark(); wg.Wait() })
	// A parks after its FIRST quota row, whichever name the writer takes
	// first. Only the first caller parks; a later caller passes straight
	// through (a sync.Once would block it in Go, where the lock poll
	// below cannot see it; L10 round 4 F1).
	var parkedOnce atomic.Bool
	store.capacityQuotaWritten = func(string) {
		if parkedOnce.CompareAndSwap(false, true) {
			close(parked)
			<-release
		}
	}
	var aErr, bErr error
	wg.Add(2) // A and B both; the cleanup's wg.Wait covers both (round 5 F3)
	go func() {
		defer wg.Done()
		aErr = store.SetAPITokenSettings(ctx, APITokenSettings{
			DefaultRateLimitPerHour: before.hour + 1, DefaultRateLimitPerDay: before.day + 1,
			DefaultLifetimeDays: before.settings.DefaultLifetimeDays + 1}, admin)
	}()
	select {
	case <-parked:
	case <-ctx.Done():
		t.Fatal("A never reached its first quota write")
	}
	bDone := make(chan struct{})
	go func() {
		defer wg.Done()
		defer close(bDone)
		bErr = store.SetAPITokenSettings(ctx, APITokenSettings{
			DefaultRateLimitPerHour: before.hour, // unchanged
			DefaultRateLimitPerDay:  before.day + 2,
			DefaultLifetimeDays:     before.settings.DefaultLifetimeDays + 1}, admin)
	}()
	// Release A once B is done (the fixed order lets it through) or B is
	// waiting on a lock A holds (the old order; releasing A then closes
	// the cycle, and one of them fails with 40P01).
	deadline := time.After(10 * time.Second)
	bFinishedFirst := false
wait:
	for {
		select {
		case <-bDone:
			bFinishedFirst = true
			break wait
		case <-deadline:
			t.Error("B neither finished nor waited on a lock within 10 s")
			break wait
		case <-time.After(50 * time.Millisecond):
			var waiting int
			if err := store.pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				break wait
			}
		}
	}
	unpark()
	wg.Wait()
	<-bDone
	if aErr != nil || bErr != nil {
		t.Fatalf("two concurrent saves must both succeed: A=%v B=%v", aErr, bErr)
	}
	// Whichever order the writer takes, the end state is one save on top
	// of the other, never a torn one: A's hour stands (B read it as
	// unchanged before A committed) and the mirror follows it; the day is
	// the later committer's — B's if B waited on a row A held, A's if B
	// finished before A was released (the writer's name order decides
	// which; the test does not pin it, round 5 F1).
	got := readAPITokenSettingsSnapshot(ctx, t, store)
	wantDay := before.day + 2 // B committed after A
	if bFinishedFirst {
		wantDay = before.day + 1 // A committed last
	}
	if got.hour != before.hour+1 || got.day != wantDay || got.mirror != before.hour+1 {
		t.Errorf("after both saves: %+v; want hour %d (A), day %d (B finished first: %v), mirror %d", got, before.hour+1, wantDay, bFinishedFirst, before.hour+1)
	}
}

// TestCapacityQuotaWriterRefusesInsideATransaction: the admission rule
// runs in the writer itself (SR-18; review 0.29.94 F2), so a caller that
// skips the pre-Begin check still cannot land an unknown quota or change
// one aveloxis.json owns — and nothing is written, not even the writes
// before the refused one in the same call.
func TestCapacityQuotaWriterRefusesInsideATransaction(t *testing.T) {
	store, ctx := tokenTestStore(t)
	store.SetCapacitySources(map[string]capacity.Source{capacity.QuotaTokenRequestsPerDay: capacity.SourceDefault})
	t.Cleanup(func() { store.SetCapacitySources(nil) })
	before, err := store.GetCapacityQuotas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hourBefore := before[capacity.QuotaTokenRequestsPerHour]
	for _, c := range []quotaWrite{
		{"no_such_quota", 5, capacity.Enforce},
		{capacity.QuotaTokenRequestsPerDay, before[capacity.QuotaTokenRequestsPerDay].Allowed + 1, capacity.Enforce}, // config-owned
		{capacity.QuotaTokenRequestsPerHour, 0, capacity.Enforce},
		{capacity.QuotaTokenRequestsPerHour, 5, capacity.Mode("loud")},
	} {
		tx, err := store.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// A valid hour write rides in front of the refused one: the call
		// must refuse before writing anything.
		err = store.writeCapacityQuotasTx(ctx, tx, []quotaWrite{{capacity.QuotaTokenRequestsPerHour, hourBefore.Allowed + 7, hourBefore.Mode}, c}, 0)
		if !errors.Is(err, ErrInvalidCapacitySettings) {
			t.Errorf("writeCapacityQuotasTx(…, %+v) = %v; want ErrInvalidCapacitySettings from the writer itself", c, err)
		}
		var hourInTx int
		if qerr := tx.QueryRow(ctx, `SELECT allowed FROM aveloxis_ops.capacity_quotas WHERE name = $1`, capacity.QuotaTokenRequestsPerHour).Scan(&hourInTx); qerr != nil {
			t.Fatal(qerr)
		}
		_ = tx.Rollback(ctx)
		if hourInTx != hourBefore.Allowed {
			t.Errorf("the valid write before the refused one landed in the transaction (hour %d→%d); every write is validated before any is made", hourBefore.Allowed, hourInTx)
		}
	}
	var unknown int
	if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM aveloxis_ops.capacity_quotas WHERE name = 'no_such_quota'`).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if unknown != 0 {
		t.Errorf("an unknown quota row was written (%d rows)", unknown)
	}
	after, err := store.GetCapacityQuotas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after[capacity.QuotaTokenRequestsPerDay] != before[capacity.QuotaTokenRequestsPerDay] || after[capacity.QuotaTokenRequestsPerHour] != hourBefore {
		t.Errorf("a refused call changed a quota: %+v → %+v", before, after)
	}
}

// TestAPITokenSettingsSeamsDefaultToNil pins the seams' production value:
// a non-nil default would fail or park every settings save.
func TestAPITokenSettingsSeamsDefaultToNil(t *testing.T) {
	s := &PostgresStore{}
	if s.apiTokenSettingsWrite != nil || s.capacityQuotaWritten != nil {
		t.Fatal("apiTokenSettingsWrite and capacityQuotaWritten must be nil in production")
	}
}

// TestAPITokenSettingsWriteInOneTransaction is the source side: the
// settings writer opens one transaction and writes its quotas through the
// one transaction-aware writer, never through SetCapacityQuota (which
// commits on its own); that writer is the only SQL site that changes a
// capacity_quotas row, validates before it writes, and writes the mirror
// after the last quota row. The lock-order PROPERTY is pinned by
// TestConcurrentSettingsSavesNeverDeadlock; this test pins the wiring.
func TestAPITokenSettingsWriteInOneTransaction(t *testing.T) {
	body := srctest.StripGoComments(srctest.FuncBody(t, srctest.Read(t, "internal/db/api_tokens.go"), "func (s *PostgresStore) SetAPITokenSettings("))
	if n := strings.Count(body, ".Begin("); n != 1 {
		t.Errorf("SetAPITokenSettings opens %d transactions; want exactly one so the quotas and the lifetime are saved together", n)
	}
	if strings.Contains(body, "s.SetCapacityQuota(") {
		t.Error("SetAPITokenSettings calls SetCapacityQuota, which commits its own transaction; it must write through writeCapacityQuotasTx inside its one transaction")
	}
	if !strings.Contains(body, "s.apiTokenSettingsWrite") {
		t.Error("the apiTokenSettingsWrite seam must sit inside SetAPITokenSettings, or the atomicity test tests nothing")
	}
	// The one writer call, the seam and the lifetime row run inside the
	// transaction in that order: Begin, quotas, seam, lifetime, Commit —
	// api_token_settings after the last quota row (the lock order).
	begin := strings.Index(body, ".Begin(")
	quotas := strings.Index(body, "s.writeCapacityQuotasTx(")
	seam := strings.Index(body, "s.apiTokenSettingsWrite")
	lifetime := strings.Index(body, "INSERT INTO aveloxis_ops.api_token_settings")
	commit := strings.Index(body, ".Commit(")
	if !(begin >= 0 && begin < quotas && quotas < seam && seam < lifetime && lifetime < commit) {
		t.Errorf("want Begin < writeCapacityQuotasTx < seam < lifetime upsert < Commit; got %d %d %d %d %d", begin, quotas, seam, lifetime, commit)
	}
	public := srctest.StripGoComments(srctest.FuncBody(t, srctest.Read(t, "internal/db/capacity_store.go"), "func (s *PostgresStore) SetCapacityQuota("))
	if strings.Contains(public, "INSERT INTO aveloxis_ops.capacity_quotas") || !strings.Contains(public, "writeCapacityQuotasTx(") {
		t.Error("SetCapacityQuota must write through writeCapacityQuotasTx, the one capacity_quotas writer")
	}
	// The admission rule lives in the writer (SR-18), not in a caller
	// convention; the mirror follows the last quota row (lock order).
	writer := srctest.StripGoComments(srctest.FuncBody(t, srctest.Read(t, "internal/db/capacity_store.go"), "func (s *PostgresStore) writeCapacityQuotasTx("))
	v, ins := strings.Index(writer, "s.validateCapacityQuota("), strings.Index(writer, "INSERT INTO")
	if !(v >= 0 && ins >= 0 && v < ins) {
		t.Error("writeCapacityQuotasTx must run validateCapacityQuota before its INSERT")
	}
	if mirror := strings.Index(writer, "UPDATE aveloxis_ops.api_token_settings"); !(mirror > strings.LastIndex(writer, "capacity_quotas")) {
		t.Error("writeCapacityQuotasTx must write the api_token_settings mirror after its last capacity_quotas statement")
	}
	if !strings.Contains(writer, "sort.Slice(") {
		t.Error("writeCapacityQuotasTx must order the rows it writes (name order) so every transaction takes them alike")
	}
	// The banned OPERATION is changing a quota row's value. A "writer" is
	// a statement the house scanner sees writing the table with a SET of
	// allowed or mode: the migrate's seed (ON CONFLICT DO NOTHING) sets
	// nothing and is not one. migrate.go holds the one reviewed exception:
	// the v0.29.89 one-time adoption of a 0.29.82 default_rate_limit_per_hour
	// into the quota row (a ledgered step that runs once, before any page
	// can write).
	writers, migrateWriters := 0, 0
	// The floor only catches a broken walk: the package has 126 non-test
	// files today (2026-10-17), so 100 is well below it.
	stmts := sqlscan.Statements(srctest.PackageFiles(t, "internal/db", 100))
	for _, st := range sqlscan.FindWrites(stmts, "aveloxis_ops.capacity_quotas") {
		if len(st.SetExprs("allowed"))+len(st.SetExprs("mode")) == 0 {
			continue
		}
		if strings.HasSuffix(st.File, "/migrate.go") {
			migrateWriters++
		} else {
			writers++
		}
	}
	if writers != 1 {
		t.Errorf("found %d sites that change a capacity_quotas row in internal/db outside migrate.go; want exactly one (writeCapacityQuotasTx)", writers)
	}
	if migrateWriters != 1 {
		t.Errorf("migrate.go changes capacity_quotas at %d sites; the reviewed exception is exactly one (the v0.29.89 adoption)", migrateWriters)
	}
}
