// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

// The smoke harness walks every 200 JSON body against the response schema
// docs/api/openapi.yaml declares for the route (stage 1b item 6, D6):
// a `null` where the spec says array fails, at any depth — the pin on
// behavior, for every route and every future one, instead of a shape pin
// on which readers use pgx.CollectRows.

var (
	openAPIOnce sync.Once
	openAPIRaw  map[string]any
	openAPIErr  error
)

func openAPIDocument(t *testing.T) map[string]any {
	t.Helper()
	openAPIOnce.Do(func() {
		src, err := os.ReadFile(filepath.Join("..", "..", "docs", "api", "openapi.yaml"))
		if err != nil {
			openAPIErr = err
			return
		}
		openAPIErr = yaml.Unmarshal(src, &openAPIRaw)
	})
	if openAPIErr != nil {
		t.Fatalf("docs/api/openapi.yaml: %v", openAPIErr)
	}
	return openAPIRaw
}

// specJSONSchema is the schema of a route's JSON response with the given
// status, or nil when the spec declares none.
func specJSONSchema(t *testing.T, route string, status int) map[string]any {
	t.Helper()
	doc := openAPIDocument(t)
	method, path, _ := strings.Cut(route, " ")
	path = strings.ReplaceAll(path, "...}", "}")
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	op, _ := item[strings.ToLower(method)].(map[string]any)
	responses, _ := op["responses"].(map[string]any)
	resp, _ := responses[fmt.Sprint(status)].(map[string]any)
	resp = resolveRef(doc, resp)
	content, _ := resp["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	schema, _ := media["schema"].(map[string]any)
	return schema
}

func resolveRef(doc map[string]any, m map[string]any) map[string]any {
	for i := 0; m != nil && i < 8; i++ {
		ref, ok := m["$ref"].(string)
		if !ok {
			return m
		}
		var cur any = doc
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cm, _ := cur.(map[string]any)
			cur = cm[part]
		}
		m, _ = cur.(map[string]any)
	}
	return m
}

func schemaTypes(schema map[string]any) map[string]bool {
	out := map[string]bool{}
	switch ty := schema["type"].(type) {
	case string:
		out[ty] = true
	case []any:
		for _, x := range ty {
			if s, ok := x.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}

// bodyProblems walks v against schema and returns every place a null
// stands where the spec says array (and only array, never null).
func bodyProblems(doc map[string]any, at string, v any, schema map[string]any) []string {
	schema = resolveRef(doc, schema)
	if schema == nil {
		return nil
	}
	if alts, ok := schema["oneOf"].([]any); ok {
		return altProblems(doc, at, v, alts)
	}
	if alts, ok := schema["anyOf"].([]any); ok {
		return altProblems(doc, at, v, alts)
	}
	var out []string
	if all, ok := schema["allOf"].([]any); ok {
		for _, s := range all {
			sm, _ := s.(map[string]any)
			out = append(out, bodyProblems(doc, at, v, sm)...)
		}
	}
	types := schemaTypes(schema)
	if v == nil {
		if types["array"] && !types["null"] {
			out = append(out, at+": null where the spec says array (D6: an empty list is [])")
		}
		return out
	}
	switch val := v.(type) {
	case []any:
		items, _ := schema["items"].(map[string]any)
		for i, e := range val {
			out = append(out, bodyProblems(doc, fmt.Sprintf("%s[%d]", at, i), e, items)...)
		}
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := props[k].(map[string]any); ok {
				out = append(out, bodyProblems(doc, at+"."+k, val[k], ps)...)
			} else if ap, ok := schema["additionalProperties"].(map[string]any); ok {
				out = append(out, bodyProblems(doc, at+"."+k, val[k], ap)...)
			}
		}
	}
	return out
}

func altProblems(doc map[string]any, at string, v any, alts []any) []string {
	var first []string
	for i, s := range alts {
		sm, _ := s.(map[string]any)
		p := bodyProblems(doc, at, v, sm)
		if len(p) == 0 {
			return nil
		}
		if i == 0 {
			first = p
		}
	}
	return first
}

// The walker itself, proven both ways on a fixture: a null under an
// array-only schema is reported, at depth and through $ref, and an
// array-or-null schema or an actual array passes.
func TestBodyProblemsWalker(t *testing.T) {
	doc := map[string]any{"components": map[string]any{"schemas": map[string]any{
		"Row": map[string]any{"type": "object", "properties": map[string]any{
			"tags": map[string]any{"type": "array"},
		}},
	}}}
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"items":    map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/Row"}},
		"maybe":    map[string]any{"type": []any{"array", "null"}},
		"byKey":    map[string]any{"type": "object", "additionalProperties": map[string]any{"$ref": "#/components/schemas/Row"}},
		"nullable": map[string]any{"oneOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "array"}}},
	}}
	body := map[string]any{
		"items":    []any{map[string]any{"tags": nil}, map[string]any{"tags": []any{}}},
		"maybe":    nil,
		"byKey":    map[string]any{"a": map[string]any{"tags": nil}},
		"nullable": nil,
	}
	got := bodyProblems(doc, "$", body, schema)
	want := []string{"$.byKey.a.tags: null where the spec says array (D6: an empty list is [])", "$.items[0].tags: null where the spec says array (D6: an empty list is [])"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("problems = %q; want %q", got, want)
	}
	if p := bodyProblems(doc, "$", nil, map[string]any{"type": "array"}); len(p) != 1 {
		t.Errorf("a null body under an array schema: %q", p)
	}
	if p := bodyProblems(doc, "$", []any{}, map[string]any{"type": "array"}); len(p) != 0 {
		t.Errorf("an empty array: %q", p)
	}
}
