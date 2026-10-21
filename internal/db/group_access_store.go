// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GroupOwner is the one primitive behind the group access rule (stage 1 of
// summary/59, eval-2 item 5): it answers who owns the group and what it is
// called, and nothing else. The RULE — the owner reads and writes, an
// administrator's session reads, everyone else is refused — lives in
// internal/admin (Service.GroupAccess); the store never takes an
// "isAdmin" from a caller (SR-18). ErrGroupNotFound for no such group; any
// other error is the lookup's failure, wrapped (SR-5: a failed lookup is
// not "not yours").
func (s *PostgresStore) GroupOwner(ctx context.Context, groupID int64) (ownerID int, name string, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT user_id, name FROM aveloxis_ops.user_groups WHERE group_id = $1`, groupID).Scan(&ownerID, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", fmt.Errorf("group %d: %w", groupID, ErrGroupNotFound)
	}
	if err != nil {
		return 0, "", fmt.Errorf("look up group %d owner: %w", groupID, err)
	}
	return ownerID, name, nil
}

// RepoInGroup reports whether the repository is linked into the group
// (aveloxis_ops.user_repos). A lookup failure is returned, never read as
// "not a member" (SR-16).
func (s *PostgresStore) RepoInGroup(ctx context.Context, groupID, repoID int64) (bool, error) {
	var in bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM aveloxis_ops.user_repos WHERE group_id = $1 AND repo_id = $2)`,
		groupID, repoID).Scan(&in)
	if err != nil {
		return false, fmt.Errorf("repo %d in group %d: %w", repoID, groupID, err)
	}
	return in, nil
}
