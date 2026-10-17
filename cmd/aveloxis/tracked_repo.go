// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/platform"
)

// errRepoNotTracked: the URL names no repository Aveloxis tracks.
var errRepoNotTracked = errors.New("repository is not tracked")

// trackedRepoID resolves a repository URL to the id of the tracked row, as
// UpsertRepo stores it (suffix and case variants included), without writing
// anything. Commands that act on an existing repository (prioritize,
// recollect) use it; before H-5 they called UpsertRepo, so a typo or an
// untracked URL created a repos row with no queue entry.
func trackedRepoID(ctx context.Context, store *db.PostgresStore, gitURL string) (int64, error) {
	id, err := store.FindRepoByURL(ctx, gitURL)
	if err != nil {
		return 0, fmt.Errorf("look up %s: %w", platform.RedactURLUserinfo(gitURL), err)
	}
	if id == 0 {
		return 0, fmt.Errorf("%s: %w — add it first with `aveloxis add-repo`", platform.RedactURLUserinfo(gitURL), errRepoNotTracked)
	}
	return id, nil
}
