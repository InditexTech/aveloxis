// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"context"
	"errors"
	"testing"
)

// TestParseUserID (0.29.103 review F2): one parse rule for users.user_id
// (SERIAL, int4) wherever a request names an account by number — a
// positive integer the column holds, or ErrInvalidInput. A larger value
// reached the store and was a 500 (58 §7 item 19; capacity round 6 R6-d).
func TestParseUserID(t *testing.T) {
	// "+1" is 1: strconv's sign rule, the same every id parse in the
	// repository has always applied (pathID's ParseInt, the API's Atoi).
	for raw, want := range map[string]int{"1": 1, "+1": 1, "2147483647": MaxUserID, " 7": 0, "0": 0, "-1": 0, "x": 0, "": 0,
		"2147483648": 0, "4294967296": 0, "99999999999999999999": 0, "1.0": 0, "0x10": 0} {
		id, err := ParseUserID(raw)
		if want == 0 {
			if !errors.Is(err, ErrInvalidInput) || id != 0 {
				t.Errorf("ParseUserID(%q) = %d, %v; want 0, ErrInvalidInput", raw, id, err)
			}
			if ref, ok := Classify(err); !ok || ref.Kind != InvalidInput || ref.Detail == "" {
				t.Errorf("ParseUserID(%q) classifies as %+v (%v); want an InvalidInput refusal with its words", raw, ref, ok)
			}
		} else if err != nil || id != want {
			t.Errorf("ParseUserID(%q) = %d, %v; want %d", raw, id, err, want)
		}
	}
}

// TestSetUserAdminRefusesAnIDTheColumnCannotHold: the deciding layer
// (SR-18) refuses a target outside int4 before any store read, so an
// adapter that parsed the number at a wider width still cannot reach the
// store with it (the fake fails the test on any store call).
func TestSetUserAdminRefusesAnIDTheColumnCannotHold(t *testing.T) {
	for _, target := range []int{0, -1, MaxUserID + 1} {
		svc, hooks, _ := testService(t, &fakeStore{}, Options{})
		err := svc.SetUserAdmin(context.Background(), adminSession, target, true)
		if ref, ok := Classify(err); !errors.Is(err, ErrInvalidInput) || !ok || ref.Kind != InvalidInput {
			t.Errorf("target %d: = %v (%+v); want an InvalidInput refusal", target, err, ref)
		}
		if len(hooks.got()) != 0 {
			t.Errorf("target %d: hooks ran %v", target, hooks.got())
		}
	}
}
