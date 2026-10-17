// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// H-1 (summary/59 §5, the hotfix release): the built-in GUI's inline
// scripts render API answers in the browser. Every string an answer
// carries can be attacker-controlled — a package author writes license
// text, a repository owner writes file names and copyright lines — and a
// value concatenated into innerHTML is markup. So the scripts build data
// into the page with DOM calls (avxEl / textContent), and an HTML sink may
// only ever receive string literals. This pin enforces that structurally:
// any operand that is not a literal fails, whatever its name or source.

// inlineScripts returns the bodies of the <script> blocks without a src.
func inlineScripts(t *testing.T) []string {
	t.Helper()
	var out []string
	rest := allTemplates
	for {
		i := strings.Index(rest, "<script")
		if i < 0 {
			break
		}
		gt := strings.Index(rest[i:], ">")
		if gt < 0 {
			t.Fatal("unterminated <script tag")
		}
		open := rest[i : i+gt+1]
		end := strings.Index(rest[i+gt+1:], "</script>")
		if end < 0 {
			t.Fatal("<script> without </script>")
		}
		body := rest[i+gt+1 : i+gt+1+end]
		if !strings.Contains(open, "src=") {
			out = append(out, body)
		}
		rest = rest[i+gt+1+end+len("</script>"):]
	}
	return out
}

// jsToken is one lexical unit of the scanned JavaScript: a string literal
// (kind 's'), a template literal with a substitution (kind 'T', dynamic), a
// regex literal (kind 'r'), punctuation (kind 'p') or a word (kind 'w').
type jsToken struct {
	kind byte
	text string
	pos  int
}

// regexStartAfter: the punctuation tokens after which a / starts a regex
// literal (exact tokens, including the lexer's own two-character ones).
// After ) or ] a / is division; a script that relies on a regex there
// cannot hide a sink, because TestTemplateScriptsWriteOnlyLiteralsIntoHTML
// also counts every sink name in the raw text, independent of this lexer.
var regexStartAfter = map[string]bool{
	"(": true, ",": true, "=": true, ":": true, "[": true, "!": true, "&": true, "|": true, "?": true,
	"{": true, "}": true, ";": true, "+": true, "-": true, "*": true, "%": true, "<": true, ">": true,
	"~": true, "^": true, "=>": true, "==": true, "+=": true,
}

// lexJS splits JavaScript into tokens, dropping whitespace and comments.
// An unterminated string, template or regex becomes a kind 'u' token (the
// lexer lost its place), which the tests report instead of panicking.
// It is small on purpose: enough to tell a literal from an expression.
func lexJS(src string) []jsToken {
	var toks []jsToken
	// A / starts a regex literal where an expression may start: at the
	// beginning, after punctuation that ends no value, or after return/typeof.
	regexAllowedAfter := func() bool {
		if len(toks) == 0 {
			return true
		}
		tk := toks[len(toks)-1]
		if tk.kind == 'p' {
			return regexStartAfter[tk.text]
		}
		return tk.kind == 'w' && (tk.text == "return" || tk.text == "typeof")
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return toks
			}
			i += end + 4
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(src) {
				toks = append(toks, jsToken{'u', src[i:], i})
				return toks
			}
			toks = append(toks, jsToken{'s', src[i : j+1], i})
			i = j + 1
		case c == '`':
			j, dynamic := i+1, false
			for j < len(src) && src[j] != '`' {
				if src[j] == '\\' {
					j++
				} else if src[j] == '$' && j+1 < len(src) && src[j+1] == '{' {
					dynamic = true
				}
				j++
			}
			if j >= len(src) {
				toks = append(toks, jsToken{'u', src[i:], i})
				return toks
			}
			kind := byte('s')
			if dynamic {
				kind = 'T'
			}
			toks = append(toks, jsToken{kind, src[i : j+1], i})
			i = j + 1
		case c == '/' && regexAllowedAfter():
			j, inClass := i+1, false
			for j < len(src) && (src[j] != '/' || inClass) && src[j] != '\n' {
				switch src[j] {
				case '\\':
					j++
				case '[':
					inClass = true
				case ']':
					inClass = false
				}
				j++
			}
			if j >= len(src) || src[j] != '/' {
				toks = append(toks, jsToken{'u', src[i:min(j, len(src))], i})
				return toks
			}
			j++
			for j < len(src) && (src[j] >= 'a' && src[j] <= 'z') {
				j++
			}
			toks = append(toks, jsToken{'r', src[i:j], i})
			i = j
		case c == '_' || c == '$' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
			j := i
			for j < len(src) && (src[j] == '_' || src[j] == '$' || src[j] == '.' || (src[j] >= 'a' && src[j] <= 'z') || (src[j] >= 'A' && src[j] <= 'Z') || (src[j] >= '0' && src[j] <= '9')) {
				j++
			}
			toks = append(toks, jsToken{'w', src[i:j], i})
			i = j
		default:
			// Two-character operators that matter here: += and ==.
			if i+1 < len(src) && (src[i:i+2] == "+=" || src[i:i+2] == "==" || src[i:i+2] == "=>") {
				toks = append(toks, jsToken{'p', src[i : i+2], i})
				i += 2
				continue
			}
			toks = append(toks, jsToken{'p', string(c), i})
			i++
		}
	}
	return toks
}

// htmlSink is one write of markup into the page and the tokens of the value.
type htmlSink struct {
	what  string
	value []jsToken
	pos   int
}

// htmlSinks finds `x.innerHTML = …`, `x.innerHTML += …`, `x.outerHTML = …`,
// `x.insertAdjacentHTML(pos, …)` and `document.write(…)`, with the tokens of
// the written value up to the end of the statement (or of the call).
func htmlSinks(toks []jsToken) []htmlSink {
	var out []htmlSink
	// valueUntil collects tokens from k until depth drops below 0 or a ;/, at depth 0 (stopAtComma).
	valueUntil := func(k int, stopAtComma bool) []jsToken {
		depth := 0
		var v []jsToken
		for ; k < len(toks); k++ {
			tk := toks[k]
			if tk.kind == 'p' {
				switch tk.text {
				case "(", "[", "{":
					depth++
				case ")", "]", "}":
					depth--
					if depth < 0 {
						return v
					}
				case ";":
					if depth == 0 {
						return v
					}
				case ",":
					if depth == 0 && stopAtComma {
						return v
					}
				}
			}
			v = append(v, tk)
		}
		return v
	}
	for k, tk := range toks {
		if tk.kind != 'w' {
			continue
		}
		name := tk.text[strings.LastIndex(tk.text, ".")+1:]
		next := func(n int) jsToken {
			if k+n < len(toks) {
				return toks[k+n]
			}
			return jsToken{}
		}
		switch {
		case (name == "innerHTML" || name == "outerHTML") && next(1).kind == 'p' && (next(1).text == "=" || next(1).text == "+="):
			out = append(out, htmlSink{tk.text + " " + next(1).text, valueUntil(k+2, true), tk.pos})
		case name == "insertAdjacentHTML" && next(1).text == "(":
			// The markup is the second argument.
			args := valueUntil(k+2, false)
			for a, at := range args {
				if at.kind == 'p' && at.text == "," {
					out = append(out, htmlSink{tk.text, args[a+1:], tk.pos})
					break
				}
			}
		case (tk.text == "document.write" || tk.text == "document.writeln") && next(1).text == "(":
			out = append(out, htmlSink{tk.text, valueUntil(k+2, false), tk.pos})
		}
	}
	return out
}

// literalOnly reports whether a value is string literals joined by + (and
// parentheses), i.e. carries no data.
func literalOnly(v []jsToken) bool {
	if len(v) == 0 {
		return false
	}
	for _, tk := range v {
		switch {
		case tk.kind == 's':
		case tk.kind == 'p' && (tk.text == "+" || tk.text == "(" || tk.text == ")"):
		default:
			return false
		}
	}
	return true
}

func TestTemplateScriptsWriteOnlyLiteralsIntoHTML(t *testing.T) {
	scripts := inlineScripts(t)
	if len(scripts) < 3 {
		t.Fatalf("found %d inline scripts; the repo page, compare page and search widget each have one", len(scripts))
	}
	examined, reads := 0, 0
	for si, s := range scripts {
		toks := lexJS(s)
		for _, tk := range toks {
			if tk.kind == 'u' {
				t.Fatalf("inline script %d: the lexer lost its place at line %d (unterminated string, template or regex)", si, strings.Count(s[:tk.pos], "\n")+1)
			}
		}
		sinks := htmlSinks(toks)
		// Ban the operation, not a spelling: every mention of a sink name in
		// the raw text must be a sink the lexer found and checked, or one of
		// the reviewed reads. A bracket spelling, an Object.assign key, a
		// logical assignment or code a confused lexer swallowed shows up here.
		scriptReads := len(sinkReadRE.FindAllString(s, -1))
		reads += scriptReads
		if raw := sinkNameRE.FindAllStringIndex(s, -1); len(raw) != len(sinks)+scriptReads {
			var where []string
			for _, m := range raw {
				where = append(where, fmt.Sprintf("line %d %q", strings.Count(s[:m[0]], "\n")+1, s[m[0]:m[1]]))
			}
			t.Errorf("inline script %d mentions HTML sinks %d times (%s), but %d are checked writes and %d reviewed reads: write data with avxEl/textContent, never through another spelling (a comment or string that names a sink API counts too — reword it)",
				si, len(raw), strings.Join(where, "; "), len(sinks), scriptReads)
		}
		for _, sink := range sinks {
			examined++
			if !literalOnly(sink.value) {
				line := strings.Count(s[:sink.pos], "\n") + 1
				var parts []string
				for _, v := range sink.value {
					parts = append(parts, v.text)
				}
				t.Errorf("inline script %d, line %d: %s receives data (%s); build it with avxEl/textContent",
					si, line, sink.what, strings.Join(parts, " "))
			}
		}
	}
	// Guard the denominator: the scripts write status messages through
	// innerHTML today, so the scan must see them.
	if examined < 8 {
		t.Fatalf("examined %d HTML sinks; the scanner is not seeing the scripts", examined)
	}
	// The reviewed reads: the two search dropdowns re-open on focus when they
	// hold results (`x.innerHTML && …`). The count is exact: more or fewer fail.
	if reads != 2 {
		t.Fatalf("found %d `.innerHTML &&` reads; the reviewed count is 2 (the two search dropdowns)", reads)
	}
}

// sinkNameRE matches every mention of an HTML-writing API by name: dot,
// bracket, object key, attribute name, any .write/.writeln member (called,
// bound or .call'ed), and the accidental disguises a name can take — a string
// literal ending in HTML (a split key such as 'inner' + 'HTML'), a 'write'
// string, a \u escape touching an identifier (inner\u0048TML).
// Boundary, decided as a class after review rounds 1-3 of 0.29.92: the pin
// guards against API DATA reaching markup through code written in good
// faith. Deliberate obfuscation by the script's own author — \x, \u{…} or
// octal escapes inside a string, line continuations, names joined from three
// or more pieces, a name computed at run time from a variable — is out of
// scope; code review owns that.
var sinkNameRE = regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\s*\[|\.\s*write(?:ln)?\b|createContextualFragment|DOMParser|srcdoc|setHTMLUnsafe|parseHTMLUnsafe|setAttribute\(\s*['"](?:on|src|href)|['"` + "`" + `][A-Za-z]*HTML['"` + "`" + `]|['"` + "`" + `]write(?:ln)?['"` + "`" + `]|[A-Za-z_$.]\\u[0-9a-fA-F{]|\\u[0-9a-fA-F]{4}[A-Za-z_$]`)

// sinkReadRE matches the reviewed reads of innerHTML (truthiness checks),
// never the logical assignment &&= (a write).
var sinkReadRE = regexp.MustCompile(`\.innerHTML\s*&&[^=]`)

// The DOM builder is defined once, in the head every page includes (L18).
func TestTemplateDOMBuilderDefinedOnce(t *testing.T) {
	defs := regexp.MustCompile(`function avxEl\(`).FindAllStringIndex(allTemplates, -1)
	if len(defs) != 1 {
		t.Fatalf("avxEl defined %d times; want exactly once, in the head template", len(defs))
	}
	head := allTemplates[strings.Index(allTemplates, `{{define "head"}}`):]
	head = head[:strings.Index(head, "<body>{{end}}")] // the head's own {{end}}; {{if .Title}} has an earlier one
	if !strings.Contains(head, "function avxEl(") {
		t.Fatal("avxEl must live in the head template, which every page includes")
	}
}

// The scanner itself: each escape a reviewer could try is detected.
func TestTemplateSinkScannerSeesEverySpelling(t *testing.T) {
	cases := map[string]bool{ // source → want literal-only
		`a.innerHTML = '<p>x</p>';`:                                   true,
		`a.innerHTML = '<p>' + 'x' + '</p>';`:                         true,
		"a.innerHTML = `<p>x</p>`;":                                   true,
		`a.innerHTML = '<p>' + v + '</p>';`:                           false,
		"a.innerHTML = `<p>${v}</p>`;":                                false,
		"a.innerHTML =\n  '<p>' +\n  v.name;":                         false,
		`a.innerHTML += x;`:                                           false,
		`a.outerHTML = f(x);`:                                         false,
		`a.insertAdjacentHTML('beforeend', '<i>' + x);`:               false,
		`document.write(x);`:                                          false,
		`a.innerHTML = rows.map(r => '<td>' + r + '</td>').join('');`: false,
		`a.innerHTML = s.replace(/"/g, '&quot;');`:                    false,
		`x = /'/.test(y); a.innerHTML = '<b>ok</b>';`:                 true,
	}
	// Spellings the lexer does not parse as sinks must still be counted by
	// the raw-name check (W1, review of 0.29.92).
	for _, src := range []string{
		`el['innerHTML'] = x;`, `Object.assign(el, {innerHTML: x});`, `el.innerHTML ||= x;`, `el.innerHTML ??= x;`,
		`document['write'](x);`, `r.createContextualFragment(x);`, `new DOMParser().parseFromString(x, 't');`,
		`f.srcdoc = x;`, `el.setHTMLUnsafe(x);`, `el.setAttribute('onclick', x);`,
		`el.innerHTML &&= x;`, `el['inner' + 'HTML'] = x;`, "el.inner\\u0048TML = x;", `const d = document; d.write(x);`, `d['writeln'](x);`,
		`document.write.call(document, x);`, `document.write.bind(document)(x);`,
		"if (q) /'/.test(y); a.innerHTML = v; /'/.test(z);",
	} {
		raw := len(sinkNameRE.FindAllString(src, -1))
		checked := 0
		toks := lexJS(src)
		for _, s := range htmlSinks(toks) {
			if literalOnly(s.value) {
				checked++
			}
		}
		if raw == 0 || raw == checked {
			t.Errorf("%q: raw sink mentions %d, literal-only checked writes %d — the pin would not fail", src, raw, checked)
		}
	}
	for src, want := range cases {
		sinks := htmlSinks(lexJS(src))
		if len(sinks) != 1 {
			t.Errorf("%q: found %d sinks, want 1", src, len(sinks))
			continue
		}
		if got := literalOnly(sinks[0].value); got != want {
			t.Errorf("%q: literalOnly = %v, want %v", src, got, want)
		}
	}
}

// avxEl's own safety: it sets only the reviewed properties on an element
// and refuses the rest. A URL-bearing property (href, src, action) or an
// attribute setter would let a caller route data into a link without a
// scheme check, so the allowlist is pinned exactly.
func TestTemplateDOMBuilderPropertyAllowlist(t *testing.T) {
	i := strings.Index(allTemplates, "function avxEl(")
	if i < 0 {
		t.Fatal("avxEl not found")
	}
	body := allTemplates[i:]
	body = body[:strings.Index(body, "\n}\n")]
	if strings.Count(body, "e[k] = v") != 1 {
		t.Fatal("avxEl must assign element properties in exactly one, allowlisted branch")
	}
	line := body[strings.LastIndex(body[:strings.Index(body, "e[k] = v")], "\n"):strings.Index(body, "e[k] = v")]
	got := map[string]bool{}
	for _, m := range regexp.MustCompile(`k === '([A-Za-z]+)'`).FindAllStringSubmatch(line, -1) {
		got[m[1]] = true
	}
	want := map[string]bool{"className": true, "title": true, "type": true, "colSpan": true}
	if len(got) != len(want) {
		t.Fatalf("avxEl property allowlist = %v; want exactly %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("avxEl property allowlist = %v; want exactly %v", got, want)
		}
	}
	if !strings.Contains(body, "else throw new Error(") {
		t.Fatal("avxEl must refuse any other property")
	}
	for _, tag := range []string{"'script'", "'style'", "'iframe'", "'object'", "'embed'", "'link'", "'meta'", "'base'"} {
		if !strings.Contains(body, tag) {
			t.Fatalf("avxEl must refuse the %s tag", tag)
		}
	}
	for _, banned := range []string{"setAttribute", "innerHTML", "insertAdjacentHTML", ".href", ".src"} {
		if strings.Contains(body, banned) {
			t.Fatalf("avxEl must not use %s", banned)
		}
	}
}

// /compare?repos= reaches the page script as a list of ids; anything that
// is not a positive integer is dropped on the server (W3, review of 0.29.92).
func TestCompareRepoIDsKeepsOnlyPositiveIntegers(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"1,2,3":                 "1,2,3",
		" 4 , 5 ":               "4,5",
		"1,<img src=x>,2":       "1,2",
		"0,-3,7,abc,9999999999": "7,9999999999",
		"1,1,2":                 "1,2",
		"9223372036854775808,8": "8",
		"1,2,3,4,5,6,7":         "1,2,3,4,5",
	}
	for in, want := range cases {
		if got := compareRepoIDs(in); got != want {
			t.Errorf("compareRepoIDs(%q) = %q, want %q", in, got, want)
		}
	}
}
