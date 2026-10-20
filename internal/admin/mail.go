// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

import (
	"time"

	"github.com/aveloxis/aveloxis/internal/mailer"
	"github.com/aveloxis/aveloxis/internal/safego"
)

// Mailer is what the Service sends through: *mailer.Mailer, or a fake in
// tests. Nil in Options means mail is not configured here.
type Mailer interface {
	Enabled() bool
	SiteURL() string
	OperatorEmail() string
	SendEmailConfirmation(toEmail, login, confirmURL string, lifetime time.Duration) error
	SendAddRequestSubmitted(to, requesterLogin, groupName, kind string, count int, sample []string, requestID int64) error
	SendAddRequestDecided(toEmail, login, groupName, kind string, approved bool, count int) error
}

var _ Mailer = (*mailer.Mailer)(nil)

// send is the one best-effort sender: the mail goes out in its own
// goroutine (a decision must not wait on SMTP), a skip the mailer already
// logged is not reported again (mailer.IsSkip), and any other failure is
// one WARN carrying attrs. Nothing is sent without a mailer.
func (s *Service) send(name string, attrs []any, fn func(m Mailer) error) {
	m := s.opts.Mailer
	if m == nil {
		return
	}
	safego.Go(s.logger, name, func() {
		if err := fn(m); err != nil && !mailer.IsSkip(err) {
			s.logger.Warn("failed to send "+name, append(attrs, "error", err)...)
		}
	})
}
