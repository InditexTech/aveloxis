// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
)

// Stage 4 of summary/59 (0.29.110, D-9/C1): the /api/ proxy attaches the
// session's own Bearer, minted once per session and held in this process,
// and forwards no cookie. Driven through the Handler with a fake api that
// records what it was sent.

type recordedAPI struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth []string // the Authorization header of each request
	cook []string // the Cookie header of each request
}

func newRecordedAPI(t *testing.T) *recordedAPI {
	t.Helper()
	a := &recordedAPI{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.auth = append(a.auth, r.Header.Get("Authorization"))
		a.cook = append(a.cook, r.Header.Get("Cookie"))
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *recordedAPI) calls() (auth, cook []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.auth...), append([]string(nil), a.cook...)
}

func proxied(s *Server, cookie string, extra http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/groups", nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: cookie})
	}
	for k, v := range extra {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestAPIProxyAttachesTheSessionsBearerAndNoCookie(t *testing.T) {
	api := newRecordedAPI(t)
	s := newTestServerWithAPIURL(t, api.srv.URL)
	// No session: the login redirect, and the api is never asked.
	if w := proxied(s, "", nil); w.Code != http.StatusFound {
		t.Fatalf("anonymous /api = %d; want 302 to /login", w.Code)
	}
	if auth, _ := api.calls(); len(auth) != 0 {
		t.Fatalf("an anonymous request reached the api: %v", auth)
	}
	sessionExpiry := time.Now().Add(time.Hour).Truncate(time.Second)
	cookie := plantSession(t, s, "tester", &Session{UserID: 1, LoginName: "tester", ExpiresAt: sessionExpiry})
	var mints atomic.Int32
	var expiries []time.Time
	s.mintProxyToken = func(_ context.Context, cookie string, expiresAt time.Time) (string, error) {
		mints.Add(1)
		expiries = append(expiries, expiresAt)
		return "minted-for-" + cookie, nil
	}
	// A Bearer the browser sent itself is replaced; the cookie never leaves.
	for i := 0; i < 3; i++ {
		if w := proxied(s, cookie, http.Header{"Authorization": {"Bearer browser-held"}}); w.Code != http.StatusOK {
			t.Fatalf("request %d = %d\n%s", i, w.Code, w.Body.String())
		}
	}
	auth, cook := api.calls()
	if len(auth) != 3 {
		t.Fatalf("the api saw %d requests; want 3", len(auth))
	}
	for i := range auth {
		if auth[i] != "Bearer minted-for-"+cookie {
			t.Errorf("request %d carried %q; want the session's minted Bearer, never the browser's", i, auth[i])
		}
		if cook[i] != "" {
			t.Errorf("request %d forwarded cookies: %q", i, cook[i])
		}
	}
	if n := mints.Load(); n != 1 {
		t.Errorf("minted %d times for one session; want once (held)", n)
	}
	// The child expires with its session (ASVS V7.4.1): the mint is asked
	// for the session's own expiry, never the 30-day default.
	if len(expiries) != 1 || !expiries[0].Equal(sessionExpiry) {
		t.Errorf("mint expiries %v; want the session's own %v", expiries, sessionExpiry)
	}
	// Sign-out forgets the held token: the next request mints again.
	s.proxyTokens.forget(db.HashToken(cookie))
	if w := proxied(s, cookie, nil); w.Code != http.StatusOK || mints.Load() != 2 {
		t.Errorf("after forget: %d, mints %d; want 200 and a second mint", w.Code, mints.Load())
	}
}

func TestAPIProxyRefusesWhenNoTokenCanBeMinted(t *testing.T) {
	api := newRecordedAPI(t)
	s := newTestServerWithAPIURL(t, api.srv.URL)
	cookie := plantSession(t, s, "tester", &Session{UserID: 1, LoginName: "tester", ExpiresAt: time.Now().Add(time.Hour)})
	// Signed out between the gate's read and the mint: a definitive 401.
	s.mintProxyToken = func(context.Context, string, time.Time) (string, error) { return "", db.ErrNoWebSession }
	if w := proxied(s, cookie, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("mint after sign-out = %d; want 401", w.Code)
	}
	// A store failure: a 500, logged; the api never sees a Bearer-less request.
	s.mintProxyToken = func(context.Context, string, time.Time) (string, error) { return "", errors.New("pool closed") }
	if w := proxied(s, cookie, nil); w.Code != http.StatusInternalServerError {
		t.Errorf("mint failure = %d; want 500", w.Code)
	}
	if auth, _ := api.calls(); len(auth) != 0 {
		t.Errorf("a request without a minted Bearer reached the api: %v", auth)
	}
	// A failed mint holds nothing: the next request mints again.
	s.mintProxyToken = func(context.Context, string, time.Time) (string, error) { return "later", nil }
	if w := proxied(s, cookie, nil); w.Code != http.StatusOK {
		t.Errorf("mint after a failure = %d; want 200", w.Code)
	}
	if auth, _ := api.calls(); len(auth) != 1 || auth[0] != "Bearer later" {
		t.Errorf("the api saw %v; want the later Bearer once", auth)
	}
}

// One mint per session even under concurrent first requests: the second
// waits for the first's answer instead of minting a twin (which would
// leave an orphan child token until sign-out).
func TestProxyTokensMintOncePerKeyUnderConcurrency(t *testing.T) {
	var c proxyTokens
	var mints atomic.Int32
	release := make(chan struct{})
	mint := func(context.Context) (string, error) {
		mints.Add(1)
		<-release
		return "one", nil
	}
	now := time.Now()
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = c.bearer(context.Background(), "k", now, now.Add(time.Hour), mint)
		}(i)
	}
	time.Sleep(20 * time.Millisecond) // every goroutine is parked on the one mint
	close(release)
	wg.Wait()
	for i, r := range results {
		if r != "one" {
			t.Errorf("waiter %d got %q", i, r)
		}
	}
	if mints.Load() != 1 || c.size() != 1 {
		t.Errorf("mints %d, held %d; want 1 and 1", mints.Load(), c.size())
	}
	// Expired entries are swept on the next mint; an expired key re-mints.
	past := now.Add(-time.Minute)
	if _, err := c.bearer(context.Background(), "old", now, past, func(context.Context) (string, error) { return "x", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := c.bearer(context.Background(), "k2", now, now.Add(time.Hour), func(context.Context) (string, error) { return "y", nil }); err != nil {
		t.Fatal(err)
	}
	if c.size() != 2 { // "k" and "k2"; "old" swept
		t.Errorf("held %d after the sweep; want 2", c.size())
	}
}

// The api's own sign-out would delete the held token while the web session
// lives on (round-1 review, M-1): the proxy refuses it, and the api never
// sees it; the built-in GUI signs out through POST /logout.
func TestAPIProxyRefusesTheAPIsOwnLogout(t *testing.T) {
	api := newRecordedAPI(t)
	s := newTestServerWithAPIURL(t, api.srv.URL)
	cookie := plantSession(t, s, "tester", &Session{UserID: 1, LoginName: "tester", ExpiresAt: time.Now().Add(time.Hour)})
	r := httptest.NewRequest(http.MethodPost, apiSessionLogoutPath, nil)
	r.AddCookie(&http.Cookie{Name: "aveloxis_session", Value: cookie})
	csrf(r)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("proxied POST %s = %d; want 403", apiSessionLogoutPath, w.Code)
	}
	if auth, _ := api.calls(); len(auth) != 0 {
		t.Errorf("the api saw the sign-out: %v", auth)
	}
}

// A Bearer the api refuses (revoked by another path, swept) is dropped at
// once: the next request mints a new one instead of presenting the dead
// token for the rest of the session (round-1 review, M-1).
func TestAPIProxyForgetsABearerTheAPIRefused(t *testing.T) {
	var refuseFirst atomic.Bool
	refuseFirst.Store(true)
	var seen []string
	var mu sync.Mutex
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if refuseFirst.Swap(false) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(api.Close)
	s := newTestServerWithAPIURL(t, api.URL)
	cookie := plantSession(t, s, "tester", &Session{UserID: 1, LoginName: "tester", ExpiresAt: time.Now().Add(time.Hour)})
	var mints atomic.Int32
	s.mintProxyToken = func(context.Context, string, time.Time) (string, error) {
		return "mint-" + strconv.Itoa(int(mints.Add(1))), nil
	}
	if w := proxied(s, cookie, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("the api's refusal = %d; want it passed through once", w.Code)
	}
	if w := proxied(s, cookie, nil); w.Code != http.StatusOK {
		t.Fatalf("the request after a refusal = %d; want 200 with a new Bearer", w.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if mints.Load() != 2 || len(seen) != 2 || seen[0] != "Bearer mint-1" || seen[1] != "Bearer mint-2" {
		t.Errorf("mints %d, the api saw %v; want the refused token dropped and a second one minted", mints.Load(), seen)
	}
}

// The leader's cancelled request must not fail a waiter's live one
// (round-1 review, L-2): the waiter mints for itself.
func TestProxyTokensWaiterMintsWhenTheLeaderWasCancelled(t *testing.T) {
	var c proxyTokens
	var mints atomic.Int32
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	entered := make(chan struct{})
	mint := func(ctx context.Context) (string, error) {
		n := mints.Add(1)
		if n == 1 {
			close(entered)
			<-ctx.Done() // the leader's request is aborted mid-mint
			return "", fmt.Errorf("create api session token: %w", ctx.Err())
		}
		return "second", nil
	}
	now := time.Now()
	leaderErr := make(chan error, 1)
	go func() {
		_, err := c.bearer(leaderCtx, "k", now, now.Add(time.Hour), mint)
		leaderErr <- err
	}()
	<-entered
	waiter := make(chan string, 1)
	go func() {
		b, err := c.bearer(context.Background(), "k", now, now.Add(time.Hour), mint)
		if err != nil {
			waiter <- "error: " + err.Error()
			return
		}
		waiter <- b
	}()
	time.Sleep(10 * time.Millisecond) // the waiter is parked on the leader's mint
	cancelLeader()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Errorf("the leader's own error = %v; want its cancellation", err)
	}
	if got := <-waiter; got != "second" {
		t.Errorf("the waiter got %q; want its own mint", got)
	}
	if mints.Load() != 2 {
		t.Errorf("mints = %d; want 2 (the leader's aborted one and the waiter's)", mints.Load())
	}
}

// size is the number of held tokens: a test-side reader (the unused-symbol
// ratchet keeps production code free of symbols only tests call).
func (c *proxyTokens) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.held)
}
