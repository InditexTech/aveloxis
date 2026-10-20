// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/static"
)

// pageScripts is the web GUI's own JavaScript (the vendored library
// excluded): the corpus every script pin reads since the scripts left the
// templates (0.29.102, stage 2e of summary/59, WEB-9).
func pageScripts(t testing.TB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, a := range static.Assets() {
		if strings.HasSuffix(a.Path, ".js") && !strings.HasPrefix(a.Path, "vendor/") {
			out[a.Path] = string(a.Body)
		}
	}
	if len(out) < 5 {
		t.Fatalf("only %d page scripts embedded; the asset set broke", len(out))
	}
	return out
}

var (
	scriptTag   = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	onAttribute = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
)

// TestTemplatesCarryNoInlineScript (WEB-9/D-11): every <script> in the
// page templates loads a /static/ file and has an empty body; no element
// carries an on*= handler, a javascript: URL, a dialog or a CDN. Without
// this the Content-Security-Policy of stage 2f would have to keep
// 'unsafe-inline' and would do nothing.
func TestTemplatesCarryNoInlineScript(t *testing.T) {
	for _, m := range scriptTag.FindAllStringSubmatch(allTemplates, -1) {
		attrs, body := m[1], strings.TrimSpace(m[2])
		if body != "" {
			t.Errorf("a template carries an inline script body: %.80q", body)
		}
		if !strings.Contains(attrs, `src="/static/`) {
			t.Errorf("a template script does not load a /static/ asset: <script%s>", attrs)
		}
	}
	for _, needle := range []string{"javascript:", "confirm(", "alert(", "prompt(", "https://cdn.", "jsdelivr", "unpkg.com", "cdnjs"} {
		if strings.Contains(allTemplates, needle) {
			t.Errorf("the templates contain %q", needle)
		}
	}
	if m := onAttribute.FindString(allTemplates); m != "" {
		i := strings.Index(allTemplates, m)
		t.Errorf("a template carries an inline handler attribute: %.120q", allTemplates[i:min(i+120, len(allTemplates))])
	}
}

// TestPageScriptsHaveNoDialogs (D-19): the pages' scripts show messages on
// the page, never confirm()/alert()/prompt() (the operator's no-dialog
// rule; aveloxis-gui's confirm_action_test.js is the counterpart).
func TestPageScriptsHaveNoDialogs(t *testing.T) {
	for path, src := range pageScripts(t) {
		for _, needle := range []string{"alert(", "confirm(", "prompt(", "document.write(", "eval("} {
			if strings.Contains(src, needle) {
				t.Errorf("%s calls %s", path, needle)
			}
		}
	}
}

// TestNoScrollWrappers (D-20, the operator's no-horizontal-scrolling rule):
// no template or stylesheet rule scrolls sideways; tables reflow
// (.tbl-stack).
func TestNoScrollWrappers(t *testing.T) {
	var css string
	for _, a := range static.Assets() {
		if a.Path == "app.css" {
			css = string(a.Body)
		}
	}
	if css == "" {
		t.Fatal("app.css is not embedded")
	}
	for _, needle := range []string{"overflow-x:auto", "overflow-x: auto", "overflow-x:scroll", "overflow-x: scroll", "overflow:auto", "overflow: auto", "overflow:scroll", "overflow: scroll"} {
		if strings.Contains(allTemplates, needle) {
			t.Errorf("a template scrolls sideways: %q", needle)
		}
		if strings.Contains(css, needle) {
			t.Errorf("app.css scrolls sideways: %q", needle)
		}
	}
	if !strings.Contains(css, "table.tbl-stack tbody td[data-label]::before") {
		t.Error("app.css lacks the stacked-table rule the data-label cells need")
	}
	for _, page := range []string{`<table class="repo-table tbl-stack"`, `<table class="tbl-stack"`} {
		if !strings.Contains(allTemplates, page) {
			t.Errorf("a listing table is not stacked: %s", page)
		}
	}
	// A stacked table hides its header by <thead>; a header row dropped
	// straight into the table lands in the implied <tbody> and renders as a
	// 200 px block of labels on a phone (review B1).
	stacked := regexp.MustCompile(`(?s)<table\b[^>]*\bclass="[^"]*\btbl-stack\b[^"]*"[^>]*>(.*?)</table>`).FindAllStringSubmatch(allTemplates, -1)
	if want := strings.Count(allTemplates, "tbl-stack"); len(stacked) != want || want < 2 {
		t.Fatalf("the stacked-table scan saw %d tables for %d tbl-stack mentions; the scan or a table's spelling broke", len(stacked), want)
	}
	for _, m := range stacked {
		body := m[1]
		if !strings.HasPrefix(strings.TrimSpace(body), "<thead>") || !strings.Contains(body, "<tbody>") {
			t.Errorf("a stacked table does not open with <thead> and carry a <tbody>: %.80q", strings.TrimSpace(body))
		}
	}
}

// TestStaticAssetsReferencedByTemplatesAreServed: every /static/ path a
// template references answers 200 with its media type through the real
// Handler (public, no session), and an unknown path is a 404.
func TestStaticAssetsReferencedByTemplatesAreServed(t *testing.T) {
	s := New(nil, config.WebConfig{Addr: ":0", APIInternalURL: "http://127.0.0.1:1", DevMode: true},
		"", slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := s.Handler()
	refs := regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`).FindAllStringSubmatch(allTemplates, -1)
	if len(refs) < 6 {
		t.Fatalf("only %d /static/ references found in the templates", len(refs))
	}
	seen := map[string]bool{}
	for _, m := range refs {
		p := m[1]
		if seen[p] {
			continue
		}
		seen[p] = true
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		ct := rec.Header().Get("Content-Type")
		wantCT := "text/javascript"
		if strings.HasSuffix(p, ".css") {
			wantCT = "text/css"
		}
		if rec.Code != http.StatusOK || !strings.HasPrefix(ct, wantCT) || rec.Body.Len() == 0 || rec.Header().Get("ETag") == "" {
			t.Errorf("GET %s = %d %q (%d bytes, ETag %q); want 200 %s with a body and an ETag", p, rec.Code, ct, rec.Body.Len(), rec.Header().Get("ETag"), wantCT)
		}
		etag := rec.Header().Get("ETag")
		r := httptest.NewRequest(http.MethodGet, p, nil)
		r.Header.Set("If-None-Match", etag)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotModified {
			t.Errorf("GET %s with its ETag = %d; want 304", p, rec.Code)
		}
		// A compressing proxy weakens the tag (W/"…"); the browser sends
		// that form back and must still get its 304 (review B8).
		r = httptest.NewRequest(http.MethodGet, p, nil)
		r.Header.Set("If-None-Match", "W/"+etag)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotModified {
			t.Errorf("GET %s with its weak ETag = %d; want 304", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/nope.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /static/nope.js = %d; want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/../icon.png", nil))
	if rec.Code == http.StatusOK && strings.Contains(rec.Header().Get("Content-Type"), "image") {
		t.Error("a path above /static/ was served")
	}
}

// pageSources is everything the web GUI ships to a browser — the page
// templates, the page scripts and the stylesheet — for pins that ask
// "does the GUI do X" without caring which file carries it.
func pageSources(t testing.TB) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(allTemplates)
	for _, a := range static.Assets() {
		if !strings.HasPrefix(a.Path, "vendor/") {
			b.Write(a.Body)
		}
	}
	return b.String()
}
