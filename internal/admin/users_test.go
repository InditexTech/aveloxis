// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/mailer"
)

func skipErr() error { return mailer.ErrRecipientSkipped }

func TestSetUserAdmin(t *testing.T) {
	t.Run("self-demotion is refused before the store", func(t *testing.T) {
		svc, hooks, logs := testService(t, &fakeStore{isUserAdmin: adminIs(true)}, Options{})
		err := svc.SetUserAdmin(context.Background(), adminSession, adminSession.UserID, false)
		if ref, ok := Classify(err); !errors.Is(err, ErrSelfDemotion) || !ok || ref.Kind != SelfDemotion {
			t.Fatalf("= %v (%+v)", err, ref)
		}
		if len(hooks.got()) != 0 || !strings.Contains(logs.String(), "self-demotion refused") {
			t.Errorf("hooks %v, log:\n%s", hooks.got(), logs.String())
		}
	})
	t.Run("promoting yourself is not a demotion", func(t *testing.T) {
		st := &fakeStore{isUserAdmin: adminIs(true), setUserAdmin: func(context.Context, int, bool) error { return nil }}
		svc, _, _ := testService(t, st, Options{})
		if err := svc.SetUserAdmin(context.Background(), adminSession, adminSession.UserID, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the operator may demote the account it acts for", func(t *testing.T) {
		st := &fakeStore{setUserAdmin: func(context.Context, int, bool) error { return nil }}
		svc, _, _ := testService(t, st, Options{})
		if err := svc.SetUserAdmin(context.Background(), Actor{UserID: 3, Kind: Operator}, 3, false); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("written: hook and audit", func(t *testing.T) {
		var got []any
		st := &fakeStore{isUserAdmin: adminIs(true), setUserAdmin: func(_ context.Context, id int, admin bool) error {
			got = []any{id, admin}
			return nil
		}}
		svc, hooks, logs := testService(t, st, Options{})
		if err := svc.SetUserAdmin(context.Background(), adminSession, 4, true); err != nil || !slices.Equal(got, []any{4, true}) {
			t.Fatalf("= %v, store got %v", err, got)
		}
		if h := hooks.got(); !slices.Equal(h, []string{"role 4"}) {
			t.Errorf("hooks = %v", h)
		}
		if !strings.Contains(logs.String(), "admin: set user admin") || !strings.Contains(logs.String(), "target_user_id=4") {
			t.Errorf("audit:\n%s", logs.String())
		}
	})
	t.Run("the store's refusals pass through, no hook", func(t *testing.T) {
		for _, tc := range []struct {
			err  error
			kind Kind
		}{{db.ErrLastAdmin, LastAdmin}, {db.ErrUserNotFound, UserNotFound}} {
			st := &fakeStore{isUserAdmin: adminIs(true), setUserAdmin: func(context.Context, int, bool) error { return tc.err }}
			svc, hooks, logs := testService(t, st, Options{})
			err := svc.SetUserAdmin(context.Background(), adminSession, 4, false)
			if ref, ok := Classify(err); !errors.Is(err, tc.err) || !ok || ref.Kind != tc.kind {
				t.Errorf("%v: = %v (%+v)", tc.err, err, ref)
			}
			if len(hooks.got()) != 0 || !strings.Contains(logs.String(), "refused") {
				t.Errorf("%v: hooks %v, log:\n%s", tc.err, hooks.got(), logs.String())
			}
		}
	})
	t.Run("a store failure is wrapped", func(t *testing.T) {
		st := &fakeStore{isUserAdmin: adminIs(true), setUserAdmin: func(context.Context, int, bool) error { return errors.New("closed pool") }}
		svc, hooks, _ := testService(t, st, Options{})
		err := svc.SetUserAdmin(context.Background(), adminSession, 4, false)
		if _, ok := Classify(err); err == nil || ok || !strings.Contains(err.Error(), "4=false") || len(hooks.got()) != 0 {
			t.Errorf("= %v, hooks %v", err, hooks.got())
		}
	})
}
