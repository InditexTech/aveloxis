// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadRefusesAnInvalidMailBlock (N7, stage 1f of summary/59): an
// invalid mail block fails Load, naming the field, as every other invalid
// value does (v0.29.61's rule) — mailer.New used to WARN and run with mail
// off, so a mistyped App Password surfaced at the first refused
// confirmation link. An empty block (mail off) and a complete one load.
// End to end: the JSON file → Load (SR-10).
func TestLoadRefusesAnInvalidMailBlock(t *testing.T) {
	for _, tc := range []struct {
		name string
		mail string
		want string // a substring of the error; "" = loads
	}{
		{"empty block (mail off)", `{}`, ""},
		{"complete block", `{"gmail_user": "ops@example.com", "gmail_app_password": "abcd efgh ijkl mnop", "site_url": "https://aveloxis.example"}`, ""},
		{"password without a user", `{"gmail_app_password": "abcdefghijklmnop"}`, "mail.gmail_user is empty"},
		{"user without a password", `{"gmail_user": "ops@example.com"}`, "mail.gmail_app_password is empty"},
		{"user is not an address", `{"gmail_user": "aveloxis.io", "gmail_app_password": "abcdefghijklmnop"}`, "not an email address"},
		{"not an App Password", `{"gmail_user": "ops@example.com", "gmail_app_password": "hunter2"}`, "App Password"},
		{"bad site_url", `{"gmail_user": "ops@example.com", "gmail_app_password": "abcdefghijklmnop", "site_url": "aveloxis.example"}`, "site_url"},
		// A set site_url is checked with the Gmail pair empty too (round-2
		// review of 0.29.100: the docs promised it; it loaded).
		{"site_url only, not a URL", `{"site_url": "aveloxis.example"}`, "site_url"},
		{"site_url only, a URL", `{"site_url": "https://aveloxis.example"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "aveloxis.json")
			if err := os.WriteFile(path, []byte(`{"database": {"host": "localhost"}, "mail": `+tc.mail+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Load = %v; want the block accepted", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "invalid config: mail: ") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %v; want an invalid-config error naming the field (%q)", err, tc.want)
			}
		})
	}
}

// TestMailerConfigIsTheOneDerivation: cmd builds every mailer from
// Config.MailerConfig, the value Load validated.
func TestMailerConfigIsTheOneDerivation(t *testing.T) {
	c := &Config{}
	c.Mail.GmailUser, c.Mail.GmailAppPassword, c.Mail.FromName, c.Mail.SiteURL, c.Mail.OperatorEmail = "u@example.com", "abcdefghijklmnop", "Ops", "https://x.example", "ops@example.com"
	c.Web.SPAURL = "https://gui.example"
	m := c.MailerConfig()
	if m.GmailUser != "u@example.com" || m.GmailAppPassword != "abcdefghijklmnop" || m.FromName != "Ops" || m.SiteURL != "https://x.example" || m.OperatorEmail != "ops@example.com" || m.SPAURL != "https://gui.example" {
		t.Errorf("MailerConfig = %+v", m)
	}
}

// TestLoadRefusesAnUnusableGitLabBase (round-4 review of 0.29.100): a
// gitlab.base_url from which no instance web base derives would build
// keyed clients from one string while every gate and classifier read "no
// instance" — refused at load like any other invalid value; an explicit
// empty value is "no instance", and the default needs no key at all.
func TestLoadRefusesAnUnusableGitLabBase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gitlab string
		want   string // a substring of the error; "" = loads
		base   string // the effective base when it loads
	}{
		{"absent block keeps the default", `{}`, "", "https://gitlab.com/api/v4"},
		{"explicit empty is no instance", `{"base_url": ""}`, "", ""},
		{"self-hosted", `{"base_url": "https://gitlab.example.org/api/v4/"}`, "", "https://gitlab.example.org/api/v4/"},
		{"no scheme", `{"base_url": "gitlab.example.org/api/v4"}`, "gitlab.base_url", ""},
		{"a query", `{"base_url": "https://gitlab.example.org/api/v4?x=1"}`, "gitlab.base_url", ""},
		{"credentials", `{"base_url": "https://u:p@gitlab.example.org/api/v4"}`, "gitlab.base_url", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "aveloxis.json")
			if err := os.WriteFile(path, []byte(`{"database": {"host": "localhost"}, "gitlab": `+tc.gitlab+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.want == "" {
				if err != nil || cfg.GitLab.BaseURL != tc.base {
					t.Fatalf("Load = %v, base %q; want the block accepted with base %q", err, cfg.GitLab.BaseURL, tc.base)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "invalid config: ") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %v; want an invalid-config error naming %s", err, tc.want)
			}
			// The value is echoed redacted: a pasted token must never reach
			// the log the refusal lands in (round-5 review).
			if strings.Contains(err.Error(), "u:p@") || strings.Contains(err.Error(), ":p@") {
				t.Errorf("the refusal echoes the URL's credentials: %v", err)
			}
		})
	}
}
