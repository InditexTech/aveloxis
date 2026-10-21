// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/srctest"
)

// 0.29.113 (OWASP ASVS 5.0, summary/62): the cookies carry the __Host-
// prefix whenever they are Secure (V3.3.1, V3.3.3) — the bare name only
// under web.dev_mode, where plain HTTP cannot carry one — and every read
// and deletion uses the same spelling.
func TestCookiesCarryTheHostPrefixUnlessDevMode(t *testing.T) {
	for _, dev := range []bool{false, true} {
		s := New(nil, config.WebConfig{DevMode: dev}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
		want := "__Host-aveloxis_session"
		if dev {
			want = "aveloxis_session"
		}
		if c := s.sessionCookie("x"); c.Name != want || c.Secure == dev || c.Path != "/" || c.Domain != "" {
			t.Errorf("dev=%v: session cookie %+v; want name %q, Secure=%v, Path=/, no Domain (the prefix's own rules)", dev, c, want, !dev)
		}
		if c := s.expireCookie("aveloxis_session"); c.Name != want || c.MaxAge != -1 {
			t.Errorf("dev=%v: the deletion names %q; want %q", dev, c.Name, want)
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: want, Value: "v"})
		if c, err := s.readCookie(r, "aveloxis_session"); err != nil || c.Value != "v" {
			t.Errorf("dev=%v: the read misses the cookie it set (%v)", dev, err)
		}
	}
}

// Every production answer carries Strict-Transport-Security (V3.4.1); a
// dev_mode server, on plain HTTP, sends none.
func TestProductionAnswersCarryHSTS(t *testing.T) {
	for _, dev := range []bool{false, true} {
		s := New(nil, config.WebConfig{DevMode: dev}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/login", nil))
		got := w.Header().Get("Strict-Transport-Security")
		if dev && got != "" {
			t.Errorf("dev_mode answers carry HSTS %q; a browser would ignore it and a reader would believe it", got)
		}
		if !dev && got != hstsValue {
			t.Errorf("production answer's HSTS = %q; want %q", got, hstsValue)
		}
	}
}

// A sensitive change asks for a recent sign-in (V7.5.1): within the
// inactivity bound; a session without a recorded sign-in is never recent.
func TestRecentSignInIsWithinTheIdleBound(t *testing.T) {
	s := New(nil, config.WebConfig{SessionIdleMinutes: 10}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, c := range []struct {
		age  time.Duration
		zero bool
		want bool
	}{{age: time.Minute, want: true}, {age: 9 * time.Minute, want: true}, {age: 11 * time.Minute, want: false}, {zero: true, want: false}} {
		sess := &Session{}
		if !c.zero {
			sess.CreatedAt = time.Now().Add(-c.age)
		}
		if got := s.recentSignIn(sess); got != c.want {
			t.Errorf("age %v zero=%v: recent = %v; want %v", c.age, c.zero, got, c.want)
		}
	}
}

// Both processes set the store's inactivity bound from the one config
// accessor (SR-10: the knob reaches the behaviour through one layer).
func TestSessionIdleIsSetFromTheOneAccessorInWebAndAPI(t *testing.T) {
	src := srctest.StripGoComments(srctest.PackageSource(t, "cmd/aveloxis", 10))
	const call = "store.SetSessionIdle(cfg.Web.SessionIdle())"
	if n := countOf(src, call); n != 2 {
		t.Errorf("%d sites set the session idle bound from the accessor; want 2 (the web and api commands)", n)
	}
}

func countOf(s, sub string) int {
	n := 0
	for i := 0; ; {
		j := indexFrom(s, sub, i)
		if j < 0 {
			return n
		}
		n++
		i = j + len(sub)
	}
}

func indexFrom(s, sub string, from int) int {
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
