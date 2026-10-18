// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import "strings"

// The one LIKE-pattern builder (stage 1b item 9, SR-17): every search that
// matches a caller's term as a substring builds its pattern here and
// compares with ESCAPE '\', so % and _ in the term match themselves.
// Before 1b the repository, owner and queue searches concatenated the raw
// term into the pattern, so `a_b` also matched `axb`.

// likeLiteral escapes LIKE's metacharacters (backslash first, then % and
// _) for a pattern whose ESCAPE character is backslash.
func likeLiteral(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// LikeContains is the pattern matching any value that contains term, for a
// `LIKE $n ESCAPE '\'` (or ILIKE) predicate.
func LikeContains(term string) string { return "%" + likeLiteral(term) + "%" }

// repoHostSQL is the host of a repository's URL (`github.com`,
// `gitlab.example.org`), the one derivation the owner search reports and
// the org expansion compares (stage 1b: the expansion put the caller's
// host inside an ILIKE pattern).
const repoHostSQL = `split_part(split_part(repo_git, '://', 2), '/', 1)`
