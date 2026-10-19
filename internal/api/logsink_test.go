// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
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

var (
	sinkHandedToHandler = regexp.MustCompile(`New(?:Text|JSON)Handler\(&(\w+)\b`)
	// A call of this package's New or NewWithOptions (not slog.New, errors.New
	// or httptest.NewRequest: a dot or a word character before the name), or
	// a Server literal wired with the home loader, whose route spawns
	// refreshHomeRepos.
	serverConstructed = regexp.MustCompile(`(?:^|[^\w.])New(?:WithOptions)?\(|homeLoader\s*[:=]`)
	bareBuffer        = regexp.MustCompile(`\b(\w+) (?::= &?|:= new\(|)(?:strings\.Builder|bytes\.Buffer)\b|var (\w+) (?:strings\.Builder|bytes\.Buffer)\b`)
)

// TestServerLogSinksAreLocked (0.29.105, the integration job on PR #238): a
// server a test builds owns goroutines that log past the request that
// started them — the capacity policy's refresh (capacity_limits.go, from the
// rate-limit middleware of any server built with New or NewWithOptions over
// a store) and the home-repos refresh (portal.go, from a Server wired with
// a home loader). Four tests handed such a server a bare strings.Builder as
// its log sink and reset or read it from the test goroutine: a data race,
// intermittent on CI under -race (slog's handler locks only its own Write;
// a Builder read from another goroutine is unsynchronised).
// The rule is on the declaration, not on how the pointer reaches the
// handler (review round 1: `w := &logs` would have slipped a sink past a
// check of the NewTextHandler argument): a function that constructs a
// server declares no bare strings.Builder or bytes.Buffer at all; a sink
// there is a lockedBuffer. Residual, by design: a logger or server built
// in one function and handed to another (the re-warm harness builds a bare
// literal and reads its sink only after the goroutine is done).
func TestServerLogSinksAreLocked(t *testing.T) {
	root := srctest.Root(t)
	dir := filepath.Join(root, "internal", "api")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, constructing, sinks := 0, 0, 0
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") || !srctest.CompiledTestFile(t, root, path) {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files++
		file := srctest.StripGoComments(string(src))
		for name, body := range srctest.FuncParts(t, file) {
			if name == srctest.PackageLevel || !serverConstructed.MatchString(body) {
				continue
			}
			constructing++
			sinks += len(sinkHandedToHandler.FindAllString(body, -1))
			for _, m := range bareBuffer.FindAllStringSubmatch(body, -1) {
				t.Errorf("%s: %s constructs a server and declares a bare buffer %s%s; the server's goroutines (the capacity refresh, the home-repos refresh) log past the request — a sink here is a lockedBuffer", path, name, m[1], m[2])
			}
		}
	}
	srctest.MinCount(t, "test files read in internal/api", files, 40)
	srctest.MinCount(t, "test functions that construct a server", constructing, 30)
	srctest.MinCount(t, "log sinks handed to a constructed server", sinks, 5)
}
