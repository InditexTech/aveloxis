// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// web.session_idle_minutes (0.29.113, ASVS V7.3.1): one default layer —
// 0 and absent mean DefaultSessionIdleMinutes; a negative value or one
// past the session's absolute life is refused at load, naming the key.
func TestSessionIdleMinutesHasOneDefaultAndIsValidated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "aveloxis.json")
	for _, c := range []struct {
		body string
		want time.Duration
		ok   bool
	}{
		{`{}`, DefaultSessionIdleMinutes * time.Minute, true},
		{`{"web": {"session_idle_minutes": 0}}`, DefaultSessionIdleMinutes * time.Minute, true},
		{`{"web": {"session_idle_minutes": 1}}`, time.Minute, true},
		{`{"web": {"session_idle_minutes": 1440}}`, 24 * time.Hour, true},
		{`{"web": {"session_idle_minutes": -1}}`, 0, false},
		{`{"web": {"session_idle_minutes": 1441}}`, 0, false},
	} {
		if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(p)
		switch {
		case c.ok && (err != nil || cfg.Web.SessionIdle() != c.want):
			t.Errorf("%s: idle %v, %v; want %v", c.body, cfg.Web.SessionIdle(), err, c.want)
		case !c.ok && (err == nil || !strings.Contains(err.Error(), "web.session_idle_minutes")):
			t.Errorf("%s = %v; want a refusal naming web.session_idle_minutes", c.body, err)
		}
	}
	if DefaultSessionIdleMinutes != 30 {
		t.Errorf("DefaultSessionIdleMinutes = %d; want NIST SP 800-63B's AAL2 inactivity bound, 30", DefaultSessionIdleMinutes)
	}
}
