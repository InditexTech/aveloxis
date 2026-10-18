// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import "strings"

// TruncateForLog returns a string-safe slice of body capped at max bytes,
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
	return s[:max] + "...(truncated)"
}
