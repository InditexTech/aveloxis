// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"io"
	"log/slog"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// packageSourceStripped is this package's non-test source, comments
// removed, for the denominator pins.
func packageSourceStripped(t *testing.T) string {
	t.Helper()
	return srctest.StripGoComments(srctest.PackageSource(t, "internal/admin", 6))
}
