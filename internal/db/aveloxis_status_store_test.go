// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"strings"
	"testing"
)

func TestSchemaDeclaresAveloxisStatus(t *testing.T) {
	src := readSchema(t)
	for _, needle := range []string{
		"CREATE TABLE IF NOT EXISTS aveloxis_ops.aveloxis_status",
		"status_name          TEXT PRIMARY KEY",
		"status               TEXT NOT NULL",
		"tool_source",
		"tool_version",
		"data_source",
		"data_collection_date",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("schema.sql must declare aveloxis_status with %q", needle)
		}
	}
}

// TestAveloxisStatusUpsert exercises set → get → re-set (one row per status_name).
func TestAveloxisStatusUpsert(t *testing.T) {
	store, ctx := emConnect(t)
	t.Cleanup(store.Close)
	clean := func() {
		store.pool.Exec(ctx, `DELETE FROM aveloxis_ops.aveloxis_status WHERE status_name='_av_test_sc'`)
	}
	clean()
	t.Cleanup(clean)

	if err := store.SetAveloxisStatus(ctx, "_av_test_sc", StatusBroken, "libmagic corrupt", "scancode preflight"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// The row as stored (the exported reader was deleted in 0.29.109, N28:
	// no page or command read it; the scancode preflight only writes).
	readStatus := func(name string) (status, detail, version string, found bool) {
		err := store.pool.QueryRow(ctx, `SELECT status, COALESCE(status_detail,''), COALESCE(tool_version,'')
			FROM aveloxis_ops.aveloxis_status WHERE status_name = $1`, name).Scan(&status, &detail, &version)
		return status, detail, version, err == nil
	}
	status, detail, version, found := readStatus("_av_test_sc")
	if !found {
		t.Fatal("the status row was not written")
	}
	if status != StatusBroken || detail != "libmagic corrupt" || version != ToolVersion {
		t.Errorf("round-trip wrong: %s %q %s (want tool_version=%s)", status, detail, version, ToolVersion)
	}
	// Upsert: same status_name overwrites, no duplicate row.
	if err := store.SetAveloxisStatus(ctx, "_av_test_sc", StatusOK, "", "scancode preflight"); err != nil {
		t.Fatalf("re-set: %v", err)
	}
	if status, _, _, _ := readStatus("_av_test_sc"); status != StatusOK {
		t.Errorf("upsert must overwrite status; got %q", status)
	}
	var n int
	store.pool.QueryRow(ctx, `SELECT count(*) FROM aveloxis_ops.aveloxis_status WHERE status_name='_av_test_sc'`).Scan(&n)
	if n != 1 {
		t.Errorf("must be exactly one row per status_name; got %d", n)
	}
	if _, _, _, found := readStatus("_av_never_recorded"); found {
		t.Error("an unrecorded subsystem must have no row")
	}
}
