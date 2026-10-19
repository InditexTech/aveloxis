// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/aveloxis/aveloxis/internal/admin"
	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/httpserver"
)

// Forge-ID changes, admin only (v0.29.63; 2026-09-23 operator request: an
// Adopt button instead of the terminal).
//
//	GET  /api/v1/admin/forge-id-changes?pending=1
//	POST /api/v1/admin/forge-id-changes/{repoID}/adopt
//	     {"old_forge_id": "…", "new_forge_id": "…", "note": "…"}  (both IDs required; note optional)
//
// The org scan records a repository re-created upstream under the same URL
// as a PENDING change (observation-only). Adopting treats the new
// repository as a continuation: it moves the stored forge ID to the one
// the scan observed and records who adopted it, and the repository page
// shows the notice. The api process holds no forge API keys, so the button
// adopts the recorded observation; `aveloxis adopt-forge-id` asks the
// forge live.

func (s *Server) handleAdminForgeIDChanges(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	pendingOnly := r.URL.Query().Get("pending") == "1"
	changes, err := s.store.ListForgeIDChanges(r.Context(), pendingOnly)
	if err != nil {
		httpserver.LogFailure(r.Context(), s.logger, slog.LevelError, err, "admin forge-ID changes: list failed", "error", err)
		http.Error(w, "forge-ID changes could not be listed", http.StatusInternalServerError)
		return
	}
	if changes == nil {
		changes = []db.ForgeIDChange{}
	}
	s.writeJSON(w, r, map[string]any{"changes": changes})
}

// adoptNoteMaxBytes bounds the optional note (it is stored and shown to
// operators, not rendered on the public page).
const adoptNoteMaxBytes = admin.AdoptNoteMaxBytes

// adoptBodyMaxBytes bounds the raw request body. JSON may spell one byte
// of the note as a six-byte \uXXXX escape, so a note at the limit can take
// six times its size on the wire; the two forge IDs and the object's keys
// fit in the fixed allowance (a forge ID is a decimal integer, at most 19
// digits for an int64). The note's own limit is checked after decoding.
const adoptBodyMaxBytes = 6*adoptNoteMaxBytes + 256

func (s *Server) handleAdminForgeIDAdopt(w http.ResponseWriter, r *http.Request) {
	info, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	repoID, err := parseRepoID(r)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The body names the change the admin chose (review of v0.29.63:
	// adopting "the repository's newest pending change" adopted a pair the
	// admin had not clicked).
	var body struct {
		OldForgeID string `json:"old_forge_id"`
		NewForgeID string `json:"new_forge_id"`
		Note       string `json:"note"`
	}
	// 0.29.104 (review round 2): the package's one body reader, bounded
	// by adoptBodyMaxBytes (an io.ReadAll + json.Unmarshal of its own
	// before; a second spelling with no reason at the site).
	if err := decodeJSONBody(w, r, adoptBodyMaxBytes, &body); err != nil {
		writeAuthError(w, http.StatusBadRequest, `invalid JSON body: want {"old_forge_id", "new_forge_id", "note"}`)
		return
	}
	// Bounds, the adopter's label, the write and its audit line are the
	// service's; a refusal is answered from the API's one table.
	if err := s.admin.AdoptForgeID(r.Context(), s.actor(info), repoID, body.OldForgeID, body.NewForgeID, body.Note); err != nil {
		if !s.refuse(w, info, err) {
			s.serverError(w, r, "handleAdminForgeIDAdopt", fmt.Errorf("repo %d: %w", repoID, err))
		}
		return
	}
	s.writeJSON(w, r, map[string]any{"adopted": true, "repo_id": repoID})
}
