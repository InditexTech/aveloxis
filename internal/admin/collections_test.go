// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/db"
)

// Stage 3b (0.29.109): the collection administration the REST handlers
// held is the service's (SR-18); the console's collections page (3c) is
// the first caller of the rename/reorder operation outside the API.

func TestCollectionOperationsRefuseAndRecord(t *testing.T) {
	st := adminStore(t)
	svc := New(st, Options{}, nil, discardLogger())
	ctx := context.Background()
	a := adminActor()

	if _, err := svc.CreateCollection(ctx, a, CollectionFields{Name: " "}); !isInput(err, "name required") {
		t.Errorf("create without a name = %v", err)
	}
	if _, err := svc.CreateCollection(ctx, a, CollectionFields{Name: strings.Repeat("n", MaxGroupNameLength+1)}); !isInput(err, "name is at most 200 characters") {
		t.Errorf("over-long name = %v", err)
	}
	if _, err := svc.CreateCollection(ctx, a, CollectionFields{Name: "x", Description: strings.Repeat("d", MaxCollectionDescriptionLength+1)}); !isInput(err, "description is at most 1000 characters") {
		t.Errorf("over-long description = %v", err)
	}
	for _, pos := range []int{math.MaxInt32 + 1, math.MinInt32 - 1} {
		if _, err := svc.CreateCollection(ctx, a, CollectionFields{Name: "x", Position: pos}); !isInput(err, "position must be between -2147483648 and 2147483647") {
			t.Errorf("position %d = %v; want the int4 rule (the encoder's refusal was a 500)", pos, err)
		}
	}
	st.createCollection = func(context.Context, string, string, int, int) (int64, error) { return 0, db.ErrCollectionNameTaken }
	if _, err := svc.CreateCollection(ctx, a, CollectionFields{Name: "Taken"}); !isKind(err, Conflict, "a collection with that name already exists") {
		t.Errorf("duplicate name = %v", err)
	}
	var createdBy int
	st.createCollection = func(_ context.Context, name, desc string, pos int, by int) (int64, error) {
		createdBy = by
		return 42, nil
	}
	if id, err := svc.CreateCollection(ctx, a, CollectionFields{Name: "Science", Description: "d", Position: 3}); err != nil || id != 42 || createdBy != 1 {
		t.Errorf("create = %d, %v (created_by %d)", id, err, createdBy)
	}

	// An id that names nothing is NotFound in the store's words — what the
	// API answered before the move (round-2 review of 0.29.109).
	for _, id := range []int64{0, -1} {
		if err := svc.UpdateCollection(ctx, a, id, CollectionFields{Name: "x"}); !isKind(err, NotFound, "no collection with that id") {
			t.Errorf("update id %d = %v", id, err)
		}
		if err := svc.DeleteCollection(ctx, a, id); !isKind(err, NotFound, "no collection with that id") {
			t.Errorf("delete id %d = %v", id, err)
		}
		if err := svc.AddGroupToCollection(ctx, a, id, 7); !isKind(err, NotFound, "no collection with that id") {
			t.Errorf("add to id %d = %v", id, err)
		}
		if err := svc.RemoveGroupFromCollection(ctx, a, id, 7); !isKind(err, NotFound, "no collection with that id") {
			t.Errorf("remove from id %d = %v", id, err)
		}
	}
	if err := svc.UpdateCollection(ctx, a, 42, CollectionFields{Name: ""}); !isInput(err, "name required") {
		t.Errorf("update without a name = %v", err)
	}
	st.updateCollection = func(context.Context, int64, string, string, int) error { return db.ErrCollectionNotFound }
	if err := svc.UpdateCollection(ctx, a, 42, CollectionFields{Name: "x"}); !isKind(err, NotFound, "no collection with that id") {
		t.Errorf("update unknown = %v", err)
	}

	if err := svc.AddGroupToCollection(ctx, a, 42, 0); !isInput(err, "group_id required") {
		t.Errorf("add group 0 = %v", err)
	}
	st.addGroupToCollection = func(context.Context, int64, int64) error { return db.ErrGroupNotAdminOwned }
	if err := svc.AddGroupToCollection(ctx, a, 42, 7); !isInput(err, "collection member groups must be admin-owned") {
		t.Errorf("add a user's group = %v", err)
	}
	st.addGroupToCollection = func(context.Context, int64, int64) error { return db.ErrGroupNotFound }
	if err := svc.AddGroupToCollection(ctx, a, 42, 7); !isKind(err, NotFound, "") {
		t.Errorf("add unknown group = %v", err)
	}

	st.deleteCollection = func(context.Context, int64) error { return nil }
	if err := svc.DeleteCollection(ctx, a, 42); err != nil {
		t.Errorf("delete = %v", err)
	}
	st.removeGroupFromCollection = func(context.Context, int64, int64) error { return nil }
	if err := svc.RemoveGroupFromCollection(ctx, a, 42, 7); err != nil {
		t.Errorf("remove group = %v", err)
	}

	// Every operation is an administrator's.
	user := &fakeStore{t: t, isUserAdmin: func(context.Context, int) (bool, error) { return false, nil }}
	usvc := New(user, Options{}, nil, discardLogger())
	b := Actor{UserID: 2, Kind: Session}
	for name, err := range map[string]error{
		"create": func() error { _, e := usvc.CreateCollection(ctx, b, CollectionFields{Name: "x"}); return e }(),
		"update": usvc.UpdateCollection(ctx, b, 42, CollectionFields{Name: "x"}),
		"delete": usvc.DeleteCollection(ctx, b, 42),
		"add":    usvc.AddGroupToCollection(ctx, b, 42, 7),
		"remove": usvc.RemoveGroupFromCollection(ctx, b, 42, 7),
	} {
		if !errors.Is(err, ErrNotAdmin) {
			t.Errorf("%s by a non-admin = %v; want ErrNotAdmin", name, err)
		}
	}
}

func isInput(err error, detail string) bool { return isKind(err, InvalidInput, detail) }

func isKind(err error, kind Kind, detail string) bool {
	ref, ok := Classify(err)
	return ok && ref.Kind == kind && (detail == "" || ref.Detail == detail)
}
