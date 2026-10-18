// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"context"
	"fmt"

	"github.com/aveloxis/aveloxis/internal/db"
)

// SetUserAdmin grants or removes the administrator role. An administrator
// never demotes their own account (ErrSelfDemotion: both GUIs hide the
// control; only a crafted request reaches this); a target outside
// 1..MaxUserID is an InvalidInput refusal (ErrInvalidInput) before any
// store read; the store refuses to demote the last administrator atomically
// (db.ErrLastAdmin) and answers db.ErrUserNotFound for no such user. The process's RoleChanged hook runs
// after the write, so the target's next request sees the role.
func (s *Service) SetUserAdmin(ctx context.Context, a Actor, target int, admin bool) error {
	const op = "set user admin"
	// The caller's own input is refused before any store read (PR #218
	// review C6): a self-demotion answers the same with the database down.
	if !admin && a.Kind != Operator && target == a.UserID {
		s.logger.Warn("admin: self-demotion refused", "actor", a.String())
		return ErrSelfDemotion
	}
	// A target the column cannot hold is refused here, whatever width the
	// adapter parsed at (SR-18; 0.29.103 review F2).
	if !validUserID(target) {
		return refuseInput("user id must be a positive integer")
	}
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return err
	}
	if err := s.st.SetUserAdmin(ctx, target, admin); err != nil {
		if _, refused := Classify(err); refused {
			s.refused(op, a, err, "target_user_id", target, "admin", admin)
			return err
		}
		return fmt.Errorf("%s %d=%t by %s: %w", op, target, admin, a, err)
	}
	s.hooks.RoleChanged(target)
	s.audit(op, a, "target_user_id", target, "admin", admin)
	return nil
}

// compile-time: the refusals this operation classifies are the store's.
var (
	_ = db.ErrLastAdmin
	_ = db.ErrUserNotFound
)
