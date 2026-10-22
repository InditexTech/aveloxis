// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// ladderDigest is the one digest of a deploy ladder: every step's command
// and description, in order, so a changed word anywhere in a ladder moves
// it. The digest is what the golden below freezes per version.
func ladderDigest(steps []deployStep) string {
	h := sha256.New()
	for _, s := range steps {
		fmt.Fprintf(h, "%d:%s\x00%d:%s\n", len(s.cmd), s.cmd, len(s.desc), s.desc)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The deploy ladders are frozen text (0.29.108, CMD-7 of summary/59 §5c):
// "historical checklists stay as the operator saw them". This golden holds
// one line per version in deployChecklists, `<version> <sha256>`, and the
// test fails when a version's ladder renders differently from its line.
// A NEW version adds its line (DEPLOY_LADDER_GOLDEN_WRITE=1 rewrites the
// file; review the diff: only the new line may appear). A version pruned
// from the map drops its line the same way. A line whose digest CHANGED
// is a historical ladder that was edited: write mode refuses to rewrite it
// unless the variable names that version (DEPLOY_LADDER_GOLDEN_WRITE=0.29.101,
// comma-separated for several), and the ledger says which ladder and why.
//
// The golden was first written from the ladders as the 25 copy-and-paste
// closures built them, BEFORE withStartNote replaced those closures, so it
// is also the proof that the replacement was byte-identical.
func TestDeployLaddersAreFrozen(t *testing.T) {
	path := filepath.Join(srctest.Root(t), "cmd", "aveloxis", "testdata", "deploy_ladder_digests.txt")
	current := map[string]string{}
	for version, steps := range deployChecklists {
		current[version] = ladderDigest(steps)
	}
	srctest.MinCount(t, "ladders digested", len(current), 60)

	if mode := os.Getenv("DEPLOY_LADDER_GOLDEN_WRITE"); mode != "" {
		// A historical digest changes only when the variable names its
		// version; adding and dropping lines needs no name.
		allowed := map[string]bool{}
		for _, v := range strings.Split(mode, ",") {
			allowed[strings.TrimSpace(v)] = true
		}
		for version, digest := range readLadderGolden(t, path) {
			if now, ok := current[version]; ok && now != digest && !allowed[version] {
				t.Fatalf("ladder %s renders differently from its golden line; a historical checklist is never regenerated silently — DEPLOY_LADDER_GOLDEN_WRITE=%s names it, with the ledger's reason", version, version)
			}
		}
		versions := make([]string, 0, len(current))
		for v := range current {
			versions = append(versions, v)
		}
		sort.Strings(versions)
		var b strings.Builder
		b.WriteString("# Deploy ladder digests: <version> <sha256 of every step's cmd and desc, in order>.\n")
		b.WriteString("# One line per deployChecklists entry. A changed digest is an edited historical\n")
		b.WriteString("# ladder — never regenerate without the ledger naming the ladder and the reason.\n")
		for _, v := range versions {
			fmt.Fprintf(&b, "%s %s\n", v, current[v])
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	golden := readLadderGolden(t, path)
	for version, digest := range current {
		want, ok := golden[version]
		if !ok {
			t.Errorf("ladder %s has no golden line — a new version adds one (DEPLOY_LADDER_GOLDEN_WRITE=1, then review the diff)", version)
			continue
		}
		if want != digest {
			t.Errorf("ladder %s renders differently from its golden digest: a historical checklist changed (operators saw the old text) — revert, or record the reason in the ledger before regenerating", version)
		}
	}
	for version := range golden {
		if _, ok := current[version]; !ok {
			t.Errorf("golden line for %s names a ladder deployChecklists no longer holds — drop the line", version)
		}
	}
}

// readLadderGolden parses the golden: `<version> <sha256 hex>` per line,
// comments and blanks skipped. A missing file is an empty golden in write
// mode and a fatal in check mode (the caller decides: write mode passes
// through an empty map, check mode fails on every version).
func readLadderGolden(t *testing.T, path string) map[string]string {
	t.Helper()
	golden := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) && os.Getenv("DEPLOY_LADDER_GOLDEN_WRITE") != "" {
			return golden
		}
		t.Fatalf("%v (DEPLOY_LADDER_GOLDEN_WRITE=1 writes it)", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		version, digest, ok := strings.Cut(line, " ")
		if !ok || len(digest) != sha256.Size*2 {
			t.Fatalf("golden line %q: want `<version> <sha256 hex>`", line)
		}
		if _, dup := golden[version]; dup {
			t.Fatalf("golden lists %s twice", version)
		}
		golden[version] = digest
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return golden
}
