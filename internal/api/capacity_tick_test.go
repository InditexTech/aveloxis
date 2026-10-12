// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/db"
)

// blockingCapacityStore stands in for the store under a hung statement:
// every call waits for its context, as pgx does, and returns its error.
type blockingCapacityStore struct{ calls chan string }

func (b blockingCapacityStore) wait(ctx context.Context, name string) error {
	b.calls <- name
	<-ctx.Done()
	return ctx.Err()
}

func (b blockingCapacityStore) PruneSignupPrivacy(ctx context.Context, _ time.Time) (db.SignupPrivacyPruned, error) {
	return db.SignupPrivacyPruned{}, b.wait(ctx, "signup")
}

func (b blockingCapacityStore) PruneRequestCounts(ctx context.Context, _ time.Time) (int64, error) {
	return 0, b.wait(ctx, "counts")
}

func (b blockingCapacityStore) AddDeltas(ctx context.Context, _ time.Time, _ map[string]int64) (map[string]int64, error) {
	return nil, b.wait(ctx, "flush")
}

// v0.29.91 (whole-PR review): RunCapacity's save and prunes ran on the
// process context with no deadline, so one hung statement stopped every
// later save and prune until the process stopped. Each step of a tick is
// bounded and a step that runs out is logged; a stop (the parent context
// cancelled) is still silent.
func TestCapacityTickStepsAreBoundedAndLogged(t *testing.T) {
	prev := capacityTickBound
	capacityTickBound = 30 * time.Millisecond
	t.Cleanup(func() { capacityTickBound = prev })

	logs := &lockedBuffer{}
	s, err := NewWithOptions(nil, slog.New(slog.NewTextHandler(logs, nil)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	fake := blockingCapacityStore{calls: make(chan string, 16)}
	s.limiter.meter = capacity.NewMeter(capacity.MeterOptions{Persister: fake})
	s.capacityMaint = fake
	// One counted request on a UTC day window: the flush has something to save.
	s.limiter.meter.Charge(capacity.Subject{Kind: capacity.KindAccount, ID: "1"},
		capacity.RateQuota{Name: capacity.QuotaRequestsPerDay, Window: capacity.UTCDay, Allowed: 10, Mode: capacity.Enforce})

	done := make(chan struct{})
	var pruned string
	go func() { s.capacityTick(context.Background(), &pruned); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a tick whose store calls hang never returned: its steps are not bounded")
	}
	close(fake.calls)
	var got []string
	for c := range fake.calls {
		got = append(got, c)
	}
	if strings.Join(got, ",") != "flush,signup,counts" {
		t.Errorf("steps run = %v; want flush, signup, counts (each bounded, none skipped by the one before)", got)
	}
	out := logs.String()
	for _, msg := range []string{"could not save daily request counts", "could not delete past sign-up secrets and keys", "could not delete old daily request counts"} {
		line := ""
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, msg) {
				line = l
			}
		}
		if !strings.Contains(line, "timed_out=true") {
			t.Errorf("%q not logged as a timeout:\n%s", msg, out)
		}
	}
	if pruned != "" {
		t.Error("a request-count prune that timed out must be retried next tick")
	}

	// A stop is not a failure: nothing logged.
	logs2 := &lockedBuffer{}
	s.logger = slog.New(slog.NewTextHandler(logs2, nil))
	fake2 := blockingCapacityStore{calls: make(chan string, 16)}
	s.capacityMaint = fake2
	s.limiter.meter = capacity.NewMeter(capacity.MeterOptions{Persister: fake2})
	s.limiter.meter.Charge(capacity.Subject{Kind: capacity.KindAccount, ID: "1"},
		capacity.RateQuota{Name: capacity.QuotaRequestsPerDay, Window: capacity.UTCDay, Allowed: 10, Mode: capacity.Enforce})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.capacityTick(ctx, &pruned)
	if logs2.String() != "" {
		t.Errorf("a stopped tick logged:\n%s", logs2.String())
	}
}
