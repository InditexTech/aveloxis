// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/srctest"
)

// N28 of summary/59 (the last two, decided by stage 3's page list, 0.29.109):
// GetAveloxisStatus and GetContributorLoginHistory were exported readers no
// page or command called. They stay deleted; the data they read is written
// as before (SetAveloxisStatus, recordLoginObservation) and read by SQL.
func TestN28ReadersStayDeleted(t *testing.T) {
	src := srctest.StripGoComments(srctest.PackageSource(t, "internal/db", 40))
	for _, gone := range []string{"func (s *PostgresStore) GetAveloxisStatus(", "func (s *PostgresStore) GetContributorLoginHistory(", "type LoginHistoryEntry struct"} {
		if strings.Contains(src, gone) {
			t.Errorf("%s is back; it had no caller (N28) — give it one in the same change or leave it out", gone)
		}
	}
	for _, kept := range []string{"func (s *PostgresStore) SetAveloxisStatus(", "func recordLoginObservation("} {
		if !strings.Contains(src, kept) {
			t.Errorf("%s is gone; the writers stay", kept)
		}
	}
}
