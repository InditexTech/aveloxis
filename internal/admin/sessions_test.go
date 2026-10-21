// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/db"
)

// 0.29.113 (ASVS V7.5.2, V7.4.5): an account lists and ends its own
// sessions; an administrator ends any account's; an API token does neither.
func TestSessionsAreTheAccountsOrAnAdministrators(t *testing.T) {
	ctx := context.Background()
	var ended []int
	st := &fakeStore{
		isUserAdmin:        func(_ context.Context, id int) (bool, error) { return id == 1, nil },
		listUserSessions:   func(_ context.Context, id int) ([]db.UserSession, error) { return []db.UserSession{{Kind: "web"}}, nil },
		deleteUserSessions: func(_ context.Context, id int) (int64, error) { ended = append(ended, id); return 2, nil },
	}
	svc, _, logs := testService(t, st, Options{})
	me := Actor{UserID: 2, Kind: Session}
	if list, err := svc.ListSessions(ctx, me, 2); err != nil || len(list) != 1 {
		t.Errorf("own list = %v, %v", list, err)
	}
	if n, err := svc.RevokeSessions(ctx, me, 2); err != nil || n != 2 {
		t.Errorf("own revoke = %d, %v", n, err)
	}
	if _, err := svc.ListSessions(ctx, me, 3); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("another account's list by a non-admin = %v; want ErrNotAdmin", err)
	}
	if _, err := svc.RevokeSessions(ctx, me, 3); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("another account's revoke by a non-admin = %v; want ErrNotAdmin", err)
	}
	if n, err := svc.RevokeSessions(ctx, adminActor(), 3); err != nil || n != 2 {
		t.Errorf("an administrator's revoke = %d, %v", n, err)
	}
	if !strings.Contains(logs.String(), "admin: end sessions") || !strings.Contains(logs.String(), "target_user_id=3") || !strings.Contains(logs.String(), "sessions=2") {
		t.Errorf("the audit line names the target and the count:\n%s", logs.String())
	}
	// An API token is not a session and never its owner's keeper: refused
	// like every administrative call, even for its own account.
	tok := Actor{UserID: 2, Kind: APIToken}
	if _, err := svc.RevokeSessions(ctx, tok, 2); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("a token's revoke of its owner's sessions = %v; want ErrNotAdmin", err)
	}
	if _, err := svc.ListSessions(ctx, tok, 2); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("a token's list = %v; want ErrNotAdmin", err)
	}
	if _, err := svc.RevokeSessions(ctx, adminActor(), 0); !isInput(err, "user id must be a positive integer") {
		t.Errorf("revoke of user 0 = %v", err)
	}
	if len(ended) != 2 || ended[0] != 2 || ended[1] != 3 {
		t.Errorf("the store ended %v; want [2 3]", ended)
	}
}
