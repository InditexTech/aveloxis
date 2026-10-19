// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestContactEmailRuleIsOne (whole-branch review of PR #238, DB-F1): the
// seed from mail.operator_email and the administrator's save decide "a
// plausible address that cannot break a header or a mailto link" through
// ONE predicate, validContactEmail; a second inlined copy drifts.
func TestContactEmailRuleIsOne(t *testing.T) {
	for _, fn := range []string{"func (s *PostgresStore) SeedCapacityContact(", "func (s *PostgresStore) SetCapacityContact("} {
		body := srctest.StripGoComments(srctest.PackageFunc(t, "internal/db", fn))
		if !strings.Contains(body, "validContactEmail(") {
			t.Errorf("%s does not decide through validContactEmail", fn)
		}
		if strings.Contains(body, `ContainsAny(`) || strings.Contains(body, `MaxContactEmailBytes`) {
			t.Errorf("%s spells the address rule itself", fn)
		}
	}
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"ops@example.org", true},
		{"", false},
		{"no-at-sign", false},
		{"a@b?x=1", false},
		{"a@b&c", false},
		{"a@b%20", false},
		{"a@b c", false},
		{"<a@b>", false},
		{"a@b\r\n", false},
		{strings.Repeat("a", MaxContactEmailBytes) + "@b", false},
	} {
		if got := validContactEmail(tc.in); got != tc.ok {
			t.Errorf("validContactEmail(%q) = %v; want %v", tc.in, got, tc.ok)
		}
	}
}
