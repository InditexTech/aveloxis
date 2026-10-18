// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package web

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/aveloxis/aveloxis/internal/mailer"
)

// TestWithMailerWarnsWithoutSiteURL: a working mailer with no site_url
// refuses every confirmation link outside dev_mode, so startup says so and
// names the right fix (round-5 review: the only signal was an ERROR, at
// submit time, that read as "turn on dev_mode"). Kept here when the
// confirmation-link tests moved to internal/admin (stage 1 of summary/59,
// review round 1 F3: the WARN had lost its only test).
func TestWithMailerWarnsWithoutSiteURL(t *testing.T) {
	valid := mailer.Config{GmailUser: "ops@example.com", GmailAppPassword: "abcdefghijklmnop"}
	for _, tc := range []struct {
		name     string
		cfg      mailer.Config
		devMode  bool
		wantWarn bool
	}{
		{name: "mail on, no site_url, dev_mode off", cfg: valid, wantWarn: true},
		{name: "whitespace site_url counts as unset", cfg: mailer.Config{GmailUser: valid.GmailUser, GmailAppPassword: valid.GmailAppPassword, SiteURL: "  "}, wantWarn: true},
		{name: "site_url set", cfg: mailer.Config{GmailUser: valid.GmailUser, GmailAppPassword: valid.GmailAppPassword, SiteURL: "https://aveloxis.io"}},
		{name: "dev_mode on", cfg: valid, devMode: true},
		{name: "mail off", cfg: mailer.Config{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			s := &Server{logger: slog.New(slog.NewTextHandler(&logs, nil))}
			s.cfg.DevMode = tc.devMode
			s.WithMailer(mailer.New(tc.cfg, nil))
			warned := strings.Contains(logs.String(), "level=WARN") && strings.Contains(logs.String(), "set mail.site_url")
			if warned != tc.wantWarn {
				t.Errorf("startup WARN = %v, want %v; log:\n%s", warned, tc.wantWarn, logs.String())
			}
		})
	}
}
