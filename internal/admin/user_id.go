// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
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

// MaxLoginLength is the longest login a forge issues (GitLab's 255; GitHub's
// is 39): a longer value names no account, so a lookup by login refuses it
// before the query and before any page echoes it (round-1 review of
// 0.29.109, web F3).
const MaxLoginLength = 255

// ValidLoginLength is the one length rule for a login a caller looks up:
// non-empty and at most MaxLoginLength characters (runes — a byte count
// splits a multi-byte login short; round-2 review of 0.29.109).
func ValidLoginLength(login string) bool {
	n := utf8.RuneCountInString(login)
	return n > 0 && n <= MaxLoginLength
}

// NormalizeLogin is the one reading of a login a request carries: the
// value trimmed, then the length rule; ok is false for a value that names
// no account (round-3 review of 0.29.109: one adapter trimmed before the
// rule and the other did not, so a padded login was a login in the
// console and a 404 in the API). The store trims again on its own
// contract (its callers include the CLI); the rule here keeps the query
// from running at all.
func NormalizeLogin(raw string) (login string, ok bool) {
	login = strings.TrimSpace(raw)
	return login, ValidLoginLength(login)
}
