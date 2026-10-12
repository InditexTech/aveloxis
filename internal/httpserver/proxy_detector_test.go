// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package httpserver

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// v0.29.91 (whole-PR review, L18): one detector for "a proxy is in front
// but <key> is empty", shared by the api and the web. It fires once per
// process for a loopback peer, or a peer the caller exempts, that sends
// X-Forwarded-For while the key is empty; it names the key; it never
// changes the request (SR-7, observation only).
func TestMissingTrustedProxyDetector(t *testing.T) {
	lan := func(ip net.IP) bool { _, n, _ := net.ParseCIDR("10.0.0.0/8"); return n.Contains(ip) }
	req := func(peer, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = peer
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct {
		name, peer, xff, trusted string
		exempt                   func(net.IP) bool
		want                     bool
	}{
		{"direct client", "203.0.113.5:1", "198.51.100.1", "", lan, false},
		{"loopback without the header", "127.0.0.1:1", "", "", lan, false},
		{"configured", "127.0.0.1:1", "198.51.100.1", "127.0.0.1", lan, false},
		{"loopback with the header", "127.0.0.1:1", "198.51.100.1", "", nil, true},
		{"IPv6 loopback", "[::1]:1", "198.51.100.1", "", nil, true},
		{"exempt peer with the header", "10.1.2.3:1", "198.51.100.1", "", lan, true},
		{"exempt rule absent", "10.1.2.3:1", "198.51.100.1", "", nil, false},
	}
	for _, c := range cases {
		logs := &strings.Builder{}
		d := &MissingTrustedProxyDetector{}
		r := req(c.peer, c.xff)
		before := r.Header.Get("X-Forwarded-For")
		d.Observe(slog.New(slog.NewTextHandler(logs, nil)), r, "api.trusted_proxy", c.trusted, c.exempt, "the effect")
		got := strings.Contains(logs.String(), "api.trusted_proxy is empty")
		if got != c.want {
			t.Errorf("%s: warned = %v, want %v:\n%s", c.name, got, c.want, logs.String())
		}
		if c.want && (!strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "effect=\"the effect\"")) {
			t.Errorf("%s: want a WARN carrying the effect:\n%s", c.name, logs.String())
		}
		if r.Header.Get("X-Forwarded-For") != before {
			t.Errorf("%s: the detector changed the request", c.name)
		}
	}
	// Once per process (per detector).
	logs := &strings.Builder{}
	d := &MissingTrustedProxyDetector{}
	l := slog.New(slog.NewTextHandler(logs, nil))
	for i := 0; i < 3; i++ {
		d.Observe(l, req("127.0.0.1:1", "198.51.100.1"), "web.trusted_proxy", "", nil, "")
	}
	if n := strings.Count(logs.String(), "web.trusted_proxy is empty"); n != 1 {
		t.Errorf("warned %d times, want once:\n%s", n, logs.String())
	}
}
