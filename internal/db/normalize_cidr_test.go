// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"errors"
	"testing"
)

// NormalizeCIDR is the one allowlist-network rule (round-2 review of
// 0.29.109): trimmed, parsed, masked; anything else is ErrInvalidCIDR.
func TestNormalizeCIDR(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"192.0.2.7/24", "192.0.2.0/24"},
		{"  203.0.113.0/24 \n", "203.0.113.0/24"},
		{"2001:db8::1/48", "2001:db8::/48"},
		{"10.0.0.1/32", "10.0.0.1/32"},
	} {
		if got, err := NormalizeCIDR(c.in); err != nil || got != c.want {
			t.Errorf("NormalizeCIDR(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", "nope", "192.0.2.1", "192.0.2.0/33", "::/129", "192.0.2.0/24 extra", "<script>"} {
		if got, err := NormalizeCIDR(in); !errors.Is(err, ErrInvalidCIDR) || got != "" {
			t.Errorf("NormalizeCIDR(%q) = %q, %v; want ErrInvalidCIDR", in, got, err)
		}
	}
}
