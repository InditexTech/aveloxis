// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"strings"
	"testing"
)

// TestLogURLRedactsBeforeTruncating is the runtime contract the redaction
// registry requires (scripts/url_log_redaction_test.go): a userinfo longer
// than the truncation window is redacted, not cut into the log, and a
// control character never reaches the line.
func TestLogURLRedactsBeforeTruncating(t *testing.T) {
	secret := strings.Repeat("s", 300)
	got := logURL("https://user:" + secret + "@github.com/o/r")
	if strings.Contains(got, "sss") || !strings.HasPrefix(got, "https://***@github.com/o/r") {
		t.Errorf("logURL = %q; want the redacted spelling", got)
	}
	long := "https://github.com/o/" + strings.Repeat("r", 400)
	if got := logURL(long); len(got) > 200+len("...(truncated)") || !strings.HasSuffix(got, "...(truncated)") {
		t.Errorf("logURL(long) = %q; want it truncated at 200 bytes", got)
	}
	if got := logURL("https://github.com/o/r\x00\n"); strings.ContainsAny(got, "\x00\n") {
		t.Errorf("logURL = %q; control characters must be stripped", got)
	}
	if got := logURL("tok-secret@github.com/o/r"); strings.Contains(got, "tok-secret") || !strings.Contains(got, "***@github.com/") {
		t.Errorf("logURL(schemeless) = %q; want the redacted spelling", got)
	}
}
