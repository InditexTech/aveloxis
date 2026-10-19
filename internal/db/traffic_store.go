// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aveloxis/aveloxis/internal/model"
)

// errTrafficNoSnapshotDate refuses a traffic read whose snapshot date
// was never set: the date keys which path and referrer rows the read
// replaces, and a zero time would file the snapshot under 0001-01-01
// while replacing nothing.
var errTrafficNoSnapshotDate = errors.New("traffic read has no snapshot date")

// trafficDate is the UTC calendar day of t as the "YYYY-MM-DD" text the
// SQL casts with ::date. Passing text keeps the day independent of the
// session's TimeZone, which the integration tier sets to
// America/Chicago: a UTC midnight sent as a timestamp would land on the
// previous day there.
func trafficDate(t time.Time) string {
	return t.UTC().Format(time.DateOnly)
}

// UpsertRepoTraffic writes one traffic read (the daily clones go through
// UpsertRepoClone) in one transaction, so the window rows that mark a
// successful read never exist without the rest of it (v0.29.97,
// aveloxis/aveloxis#234). The latest read wins: days, windows and weeks
// upsert on their UNIQUE key, and the snapshot of tr.SnapshotDate is
// replaced as a whole, so an entry that left the top 10 is not kept.
// Zero counts are written like any other. The write stamps
// repos.data_changed_at itself: the staged processor runs outside jobs.
func (s *PostgresStore) UpsertRepoTraffic(ctx context.Context, repoID int64, tr *model.RepoTraffic) error {
	if tr.SnapshotDate.IsZero() {
		return errTrafficNoSnapshotDate
	}
	snapshot := trafficDate(tr.SnapshotDate)
	return s.withRetry(ctx, func(ctx context.Context) error {
		batch := &pgx.Batch{}
		for _, v := range tr.Views {
			batch.Queue(`
				INSERT INTO aveloxis_data.repo_views
					(repo_id, view_timestamp, total_views, unique_views, data_source)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (repo_id, view_timestamp) DO UPDATE SET
					total_views = EXCLUDED.total_views,
					unique_views = EXCLUDED.unique_views,
					tool_version = EXCLUDED.tool_version,
					data_collection_date = NOW()`,
				repoID, v.Timestamp, v.TotalViews, v.UniqueViews, v.Origin.DataSource)
		}
		for _, w := range tr.Windows {
			batch.Queue(`
				INSERT INTO aveloxis_data.repo_traffic_windows
					(repo_id, traffic_kind, window_start, window_end, uniques)
				VALUES ($1, $2, $3::date, $4::date, $5)
				ON CONFLICT (repo_id, traffic_kind, window_start, window_end) DO UPDATE SET
					uniques = EXCLUDED.uniques,
					tool_version = EXCLUDED.tool_version,
					data_collection_date = NOW()`,
				repoID, string(w.Kind), trafficDate(w.Start), trafficDate(w.End), w.Uniques)
		}
		for _, w := range tr.Weeks {
			batch.Queue(`
				INSERT INTO aveloxis_data.repo_traffic_weeks
					(repo_id, traffic_kind, week_start, uniques)
				VALUES ($1, $2, $3::date, $4)
				ON CONFLICT (repo_id, traffic_kind, week_start) DO UPDATE SET
					uniques = EXCLUDED.uniques,
					tool_version = EXCLUDED.tool_version,
					data_collection_date = NOW()`,
				repoID, string(w.Kind), trafficDate(w.WeekStart), w.Uniques)
		}

		// The snapshot: the date's earlier rows go first, in the same
		// transaction, so the inserts below can only conflict with an
		// entry this read lists twice (GitHub lists each once).
		batch.Queue(`
			DELETE FROM aveloxis_data.repo_traffic_paths
			WHERE repo_id = $1 AND snapshot_date = $2::date`, repoID, snapshot)
		batch.Queue(`
			DELETE FROM aveloxis_data.repo_traffic_referrers
			WHERE repo_id = $1 AND snapshot_date = $2::date`, repoID, snapshot)
		for _, p := range tr.Paths {
			batch.Queue(`
				INSERT INTO aveloxis_data.repo_traffic_paths
					(repo_id, snapshot_date, path, title, total_views, unique_views)
				VALUES ($1, $2::date, $3, $4, $5, $6)
				ON CONFLICT (repo_id, snapshot_date, path) DO NOTHING`,
				repoID, snapshot, p.Path, p.Title, p.TotalViews, p.UniqueViews)
		}
		for _, r := range tr.Referrers {
			batch.Queue(`
				INSERT INTO aveloxis_data.repo_traffic_referrers
					(repo_id, snapshot_date, referrer, total_views, unique_views)
				VALUES ($1, $2::date, $3, $4, $5)
				ON CONFLICT (repo_id, snapshot_date, referrer) DO NOTHING`,
				repoID, snapshot, r.Referrer, r.TotalViews, r.UniqueViews)
		}

		// The repository page's cached traffic answer moves with the rows.
		batch.Queue(stampRepoCacheStateSQL+` WHERE repo_id = $1`, repoID)
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}
