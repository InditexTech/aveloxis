// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The console's numbers are parsed at the store's width (int4): a value past
// int32 is refused as out of range at the form, never narrowed on its way
// to the service (CodeQL go/incorrect-integer-conversion, seven sites on the
// 0.29.111 tree, all this reader's).
func TestConsoleNumbersAreParsedAtInt4Width(t *testing.T) {
	for _, c := range []struct {
		value string
		want  *int64
		body  string
	}{
		{"", nil, ""},
		{"12", ptr(12), ""},
		{"-5", ptr(-5), ""},
		{"2147483647", ptr(2147483647), ""},
		{"2147483648", nil, "n is out of range"},
		{"-2147483649", nil, "n is out of range"},
		{"92233720368547760", nil, "n is out of range"},
		{"x", nil, "n must be a number"},
		{"1.5", nil, "n must be a number"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(url.Values{"n": {c.value}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		got, ok := optionalInt64(w, r, "n")
		switch {
		case c.body != "":
			if ok || w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != c.body {
				t.Errorf("%q: ok=%v %d %q; want 400 %q", c.value, ok, w.Code, strings.TrimSpace(w.Body.String()), c.body)
			}
		case c.want == nil:
			if !ok || got != nil {
				t.Errorf("%q: got %v ok=%v; want nil (absent)", c.value, got, ok)
			}
		default:
			if !ok || got == nil || *got != *c.want {
				t.Errorf("%q: got %v ok=%v; want %d", c.value, got, ok, *c.want)
			}
		}
	}
}

func ptr(n int64) *int64 { return &n }
