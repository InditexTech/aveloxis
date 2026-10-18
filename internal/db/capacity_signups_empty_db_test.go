// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"testing"
	"time"
)

// Stage 1b review round 1 (D6): a window with no sign-up is [] (it was nil,
// `"signups_per_day": null` on /admin/capacity against the spec's array).
// The decisions' clock is moved far ahead so the window holds nothing.
func TestSignupsPerDayEmptyWindowIsAnEmptyList(t *testing.T) {
	store, ctx := openSR5Store(t)
	store.capacityClock = func() time.Time { return time.Date(2999, 1, 1, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { store.capacityClock = nil })
	days, err := store.SignupsPerDay(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if days == nil || len(days) != 0 {
		t.Errorf("SignupsPerDay over an empty window = %#v; want an empty, non-nil list", days)
	}
}
