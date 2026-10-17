// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package mailer

import (
	"strings"
	"testing"
)

// H-6 (summary/59 §5, the hotfix release): since v0.27.20 a group is
// approved when it is created and the approval unit is the ADDITION — a
// repository Aveloxis does not track yet, or an organization, waits for an
// administrator; a repository already tracked is linked at once. The welcome
// email still described the retired model ("groups … enter a pending state").
func TestWelcomeDescribesPerAdditionApproval(t *testing.T) {
	m, sent, _ := captureMailer(t)
	if err := m.SendWelcome("user@example.com", "alice", "GitHub"); err != nil {
		t.Fatalf("SendWelcome = %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("delivered %d messages, want 1", len(*sent))
	}
	_, body := readSent(t, (*sent)[0])
	flat := strings.Join(strings.Fields(body), " ")
	for _, stale := range []string{"pending state", "when your group is approved", "groups created by non-administrator"} {
		if strings.Contains(flat, stale) {
			t.Errorf("the welcome email still describes the retired group approval (%q):\n%s", stale, body)
		}
	}
	for _, want := range []string{"already tracks", "administrator", "organization"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the welcome email should explain per-addition approval (missing %q):\n%s", want, body)
		}
	}
}
