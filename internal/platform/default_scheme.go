// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import "strings"

// WithDefaultScheme returns raw with "https://" in front when it names no
// scheme at all: "github.com/chaoss/augur" is what people paste, and every
// catalog writer has accepted it. A URL that already carries a scheme — any
// scheme, "http://", "ssh://", "git://" — is returned as it was, so the
// parser behind it can still refuse what it does not take. The one spelling
// (SR-17; stage 1 of summary/59): the web validator, the org parser and the
// store's org canonicalizer each had their own.
func WithDefaultScheme(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") {
		return raw
	}
	return "https://" + raw
}
