// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"net/http"

	"github.com/aveloxis/aveloxis/internal/admin"
)

// The API's adapter over the admin service (stage 1 of summary/59): the
// caller's credential becomes an Actor, the process's caches are the
// service's hooks, and every refusal the service returns is answered from
// ONE table (refusalAnswer) through the one JSON error writer.

// actor is the service's view of a validated caller: an API token is an
// APIToken actor (never an administrator, whatever its owner's role), a
// bearer session a Session actor.
func (s *Server) actor(info authInfo) admin.Actor {
	if info.APITokenID != 0 {
		return admin.Actor{UserID: info.UserID, Kind: admin.APIToken}
	}
	return admin.Actor{UserID: info.UserID, Kind: admin.Session}
}

// apiHooks are this process's caches: the token-validation cache (scope
// and role) and the per-user home list.
type apiHooks struct{ s *Server }

func (h apiHooks) ScopeChanged(userIDs ...int) {
	for _, id := range userIDs {
		h.s.auth.invalidateUser(id)
		h.s.homeCache.invalidate(id)
	}
}

// RoleChanged drops the target's cached validations and home list: their
// own /me must reflect the new role at once, not after the TTL, and the
// role decides their scope (an administrator is unscoped). Only theirs: a
// role change touches no other account's cache (TestGlobalCacheFlushIsAdminOnly).
func (h apiHooks) RoleChanged(userID int) {
	h.s.auth.invalidateUser(userID)
	h.s.homeCache.invalidate(userID)
}

// refusalAnswer is the API's one table from a refusal kind to the status
// and body it answers. The words are presentation (eval-2 item 4); the
// kind is the service's. A group that is not the caller's and one that
// does not exist answer alike (N11): 404, nothing disclosed.
func refusalAnswer(ref admin.Refusal) (int, string) {
	detail := func(fallback string) string {
		if ref.Detail != "" {
			return ref.Detail
		}
		return fallback
	}
	switch ref.Kind {
	case admin.Forbidden:
		return http.StatusForbidden, "administrator access required"
	case admin.NotFound, admin.NotOwned:
		return http.StatusNotFound, detail("no group of yours with that id")
	case admin.RequestNotFound:
		return http.StatusNotFound, "no such add request"
	case admin.UserNotFound:
		return http.StatusNotFound, detail("no user with that id")
	case admin.GroupRejected:
		return http.StatusConflict, "the group has been rejected by an administrator"
	case admin.InvalidInput, admin.TooMany, admin.SelfDemotion, admin.OffHost:
		return http.StatusBadRequest, detail("invalid request")
	case admin.OrgUnregistrable, admin.LastAdmin, admin.Superseded:
		return http.StatusConflict, detail("refused")
	case admin.Partial:
		// The message holds counts only, no database text.
		return http.StatusInternalServerError, detail("some repositories could not be added") + "; try adding them again"
	}
	return http.StatusInternalServerError, "internal error; try again"
}

// refuse answers a service refusal: true when err was one and the
// response is written (a capacity refusal through refuseCapacity, every
// other kind through refusalAnswer and the JSON error writer); false when
// err is a failure the caller answers with serverError.
func (s *Server) refuse(w http.ResponseWriter, info authInfo, err error) bool {
	ref, ok := admin.Classify(err)
	if !ok {
		return false
	}
	if ref.Kind == admin.Capacity {
		return s.refuseCapacity(w, info, err)
	}
	code, msg := refusalAnswer(ref)
	writeAuthError(w, code, msg)
	return true
}
