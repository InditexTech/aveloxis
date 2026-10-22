// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"strings"
	"testing"
)

// One length rule for a login a caller looks up (round-2 review of
// 0.29.109): characters, not bytes, so a multi-byte login of 255
// characters is a login and a 256-character ASCII one is not.
func TestValidLoginLengthCountsCharacters(t *testing.T) {
	for _, c := range []struct {
		login string
		ok    bool
	}{
		{"", false},
		{"a", true},
		{strings.Repeat("a", MaxLoginLength), true},
		{strings.Repeat("a", MaxLoginLength+1), false},
		{strings.Repeat("é", MaxLoginLength), true}, // 510 bytes, 255 characters
		{strings.Repeat("é", MaxLoginLength+1), false},
	} {
		if got := ValidLoginLength(c.login); got != c.ok {
			t.Errorf("ValidLoginLength(%d bytes) = %v; want %v", len(c.login), got, c.ok)
		}
	}
	// NormalizeLogin trims before the rule (round-3 review): a padded login
	// is that login in both adapters; whitespace alone names nothing.
	if login, ok := NormalizeLogin("  octocat \t"); !ok || login != "octocat" {
		t.Errorf("NormalizeLogin(padded) = %q, %v", login, ok)
	}
	if login, ok := NormalizeLogin("   "); ok || login != "" {
		t.Errorf("NormalizeLogin(blank) = %q, %v; want refused", login, ok)
	}
	if _, ok := NormalizeLogin(" " + strings.Repeat("a", MaxLoginLength) + " "); !ok {
		t.Error("a padded login of the maximum length is a login")
	}
}
