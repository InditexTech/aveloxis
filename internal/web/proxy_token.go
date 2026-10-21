// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"sync"
	"time"

	"github.com/aveloxis/aveloxis/internal/httpserver"
)

// proxyTokens holds, per signed-in web session, the Bearer token the
// /api/ proxy attaches on the session's behalf (0.29.110, stage 4 of
// summary/59, D-9/C1): minted once through the store from the web session
// (kind "api", parent = the session, so signing out revokes it), held in
// this process only — never in a page, never in a cookie — and attached
// server-side. Keyed by the session cookie's hash (db.HashToken), so the
// raw session value is not held beyond its request. An entry expires with
// the session or the token, whichever is first, and is swept on every
// mint; sign-out forgets it at once. One mint per key at a time: a second
// request for the same session while the first mint is in flight waits
// for it rather than minting a twin.
type proxyTokens struct {
	mu       sync.Mutex
	held     map[string]proxyToken
	inflight map[string]*proxyMint
}

type proxyToken struct {
	bearer  string
	expires time.Time
}

type proxyMint struct {
	ctx    context.Context // the leader's request, for the one request-ended rule
	done   chan struct{}
	bearer string
	err    error
}

// bearer answers the held token for key, or mints one through mint and
// holds it until expires. A failed mint holds nothing and is returned to
// every waiter — except a failure that is the leader's own request ending
// (httpserver.RequestEnded on the leader's context: decided by the
// request, never by the error's type — a database connect timeout wraps
// DeadlineExceeded too): that says nothing about the waiter's request, so
// the waiter mints for itself (round-1 review of 0.29.110, L-1: an aborted
// tab's first request failed the other tab's). Under the server's request
// bound a leader's context is cancelled the moment its handler returns, so
// a waiter scheduled after that mints for itself whatever the leader's
// error was: one serialized retry per live request, never a twin.
func (c *proxyTokens) bearer(ctx context.Context, key string, now, expires time.Time, mint func(context.Context) (string, error)) (string, error) {
	for {
		c.mu.Lock()
		if c.held == nil {
			c.held = map[string]proxyToken{}
			c.inflight = map[string]*proxyMint{}
		}
		if e, ok := c.held[key]; ok && now.Before(e.expires) {
			c.mu.Unlock()
			return e.bearer, nil
		}
		m, ok := c.inflight[key]
		if !ok {
			break
		}
		c.mu.Unlock()
		<-m.done
		if m.err == nil || !httpserver.RequestEnded(m.ctx, m.err) {
			return m.bearer, m.err
		}
		// The waiter's own request ended too. Through the one request-end
		// rule on purpose (the pin routes every such decision through it);
		// its ErrHandlerTimeout clause cannot be true of a store error, so
		// this is ctx.Err() != nil in practice (round-2 I-1, declined as a
		// bare test: a second spelling of the rule).
		if httpserver.RequestEnded(ctx, m.err) {
			return "", ctx.Err()
		}
	}
	m := &proxyMint{ctx: ctx, done: make(chan struct{})}
	c.inflight[key] = m
	c.mu.Unlock()

	bearer, err := mint(ctx)

	c.mu.Lock()
	delete(c.inflight, key)
	if err == nil {
		c.held[key] = proxyToken{bearer: bearer, expires: expires}
		for k, e := range c.held { // the sweep: entries of sessions that ended without a sign-out
			if !now.Before(e.expires) {
				delete(c.held, k)
			}
		}
	}
	m.bearer, m.err = bearer, err
	close(m.done)
	c.mu.Unlock()
	return bearer, err
}

// forget drops the session's token (sign-out: the store revoked it).
func (c *proxyTokens) forget(key string) {
	c.mu.Lock()
	delete(c.held, key)
	c.mu.Unlock()
}
