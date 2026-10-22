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
	// .nav-login (Copilot on PR #241 and its L10 round, 0.29.114): the nav's
	// login became an anchor in 0.29.113 and fell outside ".nav-user span",
	// which then matched nothing while this list kept requiring it. The
	// login's element carries the class; TestNavLoginIsInsideTheWrapRule
	// pins the template side, so a change of element cannot escape again.
	for _, site := range []string{".nav-login", ".breadcrumb .current", ".group-list li", "h2", "h3", "p"} {
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

// TestNavLoginIsInsideTheWrapRule pins the template side of the nav's wrap
// rule (L10 round of 0.29.114): the rendered nav prints the login inside an
// element of class nav-login, the class the overflow-wrap rule names.
func TestNavLoginIsInsideTheWrapRule(t *testing.T) {
	tmpl := parseTemplates()
	const login = "wrap-probe-login-0123456789"
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "nav", map[string]any{"Session": &Session{LoginName: login}, "CSRF": "x"}); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	i := strings.Index(html, login)
	if i < 0 {
		t.Fatalf("the nav does not print the login:\n%s", html)
	}
	open := strings.LastIndex(html[:i], "<")
	if open < 0 || !strings.Contains(html[open:i], `class="nav-login`) && !strings.Contains(html[open:i], ` nav-login`) {
		t.Errorf("the login's element %q does not carry class nav-login; a 255-character login would widen the page", html[max(open, 0):i])
	}
}
