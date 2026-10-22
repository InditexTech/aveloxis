// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/db"
)

// Stage 3b (0.29.109): the capacity rules the REST handlers held — the
// quota's name, mode and source, the contact address, an account's
// override, the sign-up allowlist — are the service's (SR-18).

func TestSetCapacityQuotaRefusesWhatTheHandlerRefused(t *testing.T) {
	known := capacity.QuotaNames()[0]
	st := adminStore(t)
	svc := New(st, Options{}, nil, discardLogger())
	_, err := svc.SetCapacityQuota(context.Background(), adminActor(), "nope", QuotaChange{Allowed: i64(5), Mode: "enforce"})
	if ref, ok := Classify(err); !ok || ref.Kind != NotFound || ref.Detail != `no quota named "nope"` {
		t.Errorf("unknown quota = %+v (%v)", ref, err)
	}
	for _, c := range []QuotaChange{{Allowed: nil, Mode: "enforce"}, {Allowed: i64(0), Mode: "enforce"}, {Allowed: i64(5), Mode: "loud"}} {
		_, err := svc.SetCapacityQuota(context.Background(), adminActor(), known, c)
		if ref, ok := Classify(err); !ok || ref.Kind != InvalidInput || ref.Detail != "allowed must be a positive number, at most 2147483647, and mode one of enforce, shadow, off" {
			t.Errorf("%+v: %+v (%v)", c, ref, err)
		}
	}
	st.capacitySource = func(string) capacity.Source { return capacity.SourceDefault }
	_, err = svc.SetCapacityQuota(context.Background(), adminActor(), known, QuotaChange{Allowed: i64(5), Mode: "enforce"})
	if ref, ok := Classify(err); !ok || ref.Kind != Conflict || ref.Detail != known+" is set in aveloxis.json (DEFAULT); change it there" {
		t.Errorf("configured quota = %+v (%v); want Conflict naming the file", ref, err)
	}
	st.capacitySource = nil
	var gotAllowed, gotBy int
	var gotMode capacity.Mode
	st.setCapacityQuota = func(_ context.Context, name string, allowed int, mode capacity.Mode, by int) error {
		gotAllowed, gotMode, gotBy = allowed, mode, by
		return nil
	}
	mode, err := svc.SetCapacityQuota(context.Background(), adminActor(), known, QuotaChange{Allowed: i64(5), Mode: "shadow"})
	if err != nil || mode != capacity.Shadow || gotAllowed != 5 || gotMode != capacity.Shadow || gotBy != 1 {
		t.Errorf("set = %v, %v; store got allowed=%d mode=%v by=%d", mode, err, gotAllowed, gotMode, gotBy)
	}
}

// The store re-validates; its refusal is answered as one, never wrapped
// with the actor (round-1 F3; this test is its red half, round-2 A-6).
func TestSetCapacityQuotaPassesTheStoresRefusalThrough(t *testing.T) {
	st := adminStore(t)
	st.setCapacityQuota = func(context.Context, string, int, capacity.Mode, int) error { return db.ErrInvalidCapacitySettings }
	_, err := New(st, Options{}, nil, discardLogger()).SetCapacityQuota(context.Background(), adminActor(), capacity.QuotaNames()[0], QuotaChange{Allowed: i64(5), Mode: "enforce"})
	if ref, ok := Classify(err); !ok || ref.Kind != InvalidInput || !errors.Is(err, db.ErrInvalidCapacitySettings) || strings.Contains(err.Error(), " by ") {
		t.Errorf("the store's refusal = %+v (%v); want it classified and unwrapped", ref, err)
	}
}

func TestSetCapacityContactNamesTheRule(t *testing.T) {
	st := adminStore(t)
	st.setCapacityContact = func(context.Context, string, int) error { return db.ErrInvalidCapacitySettings }
	err := New(st, Options{}, nil, discardLogger()).SetCapacityContact(context.Background(), adminActor(), "not an address")
	if ref, ok := Classify(err); !ok || ref.Kind != InvalidInput || !strings.HasPrefix(ref.Detail, "contact_email must be an email address of at most 254 bytes") {
		t.Errorf("bad contact = %+v (%v)", ref, err)
	}
}

func TestSetCapacityOverrideBoundsAndSentinels(t *testing.T) {
	st := adminStore(t)
	svc := New(st, Options{}, nil, discardLogger())
	err := svc.SetCapacityOverride(context.Background(), adminActor(), db.CapacityOverride{UserID: 0})
	if ref, ok := Classify(err); !ok || ref.Kind != InvalidInput || ref.Detail != "user id must be a positive integer" {
		t.Errorf("user 0 = %+v (%v)", ref, err)
	}
	st.setCapacityOverride = func(context.Context, db.CapacityOverride, int) error { return db.ErrInvalidCapacitySettings }
	err = svc.SetCapacityOverride(context.Background(), adminActor(), db.CapacityOverride{UserID: 5})
	if ref, ok := Classify(err); !ok || ref.Kind != InvalidInput || !strings.HasPrefix(ref.Detail, "each value must be a positive number") {
		t.Errorf("bad values = %+v (%v)", ref, err)
	}
	st.setCapacityOverride = func(context.Context, db.CapacityOverride, int) error { return db.ErrUserNotFound }
	if err := svc.SetCapacityOverride(context.Background(), adminActor(), db.CapacityOverride{UserID: 5}); !errors.Is(err, db.ErrUserNotFound) {
		t.Errorf("unknown account = %v; want db.ErrUserNotFound", err)
	}
}

func TestSignupAllowlistRefusals(t *testing.T) {
	st := adminStore(t)
	svc := New(st, Options{}, nil, discardLogger())
	st.addSignupAllowlist = func(context.Context, string, string, int) (string, error) { return "", db.ErrInvalidCIDR }
	_, err := svc.AddSignupAllowlist(context.Background(), adminActor(), "nope", "")
	if ref, ok := Classify(err); !ok || ref.Kind != InvalidInput || !strings.HasPrefix(ref.Detail, "cidr must be a network in CIDR form (such as 192.0.2.0/24)") {
		t.Errorf("bad cidr on add = %+v (%v)", ref, err)
	}
	// Remove normalises before the store (round-3 A-2/B-7; this is its red
	// half, round-4): a non-network never reaches the store — the fake would
	// accept anything — and the store and the audit line get the masked
	// network, not the caller's spelling.
	var stores []string
	st.removeSignupAllowlist = func(_ context.Context, cidr string) (bool, error) { stores = append(stores, cidr); return true, nil }
	svc, _, logs := testService(t, st, Options{})
	_, err = svc.RemoveSignupAllowlist(context.Background(), adminActor(), "nope")
	if ref, ok := Classify(err); !ok || ref.Kind != InvalidInput || ref.Detail != "cidr must be a network in CIDR form" || len(stores) != 0 {
		t.Errorf("bad cidr on remove = %+v (%v), store calls %v; want the refusal with no store round trip", ref, err, stores)
	}
	if removed, err := svc.RemoveSignupAllowlist(context.Background(), adminActor(), " 192.0.2.7/24 "); !removed || err != nil || len(stores) != 1 || stores[0] != "192.0.2.0/24" {
		t.Errorf("remove = %v, %v; store got %v; want the masked network 192.0.2.0/24", removed, err, stores)
	}
	if line := logs.String(); !strings.Contains(line, "remove sign-up allowlist entry") || !strings.Contains(line, "cidr=192.0.2.0/24") || strings.Contains(line, "192.0.2.7") {
		t.Errorf("the audit line must name the network removed, never the caller's spelling:\n%s", line)
	}
	st.removeSignupAllowlist = func(context.Context, string) (bool, error) { return false, nil }
	if removed, err := svc.RemoveSignupAllowlist(context.Background(), adminActor(), "192.0.2.0/24"); removed || err != nil {
		t.Errorf("remove of an absent network = %v, %v; want false, nil (the adapter answers 404)", removed, err)
	}
}
