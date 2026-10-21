// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// The widest legal row never scrolls the page sideways (operator rule): a
// 255-character login, a 200-character name or label, as one unbroken
// token, wraps wherever it is printed. Measured in headless Chrome at
// 375 px for 0.29.109 (round-2 review B-F2): with this rule the page is
// the viewport's width; without `p` a 200-character name in a confirm
// question or a token label widened it to 1,660 px. This pin keeps the
// sites in the rule; the measurement is the proof.
func TestUnbrokenTokensWrapWhereverPrinted(t *testing.T) {
	css := srctest.Read(t, "internal/static/app.css")
	rule := "overflow-wrap:anywhere;min-width:0"
	i := strings.Index(css, rule)
	if i < 0 {
		t.Fatal("app.css has no overflow-wrap:anywhere rule")
	}
	selectors := css[strings.LastIndex(css[:i], "}")+1 : i]
	selectors = selectors[:strings.LastIndex(selectors, "{")]
	for _, site := range []string{".nav-user span", ".breadcrumb .current", ".group-list li", "h2", "h3", "p"} {
		found := false
		for _, sel := range strings.Split(selectors, ",") {
			if strings.TrimSpace(sel) == site {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is not in the overflow-wrap rule (%q); a 200-character token there scrolls the page sideways", site, selectors)
		}
	}
	// Not in the rule on purpose: a select in a .form-row (round-3 review
	// B-4 reasoned it would widen on a 200-character option). Measured at
	// 375 px with such an option: the select stays 321 px wide inside the
	// viewport, because its explicit width:100% bounds the flex automatic
	// minimum. The ledger (v0.29.109, round 3) keeps the numbers.
}
