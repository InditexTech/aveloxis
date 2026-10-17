// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package capacity

import (
	"testing"
	"time"
)

// v0.29.91 (whole-PR review, L18): the UTC-day boundary, its date and the
// windows' lengths have one owner here; the store and the api read them
// instead of re-spelling Truncate(24*time.Hour) and AddDate(0, 0, 1).
func TestWindowStartDateAndSeconds(t *testing.T) {
	// 23:59:59.999 UTC is still that day; a non-UTC zone is converted.
	late := time.Date(2026, 10, 10, 23, 59, 59, 999e6, time.UTC)
	if got := UTCDay.Start(late); !got.Equal(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)) || got.Location() != time.UTC {
		t.Errorf("UTCDay.Start(23:59:59.999) = %v", got)
	}
	east := time.FixedZone("UTC+5", 5*3600)
	morning := time.Date(2026, 10, 11, 2, 0, 0, 0, east) // 2026-10-10 21:00 UTC
	if got := UTCDate(morning); got != "2026-10-10" {
		t.Errorf("UTCDate(02:00 at UTC+5) = %q; want the UTC date 2026-10-10", got)
	}
	if got := UTCDay.End(late); !got.Equal(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("UTCDay.End = %v", got)
	}
	if got := Hour.Start(late); !got.Equal(late) {
		t.Errorf("Hour.Start = %v; an hour window starts at the first request", got)
	}
	if UTCDay.Seconds() != 86400 || Hour.Seconds() != 3600 {
		t.Errorf("Seconds = %d, %d; want 86400, 3600", UTCDay.Seconds(), Hour.Seconds())
	}
}
