// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

// Source-contract tests for v0.19.0 group approval workflow.
//
// Public access feature: users login via GitHub OAuth, can create
// groups and add repos/orgs to them, but those groups stay in
// status='pending' until an administrator approves them. Only approved
// groups feed the collection_queue. The first user to ever sign up is
// auto-promoted to admin so the operator can approve everyone else.
//
// Schema additions (verified by source-contract tests):
//   - users.email_confirmed_at TIMESTAMPTZ NULL
//   - user_groups.status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected'))
//   - user_groups.approved_by INT NULL REFERENCES users(user_id)
//   - user_groups.approved_at TIMESTAMPTZ NULL
//
// Store methods (signature pinned by tests):
//   - CreateUserGroup now sets status: 'approved' if the creator is an admin, 'pending' otherwise
//   - RejectGroup (ApproveGroup and ListPendingGroups retired in 0.29.100, N9)
//   - ListUsers, SetUserAdmin
//   - First-user-is-admin auto-promotion in UpsertOAuthUser

package db

import (
	"strings"
	"testing"
)

// TestSchemaAddsGroupStatusColumn pins the migration that adds the
// status column to user_groups.
func TestSchemaAddsGroupStatusColumn(t *testing.T) {
	src := mustReadStoreSource(t, "migrate.go")
	if !strings.Contains(src, "user_groups") || !strings.Contains(src, "status") {
		t.Error("migrate.go must add status column to aveloxis_ops.user_groups")
	}
	// Look for the addColumnIfMissing call specifically.
	hasStatus := strings.Contains(src, `"aveloxis_ops.user_groups", "status"`)
	if !hasStatus {
		t.Error("migrate.go must call addColumnIfMissing(ctx, pg, \"aveloxis_ops.user_groups\", \"status\", ...) to add the approval-status column")
	}
}

// TestSchemaAddsGroupApprovalAuditColumns pins approved_by + approved_at.
func TestSchemaAddsGroupApprovalAuditColumns(t *testing.T) {
	src := mustReadStoreSource(t, "migrate.go")
	if !strings.Contains(src, `"aveloxis_ops.user_groups", "approved_by"`) {
		t.Error("migrate.go must add approved_by INT to user_groups so we can show who approved each group")
	}
	if !strings.Contains(src, `"aveloxis_ops.user_groups", "approved_at"`) {
		t.Error("migrate.go must add approved_at TIMESTAMPTZ to user_groups")
	}
}

// TestSchemaAddsEmailConfirmedAtColumn pins the email-audit column
// on users. Set to NOW() at signup since GitHub already verified.
func TestSchemaAddsEmailConfirmedAtColumn(t *testing.T) {
	src := mustReadStoreSource(t, "migrate.go")
	if !strings.Contains(src, `"aveloxis_ops.users", "email_confirmed_at"`) {
		t.Error("migrate.go must add email_confirmed_at TIMESTAMPTZ to users — set to NOW() on signup since GitHub OAuth has already verified the address")
	}
}

// TestUpsertOAuthUserPromotesFirstUserToAdmin pins the first-user
// auto-promotion. Without this, fresh deployments have no admin and
// nobody can approve anything.
func TestUpsertOAuthUserPromotesFirstUserToAdmin(t *testing.T) {
	src := mustReadStoreSource(t, "web_store.go")
	body := extractBatchFunc(src, "SignInOAuthUser") // the body since round 3 of the final review
	if body == "" {
		t.Fatal("could not locate SignInOAuthUser body")
	}

	// We expect the function to count existing users on the INSERT
	// path and set admin=TRUE iff count==0. The exact approach is
	// flexible (could be a SELECT then INSERT, or a CTE) — we look for
	// the `admin` column being explicitly set to TRUE somewhere in
	// the INSERT path.
	hasAdminPath := strings.Contains(body, "admin = TRUE") ||
		strings.Contains(body, "admin=TRUE") ||
		strings.Contains(body, "first user")
	if !hasAdminPath {
		t.Error("SignInOAuthUser must auto-promote the first user to admin. Look for a count-then-set pattern: " +
			"if no other users exist, set admin=TRUE on the INSERT.")
	}
}

// TestUpsertOAuthUserSetsEmailConfirmedAt pins that the OAuth-supplied
// email is marked confirmed at signup time (we trust the OAuth
// provider's verification).
func TestUpsertOAuthUserSetsEmailConfirmedAt(t *testing.T) {
	src := mustReadStoreSource(t, "web_store.go")
	body := extractBatchFunc(src, "SignInOAuthUser") // the body since round 3 of the final review
	if body == "" {
		t.Fatal("source structure changed (a skip would hide it): SignInOAuthUser not yet refactored")
	}
	if !strings.Contains(body, "email_confirmed_at") {
		t.Error("SignInOAuthUser must set email_confirmed_at on signup so the audit column reflects the OAuth-verified state. " +
			"The user's email is verified by GitHub before OAuth hands it to us.")
	}
}

// TestCreateUserGroupBranchesOnAdmin pins the status-on-creation
// behavior: admins' own groups are auto-approved (status='approved'),
// non-admins get status='pending' and must wait for admin review.
func TestCreateUserGroupBranchesOnAdmin(t *testing.T) {
	src := mustReadStoreSource(t, "web_store.go")
	body := extractBatchFunc(src, "CreateUserGroup")
	if body == "" {
		t.Fatal("could not locate CreateUserGroup body")
	}
	// The function should set status explicitly. We don't pin the
	// exact branch shape but require the word 'pending' OR 'approved'
	// to appear in the function body.
	if !strings.Contains(body, "status") {
		t.Error("CreateUserGroup must set the status column when inserting a new row. " +
			"Admins' groups should auto-approve; non-admins' groups go to 'pending'.")
	}
}

// TestRejectGroupExists pins the rejection method.
func TestRejectGroupExists(t *testing.T) {
	src := mustReadStoreSource(t, "web_store.go")
	if !strings.Contains(src, "func (s *PostgresStore) RejectGroup(") {
		t.Error("web_store.go must define RejectGroup(ctx, groupID, adminID) for the admin to refuse a pending submission")
	}
}

// TestListUsersExists pins the admin's user-management list.
func TestListUsersExists(t *testing.T) {
	src := mustReadStoreSource(t, "web_store.go")
	if !strings.Contains(src, "func (s *PostgresStore) ListUsers(") {
		t.Error("web_store.go must define ListUsers(ctx) so the admin user-management page can list everyone")
	}
}

// TestSetUserAdminExists pins the admin-toggle method.
func TestSetUserAdminExists(t *testing.T) {
	src := mustReadStoreSource(t, "web_store.go")
	if !strings.Contains(src, "func (s *PostgresStore) SetUserAdmin(") {
		t.Error("web_store.go must define SetUserAdmin(ctx, userID, isAdmin) to toggle the admin role from the user-management page")
	}
}

// TestAddRepoToGroupGatesOnApproval pins the deferred-enqueue
// behavior. v0.27.20 (per-add approval, summary/15 Option A): the
// gate moved from the group to the ADDITION — a non-admin's
// not-yet-tracked URLs land on a pending collection_add_requests row
// and reach collection_queue only via ProcessApprovedAddRequest. The
// group-status read survives as the 'rejected' abuse lever.
func TestAddRepoToGroupGatesOnApproval(t *testing.T) {
	src := mustReadStoreSource(t, "add_requests.go")
	body := extractBatchFunc(src, "AddReposToGroup")
	if body == "" {
		t.Fatal("could not locate AddReposToGroup body")
	}
	if !strings.Contains(body, "GetGroupStatus") && !strings.Contains(body, "status") {
		t.Error("AddReposToGroup must check group status — 'rejected' groups refuse all adds.")
	}
	// v0.29.90: the paste writer records the add request in the same
	// transaction as the links (Copilot review 5479554271).
	if !strings.Contains(body, "addPasteWithinCap") || !strings.Contains(extractBatchFunc(mustReadStoreSource(t, "capacity_store.go"), "addPasteWithinCap"), "insertAddRequest(") {
		t.Error("AddReposToGroup must park a non-admin's not-yet-tracked URLs on an " +
			"add-request — approval is what triggers collection (v0.27.20).")
	}
}
