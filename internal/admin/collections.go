// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// The collection administration (stage 3b of summary/59, 0.29.109): the
// curated collections of admin-owned groups the public catalogue shows.
// The rules were the REST handlers' (v0.27.70); they are the service's
// now (SR-18). The store's refusals — no such collection, a taken name, a
// group that is not admin-owned, no such group — reach the adapter
// classified.

// CollectionFields is a collection's editable record as an adapter
// decoded it: the name (required, at most MaxGroupNameLength characters —
// one rule for a displayed name), its description (at most
// MaxCollectionDescriptionLength) and its position in the catalogue's
// order.
type CollectionFields struct {
	Name        string
	Description string
	Position    int
}

// MaxCollectionDescriptionLength bounds a collection's description, in
// characters: one paragraph on the public catalogue (round-1 review of
// 0.29.109, web F5: every other console text had a bound; this had none).
const MaxCollectionDescriptionLength = 1000

// validCollectionFields is the one check of a collection's texts.
func validCollectionFields(f CollectionFields) error {
	if strings.TrimSpace(f.Name) == "" {
		return refuseInput("name required")
	}
	if utf8.RuneCountInString(f.Name) > MaxGroupNameLength {
		return refuseInput(fmt.Sprintf("name is at most %d characters", MaxGroupNameLength))
	}
	if utf8.RuneCountInString(f.Description) > MaxCollectionDescriptionLength {
		return refuseInput(fmt.Sprintf("description is at most %d characters", MaxCollectionDescriptionLength))
	}
	if f.Position < math.MinInt32 || f.Position > math.MaxInt32 { // the column is int4; the encoder's refusal was a 500 (whole-branch review)
		return refuseInput(fmt.Sprintf("position must be between %d and %d", math.MinInt32, math.MaxInt32))
	}
	return nil
}

// validCollectionID is the id bound every operation on an existing
// collection applies before the store (the adapters parse the path value).
// A zero or negative id names no collection, and is refused as the store
// refused it before the move — NotFound with its words — so the adapters'
// answers stay byte-identical (round-2 review of 0.29.109).
func validCollectionID(id int64) bool { return id > 0 }

// errNoCollection is the refusal for an id that names no collection; the
// same words the classifier gives db.ErrCollectionNotFound.
func errNoCollection() error { return refuseNotFound("no collection with that id") }

// CreateCollection creates one; the caller is recorded as its creator.
func (s *Service) CreateCollection(ctx context.Context, a Actor, f CollectionFields) (int64, error) {
	const op = "create collection"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return 0, err
	}
	if err := validCollectionFields(f); err != nil {
		return 0, err
	}
	id, err := s.st.CreateCollection(ctx, f.Name, f.Description, f.Position, a.UserID)
	if err != nil {
		if _, refused := Classify(err); refused {
			s.refused(op, a, err, "name", f.Name)
			return 0, err
		}
		return 0, fmt.Errorf("%s by %s: %w", op, a, err)
	}
	s.audit(op, a, "collection_id", id, "name", f.Name)
	return id, nil
}

// UpdateCollection renames, describes or reorders one.
func (s *Service) UpdateCollection(ctx context.Context, a Actor, id int64, f CollectionFields) error {
	const op = "update collection"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return err
	}
	if !validCollectionID(id) {
		return errNoCollection()
	}
	if err := validCollectionFields(f); err != nil {
		return err
	}
	if err := s.st.UpdateCollection(ctx, id, f.Name, f.Description, f.Position); err != nil {
		if _, refused := Classify(err); refused {
			s.refused(op, a, err, "collection_id", id)
			return err
		}
		return fmt.Errorf("%s %d by %s: %w", op, id, a, err)
	}
	s.audit(op, a, "collection_id", id, "name", f.Name, "position", f.Position)
	return nil
}

// DeleteCollection removes one with its group links.
func (s *Service) DeleteCollection(ctx context.Context, a Actor, id int64) error {
	const op = "delete collection"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return err
	}
	if !validCollectionID(id) {
		return errNoCollection()
	}
	if err := s.st.DeleteCollection(ctx, id); err != nil {
		if _, refused := Classify(err); refused {
			s.refused(op, a, err, "collection_id", id)
			return err
		}
		return fmt.Errorf("%s %d by %s: %w", op, id, a, err)
	}
	s.audit(op, a, "collection_id", id)
	return nil
}

// AddGroupToCollection links an admin-owned group into a collection.
func (s *Service) AddGroupToCollection(ctx context.Context, a Actor, collectionID, groupID int64) error {
	const op = "add group to collection"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return err
	}
	if !validCollectionID(collectionID) {
		return errNoCollection()
	}
	if groupID == 0 {
		return refuseInput("group_id required")
	}
	if err := s.st.AddGroupToCollection(ctx, collectionID, groupID); err != nil {
		if _, refused := Classify(err); refused {
			s.refused(op, a, err, "collection_id", collectionID, "group_id", groupID)
			return err
		}
		return fmt.Errorf("%s %d/%d by %s: %w", op, collectionID, groupID, a, err)
	}
	s.audit(op, a, "collection_id", collectionID, "group_id", groupID)
	return nil
}

// RemoveGroupFromCollection unlinks a group from a collection.
func (s *Service) RemoveGroupFromCollection(ctx context.Context, a Actor, collectionID, groupID int64) error {
	const op = "remove group from collection"
	if err := s.requireAdmin(ctx, a, op); err != nil {
		return err
	}
	if !validCollectionID(collectionID) {
		return errNoCollection()
	}
	if err := s.st.RemoveGroupFromCollection(ctx, collectionID, groupID); err != nil {
		if _, refused := Classify(err); refused {
			s.refused(op, a, err, "collection_id", collectionID, "group_id", groupID)
			return err
		}
		return fmt.Errorf("%s %d/%d by %s: %w", op, collectionID, groupID, a, err)
	}
	s.audit(op, a, "collection_id", collectionID, "group_id", groupID)
	return nil
}
