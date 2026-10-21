// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestDeploymentRecipeRoutesTheGUIsAPIThroughTheWeb (Copilot on PR #241,
// 0.29.115): the built-in GUI's pages fetch /api/ on their own origin with
// no Authorization header; the web process's /api/ proxy attaches the
// signed-in session's Bearer (0.29.110). A recipe that sends the GUI
// host's /api/ straight to the api process therefore answers 401 to every
// browser outside exempt_cidrs once api.require_auth is true — which the
// same page's configuration sets. The GUI host's server block must not
// reach the api port; a direct-API host (Bearer clients) is a separate
// server block. The API guide must not advise the direct route either.
func TestDeploymentRecipeRoutesTheGUIsAPIThroughTheWeb(t *testing.T) {
	doc := srctest.Read(t, "docs/getting-started/deployment.md")
	const guiHost = "server_name aveloxis.example.org;"
	blocks := nginxServerBlocks(doc)
	if len(blocks) == 0 {
		t.Fatal("deployment.md carries no nginx server block")
	}
	found := false
	for _, b := range blocks {
		if !strings.Contains(b, guiHost) {
			continue
		}
		found = true
		// Every proxy_pass of the GUI host targets the web process — any
		// spelling of the api (localhost, another port) is refused, not
		// only the one literal (L10 round 1 of 0.29.115: a localhost:8383
		// location passed the literal check).
		targets := 0
		for _, line := range strings.Split(b, "\n") {
			f := strings.Fields(strings.TrimSpace(line))
			if len(f) < 2 || f[0] != "proxy_pass" {
				continue
			}
			targets++
			if target := strings.TrimSuffix(f[1], ";"); target != "http://127.0.0.1:8082" {
				t.Errorf("the GUI host proxies to %s; its every path, /api/ included, must go to the web process (http://127.0.0.1:8082), which attaches the session's Bearer", target)
			}
		}
		if targets == 0 {
			t.Errorf("the GUI host's server block has no proxy_pass:\n%s", b)
		}
	}
	if !found {
		t.Fatalf("no nginx server block names %q", guiHost)
	}
	if strings.Contains(doc, `"require_auth": true`) && !strings.Contains(doc, "api.aveloxis.example.org") {
		t.Error("the recipe sets require_auth true but documents no direct-API host for Bearer clients")
	}
	api := strings.Join(strings.Fields(srctest.Read(t, "docs/guide/api.md")), " ") // prose wraps: match across line breaks
	if strings.Contains(api, "send `/api/` to the API directly") {
		t.Error("api.md still advises sending the GUI host's /api/ to the API directly")
	}
}

// nginxServerBlocks returns each top-level `server { … }` block of the
// document's ```nginx fences, braces balanced.
func nginxServerBlocks(doc string) []string {
	var out []string
	for _, fence := range strings.Split(doc, "```nginx")[1:] {
		end := strings.Index(fence, "```")
		if end < 0 {
			continue
		}
		body := fence[:end]
		for {
			i := strings.Index(body, "server {")
			if i < 0 {
				break
			}
			depth, j := 0, i
			for ; j < len(body); j++ {
				if body[j] == '{' {
					depth++
				} else if body[j] == '}' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if j >= len(body) {
				break
			}
			out = append(out, body[i:j+1])
			body = body[j+1:]
		}
	}
	return out
}
