// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
)

// Stage 3b of summary/59 (0.29.109): the API-token rules the REST
// handlers held — the account bound, the label, the lifetime and the
// allowances with their defaults — are the service's (SR-18), so the
// console page and the API refuse the same inputs with the same words.

func adminActor() Actor { return Actor{UserID: 1, Kind: Session} }

func adminStore(t *testing.T) *fakeStore {
	return &fakeStore{t: t, isUserAdmin: func(_ context.Context, id int) (bool, error) { return id == 1, nil }}
}

func i64(n int64) *int64 { return &n }

func TestGrantAPITokenAppliesTheDefaults(t *testing.T) {
	st := adminStore(t)
	st.getAPITokenSettings = func(context.Context) (db.APITokenSettings, error) {
		return db.APITokenSettings{DefaultRateLimitPerHour: 1000, DefaultRateLimitPerDay: 10000, DefaultLifetimeDays: 30}, nil
	}
	var got db.APITokenGrant
	st.createAPIToken = func(_ context.Context, g db.APITokenGrant) (string, db.APIToken, error) {
		got = g
		return "avx_raw", db.APIToken{TokenID: 9, UserID: g.UserID, Label: g.Label}, nil
	}
	svc := New(st, Options{}, nil, discardLogger())
	raw, tok, err := svc.GrantAPIToken(context.Background(), adminActor(), TokenGrant{UserID: 5, Label: "reporting"})
	if err != nil || raw != "avx_raw" || tok.TokenID != 9 {
		t.Fatalf("grant = %q, %+v, %v", raw, tok, err)
	}
	want := db.APITokenGrant{UserID: 5, Label: "reporting", CreatedBy: 1, Lifetime: 30 * 24 * time.Hour, RateLimitPerHour: 1000, RateLimitPerDay: 10000}
	if got != want {
		t.Errorf("the store received %+v, want the defaults applied: %+v", got, want)
	}
	// Explicit values win over the defaults.
	_, _, err = svc.GrantAPIToken(context.Background(), adminActor(), TokenGrant{UserID: 5, Label: "x", LifetimeDays: i64(7), RateLimitPerHour: i64(10), RateLimitPerDay: i64(20)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifetime != 7*24*time.Hour || got.RateLimitPerHour != 10 || got.RateLimitPerDay != 20 {
		t.Errorf("explicit values not applied: %+v", got)
	}
}

func TestGrantAPITokenRefusesBeforeTheStore(t *testing.T) {
	long := strings.Repeat("x", db.MaxAPITokenLabelLength+1)
	cases := []struct {
		name   string
		grant  TokenGrant
		detail string
	}{
		{"user id zero", TokenGrant{UserID: 0, Label: "x"}, "user_id names the account the token authenticates as"},
		{"user id too large", TokenGrant{UserID: MaxUserID + 1, Label: "x"}, "user_id names the account the token authenticates as"},
		{"label empty", TokenGrant{UserID: 5, Label: "  "}, "label is required (who the token is for, or what it is used by)"},
		{"label too long", TokenGrant{UserID: 5, Label: long}, "label is at most 200 characters"},
		{"lifetime zero", TokenGrant{UserID: 5, Label: "x", LifetimeDays: i64(0)}, "lifetime_days must be a positive number of days, at most 365"},
		{"lifetime over a year", TokenGrant{UserID: 5, Label: "x", LifetimeDays: i64(366)}, "lifetime_days must be a positive number of days, at most 365"},
		{"hourly zero", TokenGrant{UserID: 5, Label: "x", RateLimitPerHour: i64(0)}, "rate_limit_per_hour must be a positive number, at most 2147483647"},
		{"daily over int4", TokenGrant{UserID: 5, Label: "x", RateLimitPerDay: i64(MaxAPITokenRateLimit + 1)}, "rate_limit_per_day must be a positive number, at most 2147483647"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := adminStore(t) // createAPIToken nil: reaching the store fails the test
			st.getAPITokenSettings = func(context.Context) (db.APITokenSettings, error) {
				return db.APITokenSettings{DefaultRateLimitPerHour: 1, DefaultRateLimitPerDay: 1, DefaultLifetimeDays: 1}, nil
			}
			svc := New(st, Options{}, nil, discardLogger())
			_, _, err := svc.GrantAPIToken(context.Background(), adminActor(), c.grant)
			ref, ok := Classify(err)
			if !ok || ref.Kind != InvalidInput || ref.Detail != c.detail {
				t.Errorf("got %+v (%v); want InvalidInput %q", ref, err, c.detail)
			}
		})
	}
	// A non-administrator is refused before any read.
	st := &fakeStore{t: t, isUserAdmin: func(context.Context, int) (bool, error) { return false, nil }}
	_, _, err := New(st, Options{}, nil, discardLogger()).GrantAPIToken(context.Background(), Actor{UserID: 2, Kind: Session}, TokenGrant{UserID: 5, Label: "x"})
	if !errors.Is(err, ErrNotAdmin) {
		t.Errorf("non-admin grant = %v; want ErrNotAdmin", err)
	}
	// A token for an account that does not exist: the store's sentinel
	// reaches the adapter (classified UserNotFound).
	st = adminStore(t)
	st.getAPITokenSettings = func(context.Context) (db.APITokenSettings, error) {
		return db.APITokenSettings{DefaultRateLimitPerHour: 1, DefaultRateLimitPerDay: 1, DefaultLifetimeDays: 1}, nil
	}
	st.createAPIToken = func(context.Context, db.APITokenGrant) (string, db.APIToken, error) {
		return "", db.APIToken{}, db.ErrUserNotFound
	}
	_, _, err = New(st, Options{}, nil, discardLogger()).GrantAPIToken(context.Background(), adminActor(), TokenGrant{UserID: 5, Label: "x"})
	if !errors.Is(err, db.ErrUserNotFound) {
		t.Errorf("unknown account = %v; want db.ErrUserNotFound", err)
	}
}

func TestRevokeAPITokenRunsTheHookAndRefusesABadID(t *testing.T) {
	st := adminStore(t)
	svc := New(st, Options{}, nil, discardLogger())
	err := svc.RevokeAPIToken(context.Background(), adminActor(), 0)
	if ref, ok := Classify(err); !ok || ref.Kind != InvalidInput || ref.Detail != "token id must be a positive integer" {
		t.Errorf("revoke 0 = %+v (%v)", ref, err)
	}
	st.revokeAPIToken = func(context.Context, int64, int) error { return db.ErrAPITokenNotFound }
	err = svc.RevokeAPIToken(context.Background(), adminActor(), 7)
	if ref, ok := Classify(err); !ok || ref.Kind != NotFound || ref.Detail != "no API token with that id" {
		t.Errorf("revoke unknown = %+v (%v)", ref, err)
	}
	hooks := newRecHooks()
	var by int
	st.revokeAPIToken = func(_ context.Context, id int64, revokedBy int) error { by = revokedBy; return nil }
	if err := New(st, Options{}, hooks, discardLogger()).RevokeAPIToken(context.Background(), adminActor(), 7); err != nil {
		t.Fatal(err)
	}
	if by != 1 {
		t.Errorf("revoked_by = %d; want the actor", by)
	}
	if got := hooks.got(); len(got) != 1 || got[0] != "tokens" {
		t.Errorf("hooks after a revoke = %v; want [tokens] (the cached validations must drop at once)", got)
	}
}

func TestSetAPITokenDefaultsValidatesAndReturnsTheSavedValues(t *testing.T) {
	st := adminStore(t)
	svc := New(st, Options{}, nil, discardLogger())
	cases := []struct {
		name   string
		d      TokenDefaults
		detail string
	}{
		{"hourly missing", TokenDefaults{LifetimeDays: i64(30)}, "default_rate_limit_per_hour must be a positive number, at most 2147483647"},
		{"lifetime missing", TokenDefaults{RateLimitPerHour: i64(10)}, "default_lifetime_days must be a positive number of days, at most 365"},
		{"daily zero", TokenDefaults{RateLimitPerHour: i64(10), LifetimeDays: i64(30), RateLimitPerDay: i64(0)}, "default_rate_limit_per_day must be a positive number, at most 2147483647"},
	}
	for _, c := range cases {
		_, err := svc.SetAPITokenDefaults(context.Background(), adminActor(), c.d)
		if ref, ok := Classify(err); !ok || ref.Kind != InvalidInput || ref.Detail != c.detail {
			t.Errorf("%s: %+v (%v); want InvalidInput %q", c.name, ref, err, c.detail)
		}
	}
	// A quota aveloxis.json decides cannot be saved here: the store's
	// refusal is a conflict naming the file.
	st.setAPITokenSettings = func(context.Context, db.APITokenSettings, int) error { return db.ErrInvalidCapacitySettings }
	_, err := svc.SetAPITokenDefaults(context.Background(), adminActor(), TokenDefaults{RateLimitPerHour: i64(10), LifetimeDays: i64(30)})
	if ref, ok := Classify(err); !ok || ref.Kind != Conflict || ref.Detail != db.ErrInvalidCapacitySettings.Error() {
		t.Errorf("configured quota = %+v (%v); want Conflict", ref, err)
	}
	var saved db.APITokenSettings
	st.setAPITokenSettings = func(_ context.Context, s db.APITokenSettings, by int) error {
		if by != 1 {
			t.Errorf("updated_by = %d; want the actor", by)
		}
		saved = s
		return nil
	}
	st.getAPITokenSettings = func(context.Context) (db.APITokenSettings, error) { return saved, nil }
	got, err := svc.SetAPITokenDefaults(context.Background(), adminActor(), TokenDefaults{RateLimitPerHour: i64(10), LifetimeDays: i64(30)})
	if err != nil || got.DefaultRateLimitPerHour != 10 || got.DefaultLifetimeDays != 30 || got.DefaultRateLimitPerDay != 0 {
		t.Errorf("saved = %+v, %v; want hour 10, lifetime 30, daily unchanged (0)", got, err)
	}
}
