// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestCapacityContactSeededAfterEveryMigrate (stage 1f of summary/59; SR-10,
// the behavior half is TestSeedCapacityContactFillsOnlyANeverSetRow in
// internal/db): the migrate seeds the refusal contact EMPTY, so both paths
// that migrate — serve and `aveloxis migrate` — must seed it from
// mail.operator_email right after their Migrate call, and the seeder must
// read that config field. Dropping either call, or moving it ahead of the
// migrate (no settings row yet), fails here.
func TestCapacityContactSeededAfterEveryMigrate(t *testing.T) {
	const seedCall = "seedCapacityContact(ctx, store, cfg, logger)"
	for _, sig := range []string{"func runServe(", "func migrateCmd("} {
		body := srctest.StripGoComments(srctest.PackageFunc(t, "cmd/aveloxis", sig))
		migrate := strings.Index(body, "store.Migrate(ctx)")
		seed := strings.Index(body, seedCall)
		switch {
		case migrate < 0:
			t.Errorf("%s no longer calls store.Migrate(ctx)", sig)
		case seed < 0:
			t.Errorf("%s does not seed the capacity contact after its migrate", sig)
		case seed < migrate:
			t.Errorf("%s seeds the capacity contact BEFORE its migrate (the settings row is created by the migrate)", sig)
		}
	}
	seeder := srctest.StripGoComments(srctest.PackageFunc(t, "cmd/aveloxis", "func seedCapacityContact("))
	if !strings.Contains(seeder, "store.SeedCapacityContact(ctx, cfg.Mail.OperatorEmail)") {
		t.Errorf("seedCapacityContact does not seed from mail.operator_email:\n%s", seeder)
	}
}
