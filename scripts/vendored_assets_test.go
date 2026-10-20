// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// TestVendoredChartJSIsTheRecordedBuild (0.29.102, B6/D-12): the web GUI
// serves its own copy of Chart.js instead of a CDN. The copy is the npm
// package's dist/chart.umd.js, verified against the registry's integrity
// at vendoring; this pins the file to the hash the vendor README records,
// so a silent edit or an unrecorded upgrade fails here.
func TestVendoredChartJSIsTheRecordedBuild(t *testing.T) {
	const recorded = "2812cb8825fdc57469eb2f7bb055e9429244e599920511ee477e828499b632cb"
	root := srctest.Root(t)
	b, err := os.ReadFile(filepath.Join(root, "internal", "static", "vendor", "chart.umd.js"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != recorded {
		t.Errorf("internal/static/vendor/chart.umd.js sha256 = %s, the recorded build is %s: record the new build (hash, version, registry integrity) in internal/static/vendor/README.md and here", got, recorded)
	}
	if !strings.HasPrefix(string(b), "/*!\n * Chart.js v4.4.7") {
		t.Errorf("the vendored file does not open with the Chart.js 4.4.7 banner: %.40q", string(b))
	}
	readme, err := os.ReadFile(filepath.Join(root, "internal", "static", "vendor", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), recorded) || !strings.Contains(string(readme), "chart.js@4.4.7") {
		t.Error("internal/static/vendor/README.md does not record the vendored build's version and hash")
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "static", "vendor", "chart.js-LICENSE.md")); err != nil {
		t.Error("the vendored library's licence file is missing")
	}
}
