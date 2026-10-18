// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/aveloxis/aveloxis/internal/capacity"
)

// The operator's 2026-10-17 migrate on 0.29.96 (v0.29.97): a schema born
// on 0.29.82–0.29.88 declares api_token_settings.default_rate_limit_per_hour
// NOT NULL with no default, and the seed step stopped supplying the column
// in 0.29.89 — so on such a fleet with no row 1 yet the seed failed with
// SQLSTATE 23502 and the migrate reported an error. The fixture puts the
// package database into that shape (default dropped, row 1 gone) and runs
// the seed: row 1 exists afterwards, its mirror is the shipped hourly
// allowance, and the column carries the default schema.sql declares.
func TestAPITokenSettingsSeedSurvivesTheOldSchemaWithoutRowOne(t *testing.T) {
	store, ctx := openSR5Store(t)
	t.Cleanup(func() {
		bg := context.Background()
		cleanupExecRetry(bg, store, `ALTER TABLE aveloxis_ops.api_token_settings ALTER COLUMN default_rate_limit_per_hour SET DEFAULT 1000`)
		var errs []error
		seedAPITokenSettings(bg, store, slog.New(slog.NewTextHandler(io.Discard, nil)), &errs)
	})
	mustExecRetry(ctx, t, store, `ALTER TABLE aveloxis_ops.api_token_settings ALTER COLUMN default_rate_limit_per_hour DROP DEFAULT`)
	mustExecRetry(ctx, t, store, `DELETE FROM aveloxis_ops.api_token_settings WHERE id = 1`)

	var errs []error
	seedAPITokenSettings(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), &errs)
	if len(errs) != 0 {
		t.Fatalf("the seed failed on the 0.29.82 schema without row 1: %v", errs)
	}
	var hour, days int
	if err := store.pool.QueryRow(ctx, `SELECT default_rate_limit_per_hour, default_lifetime_days FROM aveloxis_ops.api_token_settings WHERE id = 1`).Scan(&hour, &days); err != nil {
		t.Fatalf("row 1 after the seed: %v", err)
	}
	if hour != capacity.Shipped[capacity.QuotaTokenRequestsPerHour].Allowed || days != DefaultAPITokenLifetimeDays {
		t.Errorf("row 1 = (hour %d, days %d); want the shipped hourly allowance %d and the lifetime %d", hour, days, capacity.Shipped[capacity.QuotaTokenRequestsPerHour].Allowed, DefaultAPITokenLifetimeDays)
	}
	var def *string
	if err := store.pool.QueryRow(ctx, `SELECT column_default FROM information_schema.columns WHERE table_schema = 'aveloxis_ops' AND table_name = 'api_token_settings' AND column_name = 'default_rate_limit_per_hour'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if def == nil || *def != "1000" {
		t.Errorf("default_rate_limit_per_hour default = %v; want 1000 (schema.sql's declaration, restored by the migrate)", def)
	}
	// Idempotent: a second run changes nothing.
	seedAPITokenSettings(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), &errs)
	if len(errs) != 0 {
		t.Fatalf("second seed: %v", errs)
	}
}
