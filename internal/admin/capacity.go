// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/db"
)

// The capacity operations (stage 3b of summary/59, 0.29.109): a quota's
// value and mode, the contact address a refusal names, an account's
// overrides and the sign-up allowlist. The rules were the REST handlers'
// (v0.29.89, summary/53); they are the service's now (SR-18).

// QuotaChange is a quota's new value and mode as an adapter decoded them.
type QuotaChange struct {
	Allowed *int64
	Mode    string
}

// SetCapacityQuota changes a shipped quota. An unknown name is a NotFound
// (the route's own existence, 58 §7 item 12), a bad value or mode an
// InvalidInput, and a quota whose word is aveloxis.json's a Conflict
// naming the file: only a quota the admin page decides (SourceWeb) is
// written. Returns the mode as parsed.
func (s *Service) SetCapacityQuota(ctx context.Context, a Actor, name string, c QuotaChange) (capacity.Mode, error) {
	const op = "set capacity quota"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return "", err
	}
	if _, known := capacity.Shipped[name]; !known {
		return "", refuseNotFound("no quota named " + strconv.Quote(name))
	}
	mode, err := capacity.ParseMode(c.Mode)
	if err != nil || c.Allowed == nil || !ValidRateLimit(*c.Allowed) {
		return "", refuseInput("allowed must be a positive number, at most " + strconv.Itoa(MaxAPITokenRateLimit) + ", and mode one of enforce, shadow, off")
	}
	if src := s.st.CapacitySource(name); src != capacity.SourceWeb {
		return "", refuseConflict(name + " is set in aveloxis.json (" + string(src) + "); change it there")
	}
	if err := s.st.SetCapacityQuota(ctx, name, int(*c.Allowed), mode, a.UserID); err != nil {
		// The store re-validates the same four conditions; a refusal from it
		// is answered as one, never wrapped with the actor into a client body
		// (round-1 review F3).
		if _, refused := Classify(err); refused {
			s.refused(op, a, err, "quota", name)
			return "", err
		}
		return "", fmt.Errorf("%s %s by %s: %w", op, name, a, err)
	}
	s.audit(op, a, "quota", name, "allowed", *c.Allowed, "mode", string(mode))
	return mode, nil
}

// SetCapacityContact changes the address a capacity refusal names; an
// empty string clears it. The store's refusal of the value is an
// InvalidInput naming the rule.
func (s *Service) SetCapacityContact(ctx context.Context, a Actor, email string) error {
	const op = "set capacity contact"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return err
	}
	if err := s.st.SetCapacityContact(ctx, email, a.UserID); err != nil {
		if errors.Is(err, db.ErrInvalidCapacitySettings) {
			return refuseInput("contact_email must be an email address of at most " + strconv.Itoa(db.MaxContactEmailBytes) + " bytes, or empty")
		}
		return fmt.Errorf("%s by %s: %w", op, a, err)
	}
	s.audit(op, a, "cleared", strings.TrimSpace(email) == "") // the address is personal data: the actor and whether it was cleared, never the value
	return nil
}

// SetCapacityOverride sets an account's overrides (nil: the quota's
// value; all nil and no note removes them). The account id is bounded to
// the column; the store's refusal of the values is an InvalidInput naming
// the rule; no such account is db.ErrUserNotFound.
func (s *Service) SetCapacityOverride(ctx context.Context, a Actor, o db.CapacityOverride) error {
	const op = "set capacity override"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return err
	}
	if !validUserID(o.UserID) {
		return refuseInput("user id must be a positive integer")
	}
	if err := s.st.SetCapacityOverride(ctx, o, a.UserID); err != nil {
		if errors.Is(err, db.ErrInvalidCapacitySettings) {
			return refuseInput("each value must be a positive number (or null for the quota's value); the note is at most " + strconv.Itoa(db.MaxCapacityNoteLength) + " characters")
		}
		if _, refused := Classify(err); refused {
			s.refused(op, a, err, "user_id", o.UserID)
			return err
		}
		return fmt.Errorf("%s for user %d by %s: %w", op, o.UserID, a, err)
	}
	s.audit(op, a, "user_id", o.UserID)
	return nil
}

// AddSignupAllowlist adds a network whose sign-ups bypass the sign-up
// quota. Returns the network as stored.
func (s *Service) AddSignupAllowlist(ctx context.Context, a Actor, cidr, note string) (string, error) {
	const op = "add sign-up allowlist entry"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return "", err
	}
	stored, err := s.st.AddSignupAllowlist(ctx, cidr, note, a.UserID)
	if err != nil {
		if errors.Is(err, db.ErrInvalidCIDR) || errors.Is(err, db.ErrInvalidCapacitySettings) {
			return "", refuseInput("cidr must be a network in CIDR form (such as 192.0.2.0/24); the note is at most " + strconv.Itoa(db.MaxCapacityNoteLength) + " characters")
		}
		return "", fmt.Errorf("%s by %s: %w", op, a, err)
	}
	s.audit(op, a, "cidr", stored)
	return stored, nil
}

// RemoveSignupAllowlist removes a network; false when it was not listed
// (the adapters answer 404).
func (s *Service) RemoveSignupAllowlist(ctx context.Context, a Actor, cidr string) (bool, error) {
	const op = "remove sign-up allowlist entry"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return false, err
	}
	// The one network rule first, so the audit line names the row removed
	// (192.0.2.7/24 removes 192.0.2.0/24; round-3 review of 0.29.109) and a
	// non-network never reaches the store.
	norm, err := db.NormalizeCIDR(cidr)
	if err != nil {
		return false, refuseInput("cidr must be a network in CIDR form")
	}
	removed, err := s.st.RemoveSignupAllowlist(ctx, norm)
	if err != nil {
		return false, fmt.Errorf("%s by %s: %w", op, a, err)
	}
	if removed {
		s.audit(op, a, "cidr", norm)
	}
	return removed, nil
}
