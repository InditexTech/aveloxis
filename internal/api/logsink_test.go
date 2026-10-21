// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// lockedBuffer is a log sink safe to read while a logging goroutine writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// bufferType reports whether the type expression is bytes.Buffer or
// strings.Builder (the two bare sinks a test can declare).
func bufferType(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && ((pkg.Name == "bytes" && sel.Sel.Name == "Buffer") || (pkg.Name == "strings" && sel.Sel.Name == "Builder"))
}

// declaresLockedWrite reports whether the file declares a Write method on
// the named type (value or pointer receiver) whose body takes a lock (a
// `.Lock()` call): the shape of a locked sink. A Write that only forwards
// to the buffer is a decoy, not a lock (review round 2 of 0.29.106).
func declaresLockedWrite(f *ast.File, typeName string) bool {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil || fd.Name.Name != "Write" || len(fd.Recv.List) != 1 {
			continue
		}
		rt := fd.Recv.List[0].Type
		if star, ok := rt.(*ast.StarExpr); ok {
			rt = star.X
		}
		if id, ok := rt.(*ast.Ident); !ok || id.Name != typeName {
			continue
		}
		locks := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Lock" {
					locks = true
				}
			}
			return true
		})
		return locks
	}
	return false
}

// bufferMaker reports a call that builds a buffer without spelling its
// type: bytes.NewBuffer, bytes.NewBufferString.
func bufferMaker(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "bytes" && (sel.Sel.Name == "NewBuffer" || sel.Sel.Name == "NewBufferString")
}

// TestServerLogSinksAreLocked (0.29.105, the integration job on PR #238): a
// server a test builds owns goroutines that log past the request that
// started them — the capacity policy's refresh (capacity_limits.go: from
// the rate-limit middleware of any server built with New or NewWithOptions
// over a store, or of a capacityPolicy literal without an inline spawn) and
// the home-repos refresh (portal.go, from a Server wired with a home
// loader). Four tests handed such a server a bare strings.Builder as its
// log sink and reset or read it from the test goroutine: a data race,
// intermittent on CI under -race (slog's handler locks only its own Write;
// a Builder read from another goroutine is unsynchronised).
// The rule is decided on the syntax tree, not on how the pointer reaches
// the handler or on one spelling of the declaration (review rounds 1–3:
// `w := &logs`, `var logs = strings.Builder{}`, `bytes.NewBuffer(nil)`, a
// type alias, a by-value helper and a dot import each slipped past a text
// check): a function that constructs a server mentions the types
// bytes.Buffer and strings.Builder nowhere — body, parameters or results
// — but behind a pointer (a `*bytes.Buffer` is a buffer some other
// function owns), and calls no bytes.NewBuffer; no test file of the
// package declares a type over them, a struct holding one without a Write
// method of its own (a sink reached as &fs.logs from another file), returns
// one by value, or dot-imports bytes or strings. A sink there is a
// lockedBuffer. Residual, by design: a
// logger or server built in one function and handed to another
// (rewarmHarness builds a bare literal whose tests call rewarmOnce
// synchronously; TestRunRewarmOffReturnsAtOnce reads its sink after the
// done channel).
func TestServerLogSinksAreLocked(t *testing.T) {
	root := srctest.Root(t)
	dir := filepath.Join(root, "internal", "api")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, constructing, handlers := 0, 0, 0
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") || !srctest.CompiledTestFile(t, root, path) {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files++
		for _, imp := range f.Imports {
			if imp.Name != nil && imp.Name.Name == "." && (imp.Path.Value == `"bytes"` || imp.Path.Value == `"strings"`) {
				t.Errorf("%s: dot-imports %s; a bare buffer must be spelled with its package so the sink rule can see it", fset.Position(imp.Pos()), imp.Path.Value)
			}
		}
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				for _, sp := range gd.Specs {
					ts, ok := sp.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if bufferType(ts.Type) {
						t.Errorf("%s: type %s over a bare buffer hides the sink rule", fset.Position(ts.Pos()), ts.Name.Name)
					}
					// A struct field of bare buffer type is a sink a
					// constructing function in another file reaches as
					// &fs.logs (review round 1 of 0.29.106). A locked
					// wrapper (a mutex beside the buffer, like lockedBuffer
					// itself) is one whose Write takes the lock.
					if st, ok := ts.Type.(*ast.StructType); ok && !declaresLockedWrite(f, ts.Name.Name) {
						for _, fld := range st.Fields.List {
							if bufferType(fld.Type) {
								t.Errorf("%s: struct %s holds a bare buffer field without a Write that takes a lock; a sink reached as &x.field escapes the function rule — lock it (a Write under a mutex) or use lockedBuffer", fset.Position(fld.Pos()), ts.Name.Name)
							}
						}
					}
				}
			}
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fd.Type.Results != nil {
				for _, r := range fd.Type.Results.List {
					if bufferType(r.Type) {
						t.Errorf("%s: %s returns a bare buffer by value; a helper may hand one to a constructing function unseen", fset.Position(r.Pos()), fd.Name.Name)
					}
				}
			}
			// What constructs a server: a New/NewWithOptions of this package
			// (an identifier that is not the selector of a qualified name —
			// slog.New, errors.New — used as a call or a function value), a
			// Server literal or assignment wiring the home loader, a
			// capacityPolicy literal that leaves spawn to `go f()`.
			selectors := map[*ast.Ident]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					selectors[sel.Sel] = true
				}
				return true
			})
			constructs := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.Ident:
					if (n.Name == "New" || n.Name == "NewWithOptions") && !selectors[n] {
						constructs = true
					}
				case *ast.KeyValueExpr:
					if id, ok := n.Key.(*ast.Ident); ok && id.Name == "homeLoader" {
						constructs = true
					}
				case *ast.AssignStmt:
					for _, l := range n.Lhs {
						if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "homeLoader" {
							constructs = true
						}
					}
				case *ast.CompositeLit:
					if id, ok := n.Type.(*ast.Ident); ok && id.Name == "capacityPolicy" {
						spawn := false
						for _, el := range n.Elts {
							if kv, ok := el.(*ast.KeyValueExpr); ok {
								if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "spawn" {
									spawn = true
								}
							}
						}
						if !spawn {
							constructs = true
						}
					}
				}
				return true
			})
			if !constructs {
				continue
			}
			constructing++
			flag := func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.StarExpr:
					if bufferType(n.X) {
						return false // a pointer to a buffer another function owns
					}
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "NewTextHandler" || sel.Sel.Name == "NewJSONHandler") {
						handlers++
					}
					if bufferMaker(n) {
						t.Errorf("%s: %s constructs a server and builds a bare buffer with bytes.%s; a sink here is a lockedBuffer", fset.Position(n.Pos()), fd.Name.Name, n.Fun.(*ast.SelectorExpr).Sel.Name)
					}
				case *ast.SelectorExpr:
					if bufferType(n) {
						t.Errorf("%s: %s constructs a server and declares a bare %s.%s; the server's goroutines (the capacity refresh, the home-repos refresh) log past the request — a sink here is a lockedBuffer",
							fset.Position(n.Pos()), fd.Name.Name, n.X.(*ast.Ident).Name, n.Sel.Name)
					}
				}
				return true
			}
			ast.Inspect(fd.Type, flag)
			ast.Inspect(fd.Body, flag)
		}
	}
	srctest.MinCount(t, "test files read in internal/api", files, 40)
	srctest.MinCount(t, "test functions that construct a server", constructing, 30)
	// Every handler built in a constructing function, whatever the sink's
	// spelling (round 3: a floor on the `&x` spelling alone sat at the live
	// count, so rewriting one site to the house `logs := &lockedBuffer{}`
	// would have failed the scan's self-check for the wrong reason).
	srctest.MinCount(t, "log handlers built in constructing functions", handlers, 20)
}
