// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package monitor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/queueview"
)

// neverListed is the fake store for an over-long search: the dashboard is
// unauthenticated, and the trigram scan a request-line-sized q would cost
// must never run — a listing call fails the test.
type neverListed struct {
	fakeMonitorStore
	t *testing.T
}

func (n neverListed) ListQueuePage(context.Context, int, int, string, string, string) ([]db.QueueJob, int, error) {
	n.t.Fatal("an over-long search reached the store")
	return nil, 0, nil
}

func TestOverLongSearchNeverReachesTheStoreAndSaysSo(t *testing.T) {
	s := newServer(neverListed{t: t}, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	q := "?q=" + strings.Repeat("a", queueview.MaxSearchBytes+1)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+q, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "longer than any repository name") || !strings.Contains(w.Body.String(), "Matched 0 repos") {
		t.Errorf("dashboard with an over-long q = %d; want 200 saying nothing matches\n%s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/queue"+q, nil))
	var env struct {
		Total    int      `json:"total"`
		Jobs     []any    `json:"jobs"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || w.Code != http.StatusOK {
		t.Fatalf("/api/queue with an over-long q = %d, %v\n%s", w.Code, err, w.Body.String())
	}
	if env.Total != 0 || len(env.Jobs) != 0 || len(env.Warnings) != 1 || env.Warnings[0] != queueview.WarnSearchTooLong {
		t.Errorf("/api/queue envelope = %+v; want an empty page with the one warning", env)
	}
}
