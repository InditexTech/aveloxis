// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// joinedLiteral is the path a filepath.Join / path.Join call of string
// literals builds, with "/" between the segments; ok is false for any
// other call or a non-literal segment.
func joinedLiteral(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" || len(call.Args) == 0 {
		return "", false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || (pkg.Name != "filepath" && pkg.Name != "path") {
		return "", false
	}
	segs := make([]string, 0, len(call.Args))
	for i, a := range call.Args {
		lit, ok := a.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			if i == 0 && len(call.Args) > 1 {
				// filepath.Join(root, "internal", "web", "server.go"): the
				// head is the module root in every test that reads this way
				// (srctest.Root, a root variable); the trailing literals name
				// the file from the root (review round 1 of 0.29.106: the
				// root-headed shape was the one the scan still missed).
				continue
			}
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return "", false
		}
		segs = append(segs, s)
	}
	if len(segs) == 0 {
		return "", false
	}
	return path.Join(segs...), true
}

// TestSourceFileReadsOnlyShrink — N26 (summary/59 §7b, stage 0.5 P-1): a
// source-contract test that finds its subject by reading a production
// .go file BY NAME breaks (or, worse, passes vacuously on a negative
// needle) when that file is split. The strangler rule is that each split
// migrates the pins it breaks onto srctest.PackageFunc / PackageSource;
// this baseline makes sure no NEW by-name read appears meanwhile. Every
// string literal in a test file that names a non-test .go file is a read
// site, keyed by the test file and the file it names (resolved: a bare
// name is package-local, a path is repo-relative, and a filepath.Join or
// path.Join of literal segments is the path it builds). The baseline only
// shrinks: a new site fails, and a site that is gone must leave the list.
func TestSourceFileReadsOnlyShrink(t *testing.T) {
	root := srctest.Root(t)
	baselinePath := filepath.Join(root, "scripts", "source_file_reads_baseline.txt")
	found := map[string]bool{}
	examined := 0
	for _, dir := range []string{"internal", "cmd", "scripts"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, "_test.go") {
				return err
			}
			rel := filepath.ToSlash(must(filepath.Rel(root, p)))
			if strings.Contains(rel, "/testdata/") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
			if err != nil {
				return err
			}
			examined++
			ast.Inspect(f, func(n ast.Node) bool {
				var v string
				switch n := n.(type) {
				case *ast.CallExpr:
					// A read assembled from segments — filepath.Join("..",
					// "collector", "staged.go") — names its file as surely as
					// one literal does (whole-branch review of PR #238, I1:
					// the bare "staged.go" segment resolved package-local and
					// the site was invisible).
					joined, ok := joinedLiteral(n)
					if !ok {
						return true
					}
					v = joined
				case *ast.BasicLit:
					if n.Kind != token.STRING {
						return true
					}
					s, err := strconv.Unquote(n.Value)
					if err != nil {
						return true
					}
					v = s
				default:
					return true
				}
				if !strings.HasSuffix(v, ".go") || strings.HasSuffix(v, "_test.go") || strings.ContainsAny(v, " \n%*") {
					return true
				}
				var target string
				if !strings.Contains(v, "/") {
					target = path.Join(path.Dir(rel), v)
				} else {
					target = path.Clean(strings.TrimPrefix(path.Join(path.Dir(rel), v), "./"))
					if !strings.HasPrefix(v, ".") {
						target = path.Clean(v)
					}
				}
				// A literal that names no file on disk is a fixture or a
				// message, not a read (review A3: 159 of the first baseline's
				// entries were such noise).
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(target))); err != nil {
					return true
				}
				found[rel+" "+target] = true
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	srctest.MinCount(t, "test files examined", examined, 300)
	var sites []string
	for k := range found {
		sites = append(sites, k)
	}
	sort.Strings(sites)
	if os.Getenv("SOURCE_FILE_READS_WRITE_BASELINE") != "" {
		if err := os.WriteFile(baselinePath, []byte("# N26: test files that read a production .go file by name — \"<test file> <source file>\". Shrink-only.\n"+strings.Join(sites, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	baseline := map[string]bool{}
	for _, line := range strings.Split(srctest.Read(t, "scripts/source_file_reads_baseline.txt"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		baseline[line] = true
	}
	srctest.MinCount(t, "baseline entries", len(baseline), 100)
	for _, s := range sites {
		if !baseline[s] {
			t.Errorf("new by-name read of a production source file: %s — find the subject with srctest.PackageFunc (one function) or srctest.PackageSource (a negative needle) instead; the baseline only shrinks", s)
		}
	}
	for b := range baseline {
		if !found[b] {
			t.Errorf("baseline entry no longer matches a read site — remove it (the list only shrinks): %s", b)
		}
	}
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}
