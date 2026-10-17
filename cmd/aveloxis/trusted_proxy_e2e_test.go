// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// SR-10 (v0.29.91, whole-PR review): web.trusted_proxy end to end — the
// aveloxis.json value, through config.Load and the web server builder
// (newWebServer), to the address the sign-up quota counts. Set: the
// rightmost X-Forwarded-For entry (the one the proxy appended; the client's
// own claim to its left is ignored). Empty: the peer.
func TestWebTrustedProxyDecidesTheSignupAddress(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil)
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.1")
		return r
	}
	set := loadConfigJSON(t, `{"web": {"trusted_proxy": "127.0.0.1"}}`)
	if got := newWebServer(nil, set, nil, logger).SignupAddress(req()).String(); got != "198.51.100.1" {
		t.Errorf("web.trusted_proxy 127.0.0.1: the sign-up quota counts %q; want 198.51.100.1", got)
	}
	empty := loadConfigJSON(t, `{"web": {"trusted_proxy": ""}}`)
	if got := newWebServer(nil, empty, nil, logger).SignupAddress(req()).String(); got != "127.0.0.1" {
		t.Errorf("web.trusted_proxy empty: the sign-up quota counts %q; want the peer 127.0.0.1", got)
	}
}
