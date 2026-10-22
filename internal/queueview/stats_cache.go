// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package queueview

// The queue stats cache (moved from internal/monitor in 0.29.109, stage
// 3d): QueueStats is a `SELECT status, COUNT(*) … GROUP BY status` over
// a 100K-row queue table; at v0.18.29 every dashboard render fired it,
// per browser tab, every 10 seconds. The cache runs it at most once per
// TTL window and tells the page when it last ran and when it runs next.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aveloxis/aveloxis/internal/httpserver"
)

// DefaultStatsTTL is the cache window the monitors use: long enough to
// soak a tab's auto-refresh, short enough that an operator does not
// notice the staleness.
const DefaultStatsTTL = 60 * time.Second

// StatsLoader is what the cache calls to refill: the store's QueueStats.
type StatsLoader func(ctx context.Context) (map[string]int, error)

// StatsCache holds the one QueueStats result with the time it was
// filled. Stale-on-error: when the loader fails and a value is held, the
// held value is served and the refresh counts as skipped this tick, so
// the page stays usable through a brief database hiccup; the failure is
// logged at WARN with the age of what is served (round-2 review of
// 0.29.109: the web page had logged a 500 before the move, the cache
// said nothing for a whole outage); the error is returned only when
// nothing is held.
type StatsCache struct {
	ttl    time.Duration
	logger *slog.Logger

	mu       sync.Mutex
	cached   map[string]int
	filledAt time.Time
}

// NewStatsCache returns a cache with the given TTL; logger carries the
// refill failures the stale answers would otherwise hide.
func NewStatsCache(ttl time.Duration, logger *slog.Logger) *StatsCache {
	return &StatsCache{ttl: ttl, logger: logger}
}

// Get returns the stats, refilling through loader when the held value is
// older than the TTL, with the time of the latest fill and the time of
// the next due refresh.
func (c *StatsCache) Get(ctx context.Context, loader StatsLoader) (map[string]int, time.Time, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if c.cached != nil && now.Sub(c.filledAt) < c.ttl {
		return c.cached, c.filledAt, c.filledAt.Add(c.ttl), nil
	}
	stats, err := loader(ctx)
	if err != nil {
		if c.cached != nil {
			httpserver.LogFailure(ctx, c.logger, slog.LevelWarn, err, "queue stats refresh failed; serving the counts of the last fill",
				"filled_at", c.filledAt.UTC().Format(time.RFC3339), "age", now.Sub(c.filledAt).Round(time.Second).String(), "error", err)
			return c.cached, c.filledAt, c.filledAt.Add(c.ttl), nil
		}
		return nil, time.Time{}, time.Time{}, err
	}
	c.cached = stats
	c.filledAt = now
	return c.cached, c.filledAt, c.filledAt.Add(c.ttl), nil
}
