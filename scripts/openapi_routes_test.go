// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

// The OpenAPI contract (docs/api/openapi.yaml) against the routes the api
// process registers (internal/api). The spec is the single source of the
// public API reference, so it must say what the server does:
//
//   - the same (method, path) pairs, both ways;
//   - path parameters that are the pattern's wildcards, and query
//     parameters that the handler reads, both ways;
//   - the access each handler enforces (public, read, user, session,
//     admin), as the operation's security requirements;
//   - the refusals that access and the middleware imply (429 and 503
//     everywhere, 401 off the public paths, 403 where a handler can refuse
//     by role, scope or quota, 500 where it can fail on the server, 304
//     with ETag and If-None-Match on the response-cached routes), and no
//     documented 4xx the handler cannot produce.
//
// The handler facts come from the AST of internal/api: a route's handler
// is the HandleFunc argument (unwrapped from s.cachedRepoGET, the one
// wrapper understood), and what it does is what it can reach through calls
// to functions of the package. The reach over-approximates (a helper
// called on one branch counts), which errs toward requiring a refusal to
// be documented, never toward hiding one.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

const (
	openAPISpecPath = "docs/api/openapi.yaml"
	apiPackageDir   = "internal/api"
)

// apiRoute is one mux registration.
type apiRoute struct {
	method, path string // path as registered, {name...} kept
	handler      string // "Server.handleX"
	cached       bool   // registered through s.cachedRepoGET
}

// apiHandlerFacts is what a handler can reach.
type apiHandlerFacts struct {
	calls    map[string]bool            // package functions and methods reachable from the handler
	query    map[string]bool            // query parameter names read with a string literal
	statuses map[string]bool            // http.StatusX names used anywhere the handler reaches (http.NotFound counts as StatusNotFound)
	own      map[string]bool            // http.StatusX names used in the handler's own body
	byFunc   map[string]map[string]bool // reached function -> the http.StatusX names it uses
	public   bool                       // the path is on auth.go's publicPaths allowlist
}

// apiPackageScan is what scanAPIPackage reads from the package.
type apiPackageScan struct {
	routes   []apiRoute
	facts    map[string]apiHandlerFacts // by "METHOD /path" as registered
	problems []string                   // registrations the scan cannot read; each fails the test
}

// externalQueryReaders are the functions of OTHER packages that read query
// parameters for a handler (the reach follows calls within internal/api
// only), with the names each reads from the url.Values it is given.
var externalQueryReaders = map[string][]string{
	// The one queue-page parser (stage 3d of summary/59, 0.29.109).
	"queueview.ParseParams": {"page", "page_size", "q", "sort", "dir"},
}

// accessExceptions are handlers whose access is not decided by one of the
// require helpers. Each states why.
var accessExceptions = map[string]string{
	// Reads the Bearer token itself: no token is a 401, an API token a
	// 400 (an administrator revokes those); only a session is signed out.
	"Server.handleLogout": "session",
}

// apiAccess classifies a handler by the strongest check it reaches.
func apiAccess(handler string, f apiHandlerFacts) string {
	if a, ok := accessExceptions[handler]; ok {
		return a
	}
	return derivedAccess(f)
}

// derivedAccess is the class the reach alone gives, before exceptions.
func derivedAccess(f apiHandlerFacts) string {
	switch {
	case f.public:
		return "public"
	case f.calls["Server.requireAdmin"]:
		return "admin"
	case f.calls["Server.requireSession"]:
		return "session"
	case f.calls["Server.requireUser"]:
		return "user"
	}
	return "read"
}

// wantSecurity is the security requirement each access class is documented
// with (docs/api/conventions.md, "Access"): one entry per alternative (OR),
// the schemes of one alternative (AND) joined by "+", and "-" for the
// anonymous alternative {}.
var wantSecurity = map[string][]string{
	"public":  {},
	"read":    {"sessionToken", "apiToken", "-"},
	"user":    {"sessionToken", "apiToken"},
	"session": {"sessionToken"},
	"admin":   {"sessionToken:admin"},
}

// forbiddenTriggers are the package functions that answer 403 for a
// handler: role and token-kind checks, repository scope, and the quota
// refusals (the allocation answers 403).
var forbiddenTriggers = []string{
	"Server.requireAdmin", "Server.requireSession", "Server.authorizeRepo",
	"Server.refuseCapacity", "Server.refuseAutoAdd", "Server.refuseTokenOutOfScope",
}

// forbidsReachable: the handler can answer 403. Any reached function that
// uses http.StatusForbidden counts (the handler's own body, requireAdmin,
// writeCapacityRefusal, a helper added later), except requireUser, whose
// 403 is defensive ("not expected": an API token without an identity) and
// would otherwise admit 403 on every signed-in route. The named triggers
// count even when their 403 is written through a variable.
func forbidsReachable(f apiHandlerFacts) bool {
	for fn, statuses := range f.byFunc {
		if fn != "Server.requireUser" && statuses["StatusForbidden"] {
			return true
		}
	}
	if f.own["StatusForbidden"] {
		return true
	}
	for _, fn := range forbiddenTriggers {
		if f.calls[fn] {
			return true
		}
	}
	return false
}

// impliedResponseExceptions are responses the reach implies but one
// method of a shared handler never writes. Keyed by "METHOD /spec/path";
// each states why.
var impliedResponseExceptions = map[string]map[string]string{
	"DELETE /api/v1/repos/{repoID}/star": {
		// handleStarRepo serves PUT and DELETE; its scope and quota branch
		// runs only when r.Method != DELETE ("Unstar needs no scope").
		"403": "unstar skips the scope and quota branch",
	},
	// Server.refuse answers every refusal kind from one table (stage 1 of
	// summary/59), so its Forbidden arm is reached from every caller; the
	// service returns Forbidden only from operations that require an
	// administrator, which these group operations do not.
	"POST /api/v1/groups": {
		"403": "CreateGroup needs no role; refuse's Forbidden arm is unreachable here",
	},
	"GET /api/v1/groups/{groupID}/repos": {
		"403": "GroupAccess refuses with NotOwned (404), never Forbidden",
	},
	"GET /api/v1/groups/{groupID}/orgs": {
		"403": "GroupAccess refuses with NotOwned (404), never Forbidden",
	},
	"GET /api/v1/groups/{groupID}/pending-adds": {
		"403": "GroupAccess refuses with NotOwned (404), never Forbidden",
	},
	"POST /api/v1/groups/{groupID}/repos/{repoID}/remove": {
		"403": "RemoveFromGroup is owner-only (NotOwned, 404), never Forbidden",
	},
}

// documentedStatusSources: a documented 4xx must be one the handler can
// write. 401, 429 and 503 come from the middleware and are not listed.
var documentedStatusSources = map[string]string{
	"400": "StatusBadRequest",
	"403": "StatusForbidden",
	"404": "StatusNotFound",
	"409": "StatusConflict",
	"422": "StatusUnprocessableEntity",
}

// scanAPIPackage parses the package's sources (file name -> source) and
// returns its routes and, per route, what the handler reaches.
func scanAPIPackage(files map[string]string) (apiPackageScan, error) {
	fset := token.NewFileSet()
	funcs := map[string]*ast.FuncDecl{}   // "Name" for functions, "Type.Name" for methods
	methodsNamed := map[string][]string{} // method name -> its "Type.Name" keys
	public := map[string]bool{}
	var scan apiPackageScan
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, files[name], 0)
		if err != nil {
			return scan, err
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				key := funcKey(d)
				funcs[key] = d
				if d.Recv != nil {
					methodsNamed[d.Name.Name] = append(methodsNamed[d.Name.Name], key)
				}
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					vs, ok := sp.(*ast.ValueSpec)
					if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "publicPaths" || len(vs.Values) != 1 {
						continue
					}
					lit, ok := vs.Values[0].(*ast.CompositeLit)
					if !ok {
						scan.problems = append(scan.problems, "publicPaths is no longer a map literal: the public classification cannot be read")
						continue
					}
					for _, el := range lit.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							if k, ok := kv.Key.(*ast.BasicLit); ok {
								p, _ := strconv.Unquote(k.Value)
								public[p] = true
							}
						}
					}
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleFunc" || len(call.Args) != 2 {
				return true
			}
			at := fset.Position(call.Pos()).String()
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				scan.problems = append(scan.problems, at+": a HandleFunc pattern that is not a string literal cannot be compared with the spec")
				return true
			}
			pattern, _ := strconv.Unquote(lit.Value)
			method, path, ok := strings.Cut(pattern, " ")
			if !ok {
				scan.problems = append(scan.problems, at+": pattern "+strconv.Quote(pattern)+" has no method; every route names one")
				return true
			}
			r := apiRoute{method: method, path: path}
			h := call.Args[1]
			if wrap, ok := h.(*ast.CallExpr); ok {
				ws, isSel := wrap.Fun.(*ast.SelectorExpr)
				if !isSel || ws.Sel.Name != "cachedRepoGET" || len(wrap.Args) == 0 {
					scan.problems = append(scan.problems, at+": route "+strconv.Quote(pattern)+" is wrapped in a call this test does not understand (only s.cachedRepoGET(policy, s.handleX) is); teach the test what the wrapper does to access and responses")
					return true
				}
				r.cached = true
				h = wrap.Args[len(wrap.Args)-1]
			}
			hs, ok := h.(*ast.SelectorExpr)
			if !ok {
				scan.problems = append(scan.problems, at+": route "+strconv.Quote(pattern)+" has a handler this test cannot name")
				return true
			}
			r.handler = "Server." + hs.Sel.Name // resolved once every file is read (below)
			scan.routes = append(scan.routes, r)
			return true
		})
	}
	if len(public) == 0 {
		scan.problems = append(scan.problems, "publicPaths not found: the public classification cannot be read")
	}

	scan.facts = map[string]apiHandlerFacts{}
	for _, r := range scan.routes {
		if funcs[r.handler] == nil {
			scan.problems = append(scan.problems, r.method+" "+r.path+": handler "+r.handler+" is not a method of Server in this package; the test cannot read what it enforces")
			continue
		}
		f := apiHandlerFacts{calls: map[string]bool{}, query: map[string]bool{}, statuses: map[string]bool{}, own: map[string]bool{}, byFunc: map[string]map[string]bool{}, public: public[r.path]}
		var visit func(name string)
		visit = func(name string) {
			if f.calls[name] {
				return
			}
			f.calls[name] = true
			fd := funcs[name]
			if fd == nil || fd.Body == nil {
				return
			}
			isHandler := name == r.handler
			recvName, recvType := receiverOf(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" && strings.HasPrefix(sel.Sel.Name, "Status") {
						f.statuses[sel.Sel.Name] = true
						if f.byFunc[name] == nil {
							f.byFunc[name] = map[string]bool{}
						}
						f.byFunc[name][sel.Sel.Name] = true
						if isHandler {
							f.own[sel.Sel.Name] = true
						}
					}
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					if funcs[fn.Name] != nil {
						visit(fn.Name)
					}
				case *ast.SelectorExpr:
					if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "http" && fn.Sel.Name == "NotFound" {
						f.statuses["StatusNotFound"] = true
					}
					if name, ok := queryRead(fn, call); ok {
						f.query[name] = true
					}
					// A parameter reader of another package: the reach cannot
					// follow it, so the table says what it reads.
					if pkg, ok := fn.X.(*ast.Ident); ok {
						for _, name := range externalQueryReaders[pkg.Name+"."+fn.Sel.Name] {
							f.query[name] = true
						}
					}
					// recv.M() is the method of the receiver's own type. Any
					// other x.M() is followed only when one type in the
					// package has a method M: without type information a
					// shared name (get, invalidate) cannot be resolved, and
					// following every one would attribute other types'
					// checks to this handler.
					if id, ok := fn.X.(*ast.Ident); ok && recvName != "" && id.Name == recvName {
						if key := recvType + "." + fn.Sel.Name; funcs[key] != nil {
							visit(key)
						}
					} else if keys := methodsNamed[fn.Sel.Name]; len(keys) == 1 {
						visit(keys[0])
					}
				}
				return true
			})
		}
		visit(r.handler)
		scan.facts[r.method+" "+r.path] = f
	}
	return scan, nil
}

// apiPackageFacts scans internal/api; every scan problem fails the test.
func apiPackageFacts(t *testing.T) ([]apiRoute, map[string]apiHandlerFacts) {
	t.Helper()
	scan, err := scanAPIPackage(srctest.PackageFiles(t, apiPackageDir, 20))
	if err != nil {
		t.Fatalf("parse %s: %v", apiPackageDir, err)
	}
	for _, p := range scan.problems {
		t.Error(p)
	}
	return scan.routes, scan.facts
}

// funcKey names a declaration: "Name" for a function, "Type.Name" for a
// method (pointer receivers included).
func funcKey(fd *ast.FuncDecl) string {
	if _, typ := receiverOf(fd); typ != "" {
		return typ + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// receiverOf returns a method's receiver variable and type names.
func receiverOf(fd *ast.FuncDecl) (name, typ string) {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return "", ""
	}
	field := fd.Recv.List[0]
	t := field.Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if idx, ok := t.(*ast.IndexExpr); ok { // a generic receiver
		t = idx.X
	}
	if id, ok := t.(*ast.Ident); ok {
		typ = id.Name
	}
	if len(field.Names) == 1 {
		name = field.Names[0].Name
	}
	return name, typ
}

// queryRead reports a query parameter read with a literal name:
// X.Get("name") or X.Has("name") whose receiver is not a header.
func queryRead(fn *ast.SelectorExpr, call *ast.CallExpr) (string, bool) {
	if (fn.Sel.Name != "Get" && fn.Sel.Name != "Has") || len(call.Args) != 1 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	if strings.Contains(strings.ToLower(exprText(fn.X)), "header") {
		return "", false
	}
	name, err := strconv.Unquote(lit.Value)
	return name, err == nil
}

// exprText spells a receiver expression (r.URL.Query(), q, w.Header()).
func exprText(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return exprText(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return exprText(x.Fun) + "()"
	case *ast.Ident:
		return x.Name
	}
	return "?"
}

// openAPIDoc is the part of the spec the checks read; references are
// resolved through the raw document.
type openAPIDoc struct {
	raw   map[string]any
	paths map[string]map[string]any
}

func loadOpenAPI(t *testing.T) openAPIDoc {
	t.Helper()
	doc, err := parseOpenAPI([]byte(srctest.Read(t, openAPISpecPath)))
	if err != "" {
		t.Fatalf("%s: %s", openAPISpecPath, err)
	}
	return doc
}

func parseOpenAPI(src []byte) (openAPIDoc, string) {
	var raw map[string]any
	if err := yaml.Unmarshal(src, &raw); err != nil {
		return openAPIDoc{}, "does not parse: " + err.Error()
	}
	if v, _ := raw["openapi"].(string); !strings.HasPrefix(v, "3.1.") {
		return openAPIDoc{}, "declares openapi " + strconv.Quote(v) + "; the docs renderer and the conventions are written for 3.1"
	}
	doc := openAPIDoc{raw: raw, paths: map[string]map[string]any{}}
	paths, _ := raw["paths"].(map[string]any)
	for p, item := range paths {
		m, ok := item.(map[string]any)
		if !ok {
			return doc, "path " + p + " is not a mapping"
		}
		doc.paths[p] = m
	}
	return doc, ""
}

// lookup follows a {$ref: '#/...'} node to its target; problem is set when
// a reference dangles.
func (d openAPIDoc) lookup(node any) (m map[string]any, problem string) {
	m, _ = node.(map[string]any)
	for i := 0; m != nil && i < 8; i++ {
		ref, ok := m["$ref"].(string)
		if !ok {
			return m, ""
		}
		var cur any = d.raw
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cm, _ := cur.(map[string]any)
			cur = cm[part]
		}
		if cur == nil {
			return nil, "$ref " + strconv.Quote(ref) + " does not resolve"
		}
		m, _ = cur.(map[string]any)
	}
	return m, ""
}

func (d openAPIDoc) resolve(t *testing.T, node any) map[string]any {
	t.Helper()
	m, problem := d.lookup(node)
	if problem != "" {
		t.Error(problem)
	}
	return m
}

var httpMethods = []string{"get", "put", "post", "delete", "patch", "head", "options", "trace"}

var templateParamRe = regexp.MustCompile(`\{([^}]+)\}`)

// specPathOf is the spec's spelling of a mux path: a {name...} wildcard
// (the rest of the path) is written {name}.
func specPathOf(muxPath string) string {
	return strings.ReplaceAll(muxPath, "...}", "}")
}

func TestOpenAPISpecMatchesRegisteredRoutes(t *testing.T) {
	routes, facts := apiPackageFacts(t)
	srctest.MinCount(t, "routes registered in internal/api", len(routes), 100)
	doc := loadOpenAPI(t)

	registered := map[string]apiRoute{}
	for _, r := range routes {
		key := r.method + " " + specPathOf(r.path)
		if _, dup := registered[key]; dup {
			t.Errorf("route %s is registered twice", key)
		}
		registered[key] = r
	}
	specOps := map[string]map[string]any{}
	pathParams := map[string][]any{}
	for p, item := range doc.paths {
		if list, ok := item["parameters"].([]any); ok {
			pathParams[p] = list
		}
		for k, v := range item {
			if !slices.Contains(httpMethods, k) {
				continue
			}
			op, ok := v.(map[string]any)
			if !ok {
				t.Errorf("%s %s is not a mapping", strings.ToUpper(k), p)
				continue
			}
			specOps[strings.ToUpper(k)+" "+p] = op
		}
	}
	matched := 0
	for key := range registered {
		if _, ok := specOps[key]; ok {
			matched++
		} else {
			t.Errorf("route %s is registered but not in %s: document it there in the same change", key, openAPISpecPath)
		}
	}
	for key := range specOps {
		if _, ok := registered[key]; !ok {
			t.Errorf("%s is in %s but no route serves it: remove it, or fix the path or method", key, openAPISpecPath)
		}
	}
	t.Logf("routes: %d registered, %d in the spec, %d matched", len(registered), len(specOps), matched)

	// Every reviewed exception must still be needed: without it, the check
	// it excuses fires on its route (a stale exception would silently
	// excuse a future regression).
	for key, codes := range impliedResponseExceptions {
		r, ok := registered[key]
		if !ok {
			t.Errorf("impliedResponseExceptions[%s]: no such route; remove the entry", key)
			continue
		}
		f := facts[r.method+" "+r.path]
		without := responseProblemsWith(doc, specOps[key], r, f, nil)
		for code := range codes {
			if !slices.ContainsFunc(without, func(p string) bool { return strings.Contains(p, "response "+code+" is not documented") }) {
				t.Errorf("impliedResponseExceptions[%s][%s] is stale: the route does not need it any more; remove it", key, code)
			}
		}
	}
	for handler, access := range accessExceptions {
		found := false
		for _, r := range registered {
			if r.handler != handler {
				continue
			}
			found = true
			if derivedAccess(facts[r.method+" "+r.path]) == access {
				t.Errorf("accessExceptions[%s] is stale: the handler already reads as %q; remove it", handler, access)
			}
		}
		if !found {
			t.Errorf("accessExceptions[%s]: no route uses this handler; remove the entry", handler)
		}
	}

	keys := make([]string, 0, len(registered))
	for k := range registered {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		r := registered[key]
		op, ok := specOps[key]
		if !ok {
			continue
		}
		f, ok := facts[r.method+" "+r.path]
		if !ok {
			continue // the scan already failed the test for this route
		}
		_, specPath, _ := strings.Cut(key, " ")
		checkOperationParameters(t, doc, key, specPath, op, pathParams[specPath], f)
		for _, p := range securityProblems(op, apiAccess(r.handler, f)) {
			t.Errorf("%s: %s", key, p)
		}
		for _, p := range responseProblems(doc, op, r, f) {
			t.Errorf("%s: %s", key, p)
		}
	}
}

// checkOperationParameters: the path template's parameters are declared
// (required, in: path), and the query parameters the spec declares are the
// ones the handler reads.
func checkOperationParameters(t *testing.T, doc openAPIDoc, key, specPath string, op map[string]any, common []any, f apiHandlerFacts) {
	t.Helper()
	declared := map[string]map[string]bool{"path": {}, "query": {}, "header": {}, "cookie": {}}
	params, _ := op["parameters"].([]any)
	for _, p := range append(slices.Clone(common), params...) {
		pm := doc.resolve(t, p)
		if pm == nil {
			continue
		}
		in, _ := pm["in"].(string)
		name, _ := pm["name"].(string)
		if declared[in] == nil {
			t.Errorf("%s: parameter %q has unknown location %q", key, name, in)
			continue
		}
		declared[in][name] = true
		if in == "path" && pm["required"] != true {
			t.Errorf("%s: path parameter %q must be required", key, name)
		}
	}
	want := map[string]bool{}
	for _, m := range templateParamRe.FindAllStringSubmatch(specPath, -1) {
		want[m[1]] = true
	}
	for name := range want {
		if !declared["path"][name] {
			t.Errorf("%s: path parameter {%s} is not declared", key, name)
		}
	}
	for name := range declared["path"] {
		if !want[name] {
			t.Errorf("%s: path parameter %q is declared but the path has no {%s}", key, name, name)
		}
	}
	for name := range declared["query"] {
		if !f.query[name] {
			t.Errorf("%s: query parameter %q is declared but the handler never reads it", key, name)
		}
	}
	for name := range f.query {
		if !declared["query"][name] {
			t.Errorf("%s: the handler reads query parameter %q but the spec does not declare it", key, name)
		}
	}
}

// securityKey spells an operation's security requirements: one entry per
// alternative (OR); within an alternative (AND) the schemes, each with its
// roles, sorted and joined by "+"; "-" for the anonymous alternative {}.
// The renderer's _security_key (docs/_ext/aveloxis_openapi.py) is the
// same rule.
func securityKey(sec []any) []string {
	var got []string
	for _, alt := range sec {
		m, _ := alt.(map[string]any)
		if len(m) == 0 {
			got = append(got, "-")
			continue
		}
		var parts []string
		for scheme, roles := range m {
			name := scheme
			if rl, _ := roles.([]any); len(rl) > 0 {
				var rs []string
				for _, r := range rl {
					s, _ := r.(string)
					rs = append(rs, s)
				}
				name += ":" + strings.Join(rs, ",")
			}
			parts = append(parts, name)
		}
		sort.Strings(parts)
		got = append(got, strings.Join(parts, "+"))
	}
	return got
}

// securityProblems: the security requirements are the ones the handler's
// access class is documented with.
func securityProblems(op map[string]any, access string) []string {
	sec, ok := op["security"].([]any)
	if !ok {
		return []string{"no security list; the handler's access is " + strconv.Quote(access) + " (docs/api/conventions.md, Access)"}
	}
	if got := securityKey(sec); !slices.Equal(got, wantSecurity[access]) {
		return []string{"security is [" + strings.Join(got, " ") + "]; the handler's access is " + strconv.Quote(access) + ", documented as [" + strings.Join(wantSecurity[access], " ") + "]"}
	}
	return nil
}

// responseProblems: the refusals implied by the middleware and by what the
// handler reaches are documented, and every documented 4xx is one the
// handler can write.
func responseProblems(doc openAPIDoc, op map[string]any, r apiRoute, f apiHandlerFacts) []string {
	return responseProblemsWith(doc, op, r, f, impliedResponseExceptions)
}

// responseProblemsWith is responseProblems under a given exception list
// (the staleness check runs it without one).
func responseProblemsWith(doc openAPIDoc, op map[string]any, r apiRoute, f apiHandlerFacts, exceptions map[string]map[string]string) []string {
	var out []string
	responses, _ := op["responses"].(map[string]any)
	has := func(code string) bool { _, ok := responses[code]; return ok }
	need := func(code, why string) {
		if _, excused := exceptions[r.method+" "+specPathOf(r.path)][code]; excused {
			return
		}
		if !has(code) {
			out = append(out, "response "+code+" is not documented ("+why+")")
		}
	}
	need("429", "every request passes the rate limiter")
	need("503", "a presented token the database cannot check, or http_timeout_seconds, is a 503 on every route")
	access := apiAccess(r.handler, f)
	if access != "public" {
		need("401", "the route needs a token when api.require_auth is on, or always ("+access+")")
	}
	forbids := forbidsReachable(f)
	if forbids {
		need("403", "the handler refuses by role, token kind, scope or quota, or writes 403 itself")
	}
	if f.calls["Server.serverError"] {
		need("500", "the handler can answer a server-side failure (serverError)")
	}
	for code, status := range documentedStatusSources {
		if !has(code) {
			continue
		}
		// A 403 is reachable as forbidsReachable says, never through
		// requireUser's defensive one.
		reachable := f.statuses[status]
		if code == "403" {
			reachable = forbids
		}
		if !reachable {
			out = append(out, "response "+code+" is documented but the handler cannot write it (no http."+status+" it can reach)")
		}
	}
	if access == "public" && has("401") {
		out = append(out, "response 401 is documented but the route is public: no token is ever required")
	}
	if !r.cached && has("304") {
		out = append(out, "response 304 is documented but the route is not response-cached (cachedRepoGET): nothing answers If-None-Match")
	}
	if has("500") && !f.calls["Server.serverError"] && !f.statuses["StatusInternalServerError"] {
		out = append(out, "response 500 is documented but the handler reaches neither serverError nor http.StatusInternalServerError")
	}
	if f.calls["Server.authorizeRepo"] {
		if !successDeclaresHeader(doc, responses, sharedWithMeHeaderName) {
			out = append(out, "the handler checks repository scope, which can add the repository to Shared with Me: declare "+sharedWithMeHeaderName+" on the success response")
		}
	}
	if r.cached {
		need("304", "the route is response-cached (cachedRepoGET) and answers If-None-Match")
		if !successDeclaresHeader(doc, responses, "ETag") {
			out = append(out, "the 200 of a response-cached route carries ETag; document the header")
		}
		found := false
		params, _ := op["parameters"].([]any)
		for _, p := range params {
			if pm, _ := doc.lookup(p); pm != nil && pm["name"] == "If-None-Match" && pm["in"] == "header" {
				found = true
			}
		}
		if !found {
			out = append(out, "a response-cached route reads If-None-Match; declare the header parameter")
		}
	}
	sort.Strings(out)
	return out
}

// sharedWithMeHeaderName is the header authorizeRepo sets when it adds the
// repository to the caller's Shared with Me group (internal/api
// sharedWithMeHeader).
const sharedWithMeHeaderName = "X-Aveloxis-Added-To-Group"

// successDeclaresHeader: some 2xx response declares the header.
func successDeclaresHeader(doc openAPIDoc, responses map[string]any, header string) bool {
	for code, resp := range responses {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		m, _ := doc.lookup(resp)
		headers, _ := m["headers"].(map[string]any)
		if _, ok := headers[header]; ok {
			return true
		}
	}
	return false
}

// TestOpenAPIOperationConventions: the naming and documentation rules of
// docs/api/conventions.md that a machine can check.
func TestOpenAPIOperationConventions(t *testing.T) {
	doc := loadOpenAPI(t)
	tags := map[string]bool{}
	rawTags, _ := doc.raw["tags"].([]any)
	for _, tg := range rawTags {
		m, _ := tg.(map[string]any)
		name, _ := m["name"].(string)
		if desc, _ := m["description"].(string); desc == "" {
			t.Errorf("tag %q has no description", name)
		}
		tags[name] = true
	}
	ids := map[string]string{}
	opID := regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	examined := 0
	for p, item := range doc.paths {
		for _, method := range httpMethods {
			v, ok := item[method]
			if !ok {
				continue
			}
			examined++
			op, _ := v.(map[string]any)
			key := strings.ToUpper(method) + " " + p
			id, _ := op["operationId"].(string)
			switch {
			case !opID.MatchString(id):
				t.Errorf("%s: operationId %q is not lowerCamelCase", key, id)
			case ids[id] != "":
				t.Errorf("%s: operationId %q is also %s", key, id, ids[id])
			}
			ids[id] = key
			// A read names what it returns (a noun phrase); a verb prefix
			// says nothing a GET does not already say.
			if method == "get" {
				for _, verb := range []string{"get", "list", "fetch", "retrieve", "read", "show"} {
					if strings.HasPrefix(id, verb) && len(id) > len(verb) && id[len(verb)] >= 'A' && id[len(verb)] <= 'Z' {
						t.Errorf("%s: operationId %q starts with %q; a read is named for what it returns (docs/api/conventions.md)", key, id, verb)
					}
				}
			}
			summary, _ := op["summary"].(string)
			if summary == "" || !strings.HasSuffix(strings.TrimSpace(summary), ".") {
				t.Errorf("%s: summary %q is not one sentence ending in a period", key, summary)
			}
			if method == "get" && !strings.HasPrefix(summary, "Returns ") && !strings.HasPrefix(summary, "Answers ") {
				t.Errorf("%s: a read's summary says what it returns (\"Returns ...\"); got %q", key, summary)
			}
			opTags, _ := op["tags"].([]any)
			if len(opTags) != 1 {
				t.Errorf("%s: has %d tags; each operation belongs to exactly one section", key, len(opTags))
			} else if name, _ := opTags[0].(string); !tags[name] {
				t.Errorf("%s: tag %q is not declared in the top-level tags", key, name)
			}
			if desc, _ := op["description"].(string); !strings.Contains(desc, "Handler: `") {
				t.Errorf("%s: the description does not cite its handler (\"Handler: `handleX`.\")", key)
			} else if !strings.HasPrefix(desc, "Handler: `") && !strings.Contains(desc, "\n\nHandler: `") {
				t.Errorf("%s: the \"Handler:\" line is not its own paragraph (a blank line before it); it runs into the text above when rendered", key)
			}
		}
	}
	srctest.MinCount(t, "operations examined", examined, 100)
}

// TestOpenAPIReferencesResolve: every $ref in the spec names something in
// it (the renderer resolves them at build time; a dangling one fails the
// docs build late and far from the edit).
func TestOpenAPIReferencesResolve(t *testing.T) {
	doc := loadOpenAPI(t)
	refs := 0
	var walk func(n any)
	walk = func(n any) {
		switch x := n.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				refs++
				if !strings.HasPrefix(ref, "#/") {
					t.Errorf("$ref %q is not local; the docs build must not reach the network", ref)
				} else {
					doc.resolve(t, x)
				}
			}
			for _, v := range x {
				walk(v)
			}
		case []any:
			for _, v := range x {
				walk(v)
			}
		}
	}
	walk(doc.raw)
	srctest.MinCount(t, "$refs examined", refs, 100)
}

// yaml11Booleans are the plain scalars that YAML 1.1 parsers (PyYAML,
// which the docs build uses, and SnakeYAML) read as booleans but YAML 1.2
// (yaml.v3, this test's parser) reads as strings: yes/no/on/off in the
// three case forms those resolvers match (lower, Capitalized, UPPER). Not
// y/n (neither resolver matches them) and not true/false, which are
// booleans in both versions and are meant as booleans where the spec
// writes them (required: true). Without this test an
// `enum: [enforce, shadow, off]` renders as `False`.
var yaml11Booleans = map[string]bool{
	"yes": true, "Yes": true, "YES": true, "no": true, "No": true, "NO": true,
	"on": true, "On": true, "ON": true, "off": true, "Off": true, "OFF": true,
}

// unquotedYAML11Booleans returns the line of every plain (unquoted) scalar
// that a YAML 1.1 parser reads as a boolean.
func unquotedYAML11Booleans(src []byte) ([]int, int, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, 0, err
	}
	var lines []int
	scalars := 0
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			scalars++
			if n.Style == 0 && yaml11Booleans[n.Value] {
				lines = append(lines, n.Line)
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&root)
	return lines, scalars, nil
}

func TestOpenAPIHasNoUnquotedYAML11Booleans(t *testing.T) {
	lines, scalars, err := unquotedYAML11Booleans([]byte(srctest.Read(t, openAPISpecPath)))
	if err != nil {
		t.Fatal(err)
	}
	srctest.MinCount(t, "scalars examined", scalars, 1000)
	for _, l := range lines {
		t.Errorf("%s:%d: an unquoted yes/no/on/off is a boolean to YAML 1.1 readers (the docs build's PyYAML); quote it", openAPISpecPath, l)
	}
	got, _, _ := unquotedYAML11Booleans([]byte("a: [enforce, \"off\", 'on', OFF]\nb: No\nc: nope\nd: [y, n, oFF, true]\n"))
	if !slices.Equal(got, []int{1, 2}) {
		t.Errorf("fixture: flagged lines %v, want [1 2] (OFF unquoted on line 1, No on line 2; quoted forms, longer words, y/n, mixed case and true pass)", got)
	}
}

// TestOpenAPIAccessClassifierFixtures drives apiAccess with each class.
func TestOpenAPIAccessClassifierFixtures(t *testing.T) {
	calls := func(names ...string) apiHandlerFacts {
		f := apiHandlerFacts{calls: map[string]bool{}}
		for _, n := range names {
			f.calls[n] = true
		}
		return f
	}
	pub := calls()
	pub.public = true
	for _, c := range []struct {
		name, handler string
		facts         apiHandlerFacts
		want          string
	}{
		{"public path", "Server.handleHealth", pub, "public"},
		{"no helper", "Server.handleRepoStats", calls("Server.authorizeRepo"), "read"},
		{"requireUser", "Server.handleGroupsList", calls("Server.requireUser"), "user"},
		{"requireSession wins over requireUser", "Server.handleMeEmail", calls("Server.requireSession", "Server.requireUser"), "session"},
		{"requireAdmin wins", "Server.handleAdminUsers", calls("Server.requireAdmin", "Server.requireUser"), "admin"},
		{"exception", "Server.handleLogout", calls(), "session"},
	} {
		if got := apiAccess(c.handler, c.facts); got != c.want {
			t.Errorf("%s: apiAccess = %q, want %q", c.name, got, c.want)
		}
	}
}

// fixturePackage is a minimal internal/api: a Server, publicPaths, and the
// routes and handlers each fixture adds.
func fixturePackage(routes, handlers string) map[string]string {
	return map[string]string{"server.go": `package api
import "net/http"
type Server struct{ mux *http.ServeMux; other *other }
type other struct{}
var publicPaths = map[string]bool{"/api/v1/health": true}
func (s *Server) cachedRepoGET(p int, h http.HandlerFunc) http.HandlerFunc { return h }
func (s *Server) wrap(h http.HandlerFunc) http.HandlerFunc { return h }
func (s *Server) makeHandler() http.HandlerFunc { return nil }
func (s *Server) authorizeRepo(w http.ResponseWriter, r *http.Request, id int64) bool { return true }
func (s *Server) refuseCapacity(w http.ResponseWriter, err error) bool { return false }
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) bool { http.Error(w, "not expected", http.StatusForbidden); return true }
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, h string, err error) {}
func writeCapacityRefusal(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) }
func (o *other) handleElsewhere(w http.ResponseWriter, r *http.Request) {}
func (s *Server) register() {
` + routes + `
}
` + handlers}
}

// TestScanAPIPackageFixtures: registrations the scan cannot read fail it,
// and the facts it reads drive the response checks.
func TestScanAPIPackageFixtures(t *testing.T) {
	for _, c := range []struct {
		name, routes string
		wantProblem  string
	}{
		{"handler on another receiver", `s.mux.HandleFunc("GET /a", s.other.handleElsewhere)`, "is not a method of Server"},
		{"unknown wrapper", `s.mux.HandleFunc("GET /a", s.wrap(s.handleA))`, "wrapped in a call this test does not understand"},
		{"zero-argument wrapper", `s.mux.HandleFunc("GET /a", s.makeHandler())`, "wrapped in a call this test does not understand"},
		{"pattern without a method", `s.mux.HandleFunc("/a", s.handleA)`, "has no method"},
	} {
		scan, err := scanAPIPackage(fixturePackage(c.routes, `func (s *Server) handleA(w http.ResponseWriter, r *http.Request) {}`))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !slices.ContainsFunc(scan.problems, func(p string) bool { return strings.Contains(p, c.wantProblem) }) {
			t.Errorf("%s: problems %q, want one containing %q", c.name, scan.problems, c.wantProblem)
		}
	}

	scan, err := scanAPIPackage(fixturePackage(`
	s.mux.HandleFunc("GET /own403", s.handleOwn403)
	s.mux.HandleFunc("GET /capacity", s.handleCapacity)
	s.mux.HandleFunc("GET /plain", s.handlePlain)
	s.mux.HandleFunc("GET /scoped", s.cachedRepoGET(1, s.handleScoped))
	s.mux.HandleFunc("GET /user", s.handleUser)
	s.mux.HandleFunc("GET /refusal", s.handleRefusal)
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /fails", s.handleFails)
	s.mux.HandleFunc("GET /fails500", s.handleFails500)`, `
func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) { s.requireUser(w, r) }
func (s *Server) handleRefusal(w http.ResponseWriter, r *http.Request) { s.requireUser(w, r); writeCapacityRefusal(w) }
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {}
func (s *Server) handleFails(w http.ResponseWriter, r *http.Request) { s.serverError(w, r, "x", nil) }
func (s *Server) handleFails500(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", http.StatusInternalServerError) }
func (s *Server) handleOwn403(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) }
func (s *Server) handleCapacity(w http.ResponseWriter, r *http.Request) { s.refuseCapacity(w, nil) }
func (s *Server) handlePlain(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
func (s *Server) handleScoped(w http.ResponseWriter, r *http.Request) { s.authorizeRepo(w, r, 1) }`))
	if err != nil || len(scan.problems) != 0 {
		t.Fatalf("fixture scan: %v %q", err, scan.problems)
	}
	doc, perr := parseOpenAPI([]byte("openapi: 3.1.0\npaths: {}\n"))
	if perr != "" {
		t.Fatal(perr)
	}
	base := func(extra ...string) map[string]any {
		resp := map[string]any{"200": map[string]any{"description": "ok"}, "401": map[string]any{}, "429": map[string]any{}, "503": map[string]any{}}
		for _, code := range extra {
			resp[code] = map[string]any{"description": code}
		}
		return map[string]any{"responses": resp}
	}
	routeOf := map[string]apiRoute{}
	for _, r := range scan.routes {
		routeOf[r.path] = r
	}
	for _, c := range []struct {
		name, path string
		op         map[string]any
		want       string // a problem containing this; "" for none
	}{
		{"a direct StatusForbidden needs a 403", "/own403", base(), "response 403 is not documented"},
		{"refuseCapacity needs a 403", "/capacity", base(), "response 403 is not documented"},
		{"documented 403 the handler writes", "/own403", base("403"), ""},
		{"a 409 the handler cannot write", "/plain", base("409"), "response 409 is documented but the handler cannot write it"},
		{"http.NotFound makes 404 reachable", "/plain", base("404"), ""},
		{"a 400 the handler cannot write", "/plain", base("400"), "response 400 is documented but the handler cannot write it"},
		{"scope check needs the Shared with Me header", "/scoped", base("403", "304"), "declare X-Aveloxis-Added-To-Group"},
		{"a helper's defensive 403 does not make 403 reachable", "/user", base("403"), "response 403 is documented but the handler cannot write it"},
		{"a helper that writes 403 needs a 403", "/refusal", base(), "response 403 is not documented"},
		{"a documented 403 a helper writes", "/refusal", base("403"), ""},
		{"a 401 on a public route", "/api/v1/health", map[string]any{"responses": map[string]any{"200": map[string]any{}, "401": map[string]any{}, "429": map[string]any{}, "503": map[string]any{}}}, "the route is public"},
		{"a 304 on an uncached route", "/plain", base("304"), "not response-cached"},
		{"a 500 the handler cannot write", "/plain", base("500"), "reaches neither serverError"},
		{"serverError makes 500 reachable", "/fails", base("500"), ""},
		{"StatusInternalServerError makes 500 reachable", "/fails500", base("500"), ""},
	} {
		r := routeOf[c.path]
		got := responseProblems(doc, c.op, r, scan.facts[r.method+" "+r.path])
		hit := slices.ContainsFunc(got, func(p string) bool { return c.want != "" && strings.Contains(p, c.want) })
		switch {
		case c.want == "" && slices.ContainsFunc(got, func(p string) bool {
			return strings.Contains(p, "403") || strings.Contains(p, "404") || strings.Contains(p, "500")
		}):
			t.Errorf("%s: unexpected problems %q", c.name, got)
		case c.want != "" && !hit:
			t.Errorf("%s: problems %q, want one containing %q", c.name, got, c.want)
		}
	}

	// Security alternatives: OR is one entry each, AND is one joined entry.
	or := []any{map[string]any{"sessionToken": []any{}}, map[string]any{"apiToken": []any{}}}
	and := []any{map[string]any{"sessionToken": []any{}, "apiToken": []any{}}}
	if p := securityProblems(map[string]any{"security": or}, "user"); len(p) != 0 {
		t.Errorf("OR of session and API token is the user class; got %q", p)
	}
	if p := securityProblems(map[string]any{"security": and}, "user"); len(p) == 0 {
		t.Error("an AND of session and API token is not the user class (both tokens at once); the check passed it")
	}
	if got := securityKey(and); !slices.Equal(got, []string{"apiToken+sessionToken"}) {
		t.Errorf("securityKey(AND) = %q, want one alternative [apiToken+sessionToken]", got)
	}
	if got := securityKey(append(slices.Clone(or), map[string]any{})); !slices.Equal(got, []string{"sessionToken", "apiToken", "-"}) {
		t.Errorf("securityKey(OR + anonymous) = %q, want [sessionToken apiToken -]", got)
	}
}
