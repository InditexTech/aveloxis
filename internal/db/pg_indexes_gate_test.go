// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestNoIndexGateReadsPgIndexes (H-2): pg_indexes lists an INVALID index —
// the leftover of an interrupted CREATE INDEX CONCURRENTLY — as present, so
// a gate that asks it skips rebuilding a broken index forever. Index gates
// ask validIndexExists (pg_index.indisvalid). Production Go code must not
// consult pg_indexes at all.
func TestNoIndexGateReadsPgIndexes(t *testing.T) {
	root := srctest.Root(t)
	examined := 0
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			examined++
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			if strings.Contains(strings.ToLower(srctest.StripGoComments(string(b))), "pg_indexes") {
				rel, _ := filepath.Rel(root, p)
				t.Errorf("%s reads pg_indexes; an INVALID index is listed there as present — use validIndexExists", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	srctest.MinCount(t, "production Go files examined", examined, 100)
}
