// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package monitor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/model"
)

// faultStore fails the one call named by fail with a store-text error and
// answers every other call with one queued job.
type faultStore struct{ fail string }

const storeText = "closed pool: aveloxis_ops.collection_queue"

func (f faultStore) err(call string) error {
	if f.fail == call {
		return errors.New(storeText)
	}
	return nil
}

func (f faultStore) QueueStats(context.Context) (map[string]int, error) {
	if err := f.err("QueueStats"); err != nil {
		return nil, err
	}
	return map[string]int{"queued": 1}, nil
}

func (f faultStore) ListQueuePage(_ context.Context, limit, offset int, _, _, _ string) ([]db.QueueJob, int, error) {
	if err := f.err("ListQueuePage"); err != nil {
		return nil, 0, err
	}
	return []db.QueueJob{{RepoID: 7, Status: "queued", DueAt: time.Now()}}, 1, nil
}

func (f faultStore) GetReposBatch(context.Context, []int64) (map[int64]*model.Repo, error) {
	if err := f.err("GetReposBatch"); err != nil {
		return nil, err
	}
	return map[int64]*model.Repo{7: {Owner: "o", Name: "r", GitURL: "https://github.com/o/r", Platform: model.PlatformGitHub}}, nil
}

func (f faultStore) GetRepoStatsBatch(context.Context, []int64) (map[int64]*db.RepoStats, error) {
	if err := f.err("GetRepoStatsBatch"); err != nil {
		return nil, err
	}
	return map[int64]*db.RepoStats{}, nil
}

func (f faultStore) PrioritizeRepo(context.Context, int64) error { return f.err("PrioritizeRepo") }

// TestMonitorStoreFailuresAreLoggedGeneric500s — PR #218 review C5: the
// stats and queue endpoints wrote the store's error text into their 500
// bodies and logged nothing, and the dashboard discarded its read errors
// and rendered an outage as an empty fleet. Every store failure is one
// logged ERROR naming the handler and a generic 500 body; with no fault
// each surface answers 200.
func TestMonitorStoreFailuresAreLoggedGeneric500s(t *testing.T) {
	// An enrichment read (names, counts) that fails is not an outage of the
	// page since 0.29.109: the one loader (queueview.Load) answers the rows
	// bare, ids shown, and logs the failure as a WARN — the convention the
	// web GUI and the API already had (58 §7 item 10). bare marks those.
	for _, tc := range []struct {
		method, path, fail, handler string
		bare                        bool
	}{
		{http.MethodGet, "/api/stats", "QueueStats", "handleStats", false},
		{http.MethodGet, "/api/queue", "ListQueuePage", "handleQueue", false},
		{http.MethodGet, "/", "QueueStats", "handleDashboard", false},
		{http.MethodGet, "/", "ListQueuePage", "handleDashboard", false},
		{http.MethodGet, "/", "GetReposBatch", "handleDashboard", true},
		{http.MethodGet, "/", "GetRepoStatsBatch", "handleDashboard", true},
		{http.MethodPost, "/api/prioritize/7", "PrioritizeRepo", "handlePrioritize", false},
	} {
		t.Run(tc.path+" "+tc.fail, func(t *testing.T) {
			serve := func(fail string) (*httptest.ResponseRecorder, string) {
				var logs bytes.Buffer
				s := newServer(faultStore{fail: fail}, slog.New(slog.NewTextHandler(&logs, nil)), Options{})
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
				return w, logs.String()
			}
			if w, _ := serve(""); w.Code != http.StatusOK {
				t.Fatalf("with no fault = %d %q; want 200 (the fake must reach the faulted call)", w.Code, w.Body.String())
			}
			w, logs := serve(tc.fail)
			if tc.bare {
				body := w.Body.String()
				label := `<td data-label="Repo">o/r</td>` // the names read; the counts did not
				if tc.fail == "GetReposBatch" {
					label = `<td data-label="Repo">7</td>` // the names did not: the id, never a bare slash
				}
				if w.Code != http.StatusOK || !strings.Contains(body, label) || !strings.Contains(body, "could not be read") {
					t.Errorf("a failed %s = %d; want 200 with the row %s and the banner saying what could not be read\n%s", tc.fail, w.Code, label, body)
				}
				if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "unreadable") || !strings.Contains(logs, "closed pool") {
					t.Errorf("the enrichment failure is not logged at WARN with its cause:\n%s", logs)
				}
				return
			}
			if w.Code != http.StatusInternalServerError {
				t.Errorf("a failed %s = %d; want 500", tc.fail, w.Code)
			}
			if strings.Contains(w.Body.String(), "closed pool") || strings.Contains(w.Body.String(), "aveloxis_ops") {
				t.Errorf("the 500 body carries the store's text: %q", w.Body.String())
			}
			if !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "handler="+tc.handler) || !strings.Contains(logs, "closed pool") {
				t.Errorf("the failure is not logged at ERROR with handler=%s and its cause:\n%s", tc.handler, logs)
			}
		})
	}
}

// TestMonitorAbandonedRequestIsNotAnError: a client that left mid-request
// reaches the arm with context.Canceled; nobody is listening, so it is not
// an ERROR (the API's serverError rule, batch 5 review round 1).
func TestMonitorAbandonedRequestIsNotAnError(t *testing.T) {
	var logs bytes.Buffer
	s := newServer(faultStore{}, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Options{})
	// NET-6 review r5 F1: "abandoned" is the REQUEST's context being done,
	// not the error's type.
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	s.serverError(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil).WithContext(gone), "probe", context.Canceled)
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a canceled request logged at ERROR:\n%s", logs.String())
	}
}
