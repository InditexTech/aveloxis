// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package db

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/platform"
)

// TestOrgApprovalRefusalAdviceCoversApprovedRequests — PR #218 fix review r2
// F6 and r3 F3: an org approved before a check existed (the index bound, the
// credentials refusal, v0.29.57's host gate) that lost its registration
// reaches these refusals on re-approve, and rejecting an approved row is a
// silent no-op. Every arm says what happens to an already-approved request.
func TestOrgApprovalRefusalAdviceCoversApprovedRequests(t *testing.T) {
	for _, err := range []error{
		ErrURLTooLong,
		platform.ErrURLUserinfo,
		ErrOrgOffGitHubHost,
		fmt.Errorf("register: %w", ErrOrgOffGitHubHost),
		ErrOrgOffGitLabInstance,
		fmt.Errorf("register: %w", ErrOrgOffGitLabInstance),
	} {
		advice := OrgApprovalRefusalAdvice(err, "https://api.github.com", "")
		if !strings.Contains(advice, "reject") || !strings.Contains(advice, "already approved") {
			t.Errorf("%v: advice %q must say to reject a pending request and what happens to an approved one", err, advice)
		}
	}
}

// TestOrgApprovalRefusalAdviceNamesTheGitLabInstance (round-3 review of
// 0.29.100): the GitLab twin of the host refusal names the configured
// instance, or says none is configured — never the GitHub host.
func TestOrgApprovalRefusalAdviceNamesTheGitLabInstance(t *testing.T) {
	with := OrgApprovalRefusalAdvice(ErrOrgOffGitLabInstance, "https://api.github.com", "https://code.example.invalid")
	if !strings.Contains(with, "GitLab instance (https://code.example.invalid)") || strings.Contains(with, "github.com") {
		t.Errorf("with an instance: %q", with)
	}
	without := OrgApprovalRefusalAdvice(ErrOrgOffGitLabInstance, "https://api.github.com", "")
	if !strings.Contains(without, "no GitLab instance is configured") || strings.Contains(without, "github.com") {
		t.Errorf("without an instance: %q", without)
	}
}
