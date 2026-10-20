// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import "github.com/aveloxis/aveloxis/internal/platform"

// logURL is the one spelling of a caller's URL in a log attribute or a
// reported line: userinfo redacted FIRST, then truncated and stripped of
// control characters (batch 5b review round 1: the other order let a
// userinfo longer than the truncation window survive into the WARN
// unredacted). Every URL-keyed attribute in this package goes through it
// (scripts/url_log_redaction_test.go trusts the name; its runtime test is
// TestLogURLRedactsBeforeTruncating here).
func logURL(u string) string {
	return platform.TruncateForLog([]byte(platform.RedactURLUserinfo(u)), 200)
}
