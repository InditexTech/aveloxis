// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/httpserver"
)

// v0.29.91 review round 2 (R2-1): nginx → web → /api → api. The web's
// proxy appended its own peer (nginx, 127.0.0.1) to X-Forwarded-For, so an
// api with api.trusted_proxy 127.0.0.1 took the rightmost entry — nginx's
// exempt address — and require_auth and the per-IP limits still applied to
// no one. The web now forwards exactly the client it resolved under its own
// web.trusted_proxy: one api setting is right for both paths, and a client's
// own X-Forwarded-For claims never pass through unresolved.
func TestWebAPIProxyForwardsTheClientItResolved(t *testing.T) {
	var mu sync.Mutex
	var seenXFF []string
	var seenAddr string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		seenXFF = r.Header.Values("X-Forwarded-For")
		// The api's own rule with api.trusted_proxy 127.0.0.1 (the web on
		// the same host is its peer).
		seenAddr = httpserver.ClientIP(r, "127.0.0.1").String()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(api.Close)

	through := func(webTrusted string, logs *strings.Builder) (string, []string) {
		s := New(nil, config.WebConfig{TrustedProxy: webTrusted, APIInternalURL: api.URL}, "", slog.New(slog.NewTextHandler(logs, nil)))
		if s.apiProxy == nil {
			t.Fatal("the /api proxy was not built")
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		r.RemoteAddr = "127.0.0.1:5555" // nginx on the same host
		r.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.1")
		w := httptest.NewRecorder()
		s.apiProxy.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("proxied = %d %s", w.Code, w.Body.String())
		}
		mu.Lock()
		defer mu.Unlock()
		return seenAddr, seenXFF
	}

	logs := &strings.Builder{}
	addr, xff := through("127.0.0.1", logs)
	if addr != "198.51.100.1" || strings.Join(xff, ",") != "198.51.100.1" {
		t.Errorf("both keys 127.0.0.1: the api resolved %q from X-Forwarded-For %q; want the browser 198.51.100.1, alone", addr, xff)
	}
	if strings.Contains(logs.String(), "trusted_proxy is empty") {
		t.Errorf("configured web warned:\n%s", logs.String())
	}

	logs.Reset()
	addr, xff = through("", logs)
	if addr != "127.0.0.1" || strings.Join(xff, ",") != "127.0.0.1" {
		t.Errorf("web key empty: the api resolved %q from X-Forwarded-For %q; want the web's own resolution 127.0.0.1 (no client claim forwarded)", addr, xff)
	}
	if !strings.Contains(logs.String(), "web.trusted_proxy is empty") {
		t.Errorf("web key empty behind a proxy: the web must WARN naming web.trusted_proxy:\n%s", logs.String())
	}
}
