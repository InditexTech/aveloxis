// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import (
	"strings"
	"unicode/utf8"
)

// TruncateForLog returns body as a log attribute capped at max bytes, cut
// on a rune boundary so the result is valid UTF-8 whatever the body was,
// suitable for a log attribute without flooding it when an upstream
// returns an HTML error page or other large body. The inputs are untrusted
// (third-party response bodies, request paths, user-typed values): newlines
// and other control characters are stripped so they cannot forge log lines
// (CodeQL: go/log-injection), then the length is bounded. One spelling for
// the web GUI and the admin service (stage 1 of summary/59; it was
// web.truncateForLog).
func TruncateForLog(body []byte, max int) string {
	s := strings.ReplaceAll(string(body), "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut-- // never split a rune (0.29.106 whole-branch review P6)
	}
	return s[:cut] + "...(truncated)"
}
