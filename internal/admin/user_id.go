// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"math"
	"strconv"
)

// MaxUserID is the largest users.user_id the column holds (SERIAL, int4).
// A request naming an account by a larger number used to reach pgx's
// parameter encoder as a 500 (58 §7 item 19; capacity round 6 R6-d).
const MaxUserID = math.MaxInt32

// ParseUserID is the one rule for a users.user_id a request carries as
// text (a path value, a query value): a positive integer no
// larger than MaxUserID, or an InvalidInput refusal the adapters answer
// as 400 before any store read (0.29.103 review F2). It parses at int's
// own width, so no caller narrows a 64-bit value (CodeQL
// go/incorrect-integer-conversion, PR #238).
func ParseUserID(raw string) (int, error) {
	id, err := strconv.Atoi(raw)
	if err != nil || !validUserID(id) {
		return 0, refuseInput("user id must be a positive integer")
	}
	return id, nil
}

// validUserID is the range ParseUserID accepts, for a value that arrived
// already numeric (a JSON body, or an adapter that parsed on its own): the
// service refuses the rest at the deciding layer (SR-18).
func validUserID(id int) bool { return id > 0 && id <= MaxUserID }
