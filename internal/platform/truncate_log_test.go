// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import (
	"testing"
	"unicode/utf8"
)

// TestTruncateForLogCutsOnARuneBoundary (0.29.106 whole-branch review P6):
// the cap is in bytes, but the cut never splits a rune — a log attribute
// holds valid UTF-8 whatever the body's encoding.
func TestTruncateForLogCutsOnARuneBoundary(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"abc", 3, "abc"},
		{"abcd", 3, "abc...(truncated)"},
		{"ééé", 3, "é...(truncated)"}, // 2-byte runes: byte 3 is mid-rune
		{"ééé", 4, "éé...(truncated)"},
		{"日本語", 5, "日...(truncated)"}, // 3-byte runes
		{"a\nb\x00c", 10, "a bc"},
		{"", 0, ""},
		{"é", 1, "...(truncated)"},
	} {
		got := TruncateForLog([]byte(tc.in), tc.max)
		if got != tc.want || !utf8.ValidString(got) {
			t.Errorf("TruncateForLog(%q, %d) = %q (valid UTF-8: %v), want %q", tc.in, tc.max, got, utf8.ValidString(got), tc.want)
		}
	}
}
