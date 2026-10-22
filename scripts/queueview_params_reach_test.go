// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

import (
	"regexp"
	"slices"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// The OpenAPI reach cannot follow a call into another package, so
// externalQueryReaders says what queueview.ParseParams reads. This keeps
// the table and the function in step (round-1 review of 0.29.109, F4): the
// query names ParseParams reads with a literal are exactly the table's.
func TestExternalQueryReadersMatchParseParams(t *testing.T) {
	body := srctest.StripGoComments(srctest.PackageFunc(t, "internal/queueview", "func ParseParams("))
	read := regexp.MustCompile(`q\.Get\("([a-z_]+)"\)`)
	var got []string
	for _, m := range read.FindAllStringSubmatch(body, -1) {
		if !slices.Contains(got, m[1]) {
			got = append(got, m[1])
		}
	}
	srctest.MinCount(t, "query names ParseParams reads", len(got), 4)
	want := slices.Clone(externalQueryReaders["queueview.ParseParams"])
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("queueview.ParseParams reads %v but externalQueryReaders says %v; keep them equal so the reach sees every parameter", got, want)
	}
}
