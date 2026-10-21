// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aveloxis/aveloxis/internal/db"
)

// The API-token operations (stage 3b of summary/59, 0.29.109): granting a
// token to an account, revoking one and changing the defaults the grant
// form offers. The rules were the REST handlers' (v0.29.82); they are the
// service's now (SR-18), so the console page and the API refuse the same
// inputs with the same words. The token itself is returned once, by the
// grant, and never stored or logged.

// MaxAPITokenLifetimeDays is the store's maximum (db.MaxAPITokenLifetimeDays,
// 365: the operator's decision, the ASVS review's A3), checked here so the
// refusal names the bound.
const MaxAPITokenLifetimeDays = int64(db.MaxAPITokenLifetimeDays)

// MaxAPITokenRateLimit is the largest allowance the columns hold
// (rate_limit_per_hour and rate_limit_per_day are INT).
const MaxAPITokenRateLimit = math.MaxInt32

// ValidLifetimeDays and ValidRateLimit are the two bounds every token
// value and default is held to.
func ValidLifetimeDays(d int64) bool { return d > 0 && d <= MaxAPITokenLifetimeDays }
func ValidRateLimit(n int64) bool    { return n > 0 && n <= MaxAPITokenRateLimit }

// TokenGrant is a grant as an adapter decoded it: a nil value takes the
// current default.
type TokenGrant struct {
	UserID           int
	Label            string
	LifetimeDays     *int64
	RateLimitPerHour *int64
	RateLimitPerDay  *int64
}

// TokenDefaults are the grant form's defaults as an adapter decoded them.
// RateLimitPerHour and LifetimeDays are required; a nil RateLimitPerDay
// leaves the daily default unchanged (a GUI older than v0.29.89 sends none).
type TokenDefaults struct {
	RateLimitPerHour *int64
	RateLimitPerDay  *int64
	LifetimeDays     *int64
}

// GrantAPIToken mints a token for an account. The account id is bounded
// to the column, the label is required and bounded, the lifetime and the
// allowances take the current defaults when omitted and are bounded; each
// refusal is an InvalidInput with the field's rule, before any write. An
// account that does not exist is the store's db.ErrUserNotFound. The raw
// token is returned once with its row; the audit line never carries it.
func (s *Service) GrantAPIToken(ctx context.Context, a Actor, g TokenGrant) (string, db.APIToken, error) {
	const op = "grant API token"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return "", db.APIToken{}, err
	}
	if !validUserID(g.UserID) { // users.user_id is int4 (58 §7 item 19: a larger value was a 500)
		return "", db.APIToken{}, refuseInput("user_id names the account the token authenticates as")
	}
	if strings.TrimSpace(g.Label) == "" {
		return "", db.APIToken{}, refuseInput("label is required (who the token is for, or what it is used by)")
	}
	if utf8.RuneCountInString(strings.TrimSpace(g.Label)) > db.MaxAPITokenLabelLength {
		return "", db.APIToken{}, refuseInput("label is at most " + strconv.Itoa(db.MaxAPITokenLabelLength) + " characters")
	}
	settings, err := s.st.GetAPITokenSettings(ctx)
	if err != nil {
		return "", db.APIToken{}, fmt.Errorf("%s by %s: read the defaults: %w", op, a, err)
	}
	days := int64(settings.DefaultLifetimeDays)
	if g.LifetimeDays != nil {
		days = *g.LifetimeDays
	}
	limit := int64(settings.DefaultRateLimitPerHour)
	if g.RateLimitPerHour != nil {
		limit = *g.RateLimitPerHour
	}
	daily := int64(settings.DefaultRateLimitPerDay)
	if g.RateLimitPerDay != nil {
		daily = *g.RateLimitPerDay
	}
	if !ValidLifetimeDays(days) {
		return "", db.APIToken{}, refuseInput("lifetime_days must be a positive number of days, at most " + strconv.FormatInt(MaxAPITokenLifetimeDays, 10))
	}
	if !ValidRateLimit(limit) {
		return "", db.APIToken{}, refuseInput("rate_limit_per_hour must be a positive number, at most " + strconv.Itoa(MaxAPITokenRateLimit))
	}
	if !ValidRateLimit(daily) {
		return "", db.APIToken{}, refuseInput("rate_limit_per_day must be a positive number, at most " + strconv.Itoa(MaxAPITokenRateLimit))
	}
	raw, tok, err := s.st.CreateAPIToken(ctx, db.APITokenGrant{
		UserID: g.UserID, Label: g.Label, CreatedBy: a.UserID,
		Lifetime: time.Duration(days) * 24 * time.Hour, RateLimitPerHour: int(limit), RateLimitPerDay: int(daily),
	})
	if err != nil {
		if _, refused := Classify(err); refused {
			s.refused(op, a, err, "user_id", g.UserID)
			return "", db.APIToken{}, err
		}
		return "", db.APIToken{}, fmt.Errorf("%s for user %d by %s: %w", op, g.UserID, a, err)
	}
	s.audit(op, a, "token_id", tok.TokenID, "user_id", tok.UserID,
		"rate_limit_per_hour", tok.RateLimitPerHour, "rate_limit_per_day", tok.RateLimitPerDay, "expires_at", tok.ExpiresAt)
	return raw, tok, nil
}

// RevokeAPIToken ends a token. The API rechecks a token's row on every
// call (APITokenActive, ASVS V7.4.1), so a revoked token stops at its next
// call in every api process whatever process revoked it; the TokensChanged
// hook that runs after the write only drops the revoking process's cached
// validations (the owner's role and scope), so nothing of the token lingers
// in them. No such token is db.ErrAPITokenNotFound (NotFound).
func (s *Service) RevokeAPIToken(ctx context.Context, a Actor, tokenID int64) error {
	const op = "revoke API token"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return err
	}
	if tokenID <= 0 {
		return refuseInput("token id must be a positive integer")
	}
	if err := s.st.RevokeAPIToken(ctx, tokenID, a.UserID); err != nil {
		if _, refused := Classify(err); refused {
			s.refused(op, a, err, "token_id", tokenID)
			return err
		}
		return fmt.Errorf("%s %d by %s: %w", op, tokenID, a, err)
	}
	s.hooks.TokensChanged()
	s.audit(op, a, "token_id", tokenID)
	return nil
}

// SetAPITokenDefaults changes the grant form's defaults. The allowances
// are the token quotas (the Capacity page edits the same rows): a quota
// aveloxis.json decides is refused by the store (db.ErrInvalidCapacitySettings),
// answered as a Conflict naming the file. Returns the saved values.
func (s *Service) SetAPITokenDefaults(ctx context.Context, a Actor, d TokenDefaults) (db.APITokenSettings, error) {
	const op = "set API token defaults"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return db.APITokenSettings{}, err
	}
	if d.RateLimitPerHour == nil || !ValidRateLimit(*d.RateLimitPerHour) {
		return db.APITokenSettings{}, refuseInput("default_rate_limit_per_hour must be a positive number, at most " + strconv.Itoa(MaxAPITokenRateLimit))
	}
	if d.LifetimeDays == nil || !ValidLifetimeDays(*d.LifetimeDays) {
		return db.APITokenSettings{}, refuseInput("default_lifetime_days must be a positive number of days, at most " + strconv.FormatInt(MaxAPITokenLifetimeDays, 10))
	}
	if d.RateLimitPerDay != nil && !ValidRateLimit(*d.RateLimitPerDay) {
		return db.APITokenSettings{}, refuseInput("default_rate_limit_per_day must be a positive number, at most " + strconv.Itoa(MaxAPITokenRateLimit))
	}
	st := db.APITokenSettings{DefaultRateLimitPerHour: int(*d.RateLimitPerHour), DefaultLifetimeDays: int(*d.LifetimeDays)}
	if d.RateLimitPerDay != nil {
		st.DefaultRateLimitPerDay = int(*d.RateLimitPerDay)
	}
	if err := s.st.SetAPITokenSettings(ctx, st, a.UserID); err != nil {
		if errors.Is(err, db.ErrInvalidCapacitySettings) {
			s.refused(op, a, err)
			return db.APITokenSettings{}, refuseConflict(err.Error())
		}
		return db.APITokenSettings{}, fmt.Errorf("%s by %s: %w", op, a, err)
	}
	saved, err := s.st.GetAPITokenSettings(ctx)
	if err != nil {
		return db.APITokenSettings{}, fmt.Errorf("%s by %s: read back: %w", op, a, err)
	}
	s.audit(op, a, "default_rate_limit_per_hour", saved.DefaultRateLimitPerHour,
		"default_rate_limit_per_day", saved.DefaultRateLimitPerDay, "default_lifetime_days", saved.DefaultLifetimeDays)
	return saved, nil
}
