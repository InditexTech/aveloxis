// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import "time"

// OwnerRepo is one repository of an organization, user or group listing —
// the item the forge listers yield and the scheduler's one ingest takes
// (stage 1 of summary/59, eval-3 target 1). URL is the forge's web URL of
// the repository (what the catalog stores), Owner the account or group path
// that owns it, ForgeID the forge's numeric id as text (model.ForgeIDString,
// rename-proof), CreatedAt when the forge created it (dates a forge-ID
// change the scan records; zero when the forge did not say).
type OwnerRepo struct {
	URL       string
	Owner     string
	Name      string
	ForgeID   string
	CreatedAt time.Time
}
