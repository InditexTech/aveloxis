// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package admin

// The account e-mail flow (v0.19.10 fallback, v0.20.4 click-to-confirm):
// one place decides whether a confirmation link can be mailed and one
// classification of a token, for the web process's /account/email pages
// and the API's /me/email routes alike. Moved here from internal/web in
// stage 1 of summary/59 (eval-2 item 7) so the API no longer imports the
// web package; the request supplies only its Host and TLS state, as a
// LinkOrigin.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/httpserver"
	"github.com/aveloxis/aveloxis/internal/mailer"
	"github.com/aveloxis/aveloxis/internal/platform"
)

// LinkOrigin is what a request contributes to the confirmation link: its
// Host header and whether it arrived over TLS. The web process fills it
// from the request; the API passes the zero value (it never builds a link
// from a request). Host is attacker-controlled and is used ONLY under
// emailConfirmBase's loopback-and-dev-mode rule.
type LinkOrigin struct {
	Host string
	TLS  bool
}

// emailConfirmBase returns the base URL for the click-to-confirm link: the
// operator-configured site URL (which may carry a path; mailer.New has
// already trimmed it). The request Host header is attacker-controlled — a
// proxy that forwards Host (the usual nginx `proxy_set_header Host $host`)
// passes whatever the client sent — so deriving the link from it let an
// authenticated attacker submit a VICTIM's address with a crafted Host,
// have the victim receive a link to the attacker's server carrying the
// confirmation token, and replay that token to bind the victim's email to
// their own account (Copilot review on PR #207; CodeQL go/email-injection
// alert 197 traces the same request data into the message).
//
// So without a site URL the Host is used ONLY with web.dev_mode on AND when
// loopbackAuthority accepts it — the local-dev case that fallback existed
// for. dev_mode is required because a reverse proxy on the same host that
// does not forward Host (nginx's default for proxy_pass to 127.0.0.1) makes
// every visitor's request look loopback; the link would point at their own
// machine (round-4 review, operator decision, v0.29.30). Anywhere else this
// returns false and no link is mailed. It does not need the token, so the
// decision is made before anything is stored.
func emailConfirmBase(siteURL string, o LinkOrigin, devMode bool) (string, bool) {
	if siteURL != "" { // normalized once, by mailer.New
		return siteURL, true
	}
	if !devMode {
		return "", false
	}
	authority, ok := loopbackAuthority(o.Host)
	if !ok {
		return "", false
	}
	scheme := "https"
	if !o.TLS {
		scheme = "http"
	}
	return scheme + "://" + authority, true
}

// confirmationLink is the mailed link for a base from emailConfirmBase:
// the separate-repo front end's profile page when web.spa_url is set
// (it confirms through the API), this process's /account/email/confirm
// otherwise (2026-10-04). The token is hex, so it needs no escaping. On
// the front end it rides in the FRAGMENT: a fragment is never sent to the
// static server (so never in its access log), the front end's analytics
// tag excludes it, and the page moves it into the tab's session storage
// and drops it from the URL while its script loads, before any request
// is issued, so a signed-out click's login round trip carries a
// token-free ?next= (reviews 2026-10-04). spa_url is taken as written:
// config refuses a non-canonical value at load.
func confirmationLink(base, spaURL, token string) string {
	if spaURL != "" {
		return spaURL + "/profile.html#token=" + token
	}
	return base + "/account/email/confirm?token=" + token
}

// Why a confirmation link cannot be sent from this request.
var (
	errConfirmationMailDisabled = errors.New("mail is not configured, so no confirmation link can be sent")
	errConfirmationNoLinkBase   = errors.New("mail.site_url is not set — set it to this site's public URL to send confirmation links (web.dev_mode lets a loopback request build one for local development only; keep dev_mode off in production)")
)

// confirmationBaseFor decides whether this request's user can be mailed a
// working confirmation link, and returns the link base when so.
// SubmitAccountEmail refuses on an error; the dashboard's email gate uses
// the same decision so it never sends a user to a form that would refuse
// them (round-5 review: the two sites once took dev_mode separately, and
// a wrong value there brought back the v0.29.29 lockout with every test
// green — here both read the one Options).
func (s *Service) confirmationBaseFor(o LinkOrigin) (string, error) {
	if s.opts.Mailer == nil || !s.opts.Mailer.Enabled() {
		return "", errConfirmationMailDisabled
	}
	base, ok := emailConfirmBase(s.opts.Mailer.SiteURL(), o, s.opts.DevMode)
	if !ok {
		return "", errConfirmationNoLinkBase
	}
	return base, nil
}

// EmailGate reads the account's addresses and decides whether the
// dashboard must send the user to the e-mail form: they have neither a
// confirmed nor a pending address (v0.19.10; with a pending one the
// dashboard shows its "check your inbox" banner instead), AND a
// confirmation could actually be sent from here. Where it could not — mail
// off, or no trustworthy link base — the form refuses, so redirecting
// locked the user out of the dashboard; the operator chose to let them in
// without an address (v0.29.29). A lookup ERROR is not "no address"
// (SR-5): it is logged and the dashboard renders. pending is the pending
// address for the banner.
func (s *Service) EmailGate(ctx context.Context, a Actor, o LinkOrigin) (needsForm bool, pending string) {
	confirmed, err := s.st.GetUserEmail(ctx, a.UserID)
	if err != nil {
		httpserver.LogFailure(ctx, s.logger, slog.LevelWarn, err, "dashboard: could not read the user's email; rendering without the email-form redirect",
			"user_id", a.UserID, "error", err)
		return false, ""
	}
	pending, err = s.st.GetUserLivePendingEmail(ctx, a.UserID)
	if err != nil {
		httpserver.LogFailure(ctx, s.logger, slog.LevelWarn, err, "dashboard: could not read the user's pending email; rendering without the email-form redirect",
			"user_id", a.UserID, "error", err)
		return false, ""
	}
	if strings.TrimSpace(confirmed) != "" || strings.TrimSpace(pending) != "" {
		return false, pending
	}
	_, err = s.confirmationBaseFor(o)
	return err == nil, pending
}

// SubmitAccountEmail records rawEmail as the account's pending address and
// mails the click-to-confirm link, so the address becomes users.email only
// when the link is followed. It returns "" when the address was recorded
// and the link handed to SMTP, or the message the form should show.
//
// Nothing is stored until the submission is known to be mailable: the
// address passes the mailer's own recipient rule (so what is stored is the
// bare addr-spec Send will accept) and confirmationBaseFor allows it.
// Storing first left an email_pending behind on a refusal, and the
// dashboard then told the user a link had been sent. For the same reason a
// send that fails after storing clears that pending address again.
func (s *Service) SubmitAccountEmail(ctx context.Context, a Actor, login, rawEmail string, o LinkOrigin) string {
	email, err := mailer.ParseRecipient(rawEmail)
	if err != nil {
		s.logger.Info("account email rejected: not a deliverable address",
			"user_id", a.UserID, "error", platform.TruncateForLog([]byte(err.Error()), 200))
		return "Please enter a valid email address."
	}
	base, err := s.confirmationBaseFor(o)
	if err != nil {
		s.logger.Error("refusing an account email: "+err.Error(),
			"user_id", a.UserID, "host", platform.TruncateForLog([]byte(o.Host), 200))
		return "Email confirmation is not configured on this site. Contact the operator."
	}
	if err := s.st.SetUserPendingEmail(ctx, a.UserID, email); err != nil {
		httpserver.LogFailure(ctx, s.logger, slog.LevelWarn, err, "failed to set pending email", "user_id", a.UserID, "error", err)
		return "Could not save email. Try again."
	}
	token, err := s.st.CreateEmailConfirmation(ctx, a.UserID, email)
	if err != nil {
		httpserver.LogFailure(ctx, s.logger, slog.LevelWarn, err, "failed to create email confirmation", "user_id", a.UserID, "error", err)
		return "Could not generate confirmation. Try again."
	}
	if err := s.opts.Mailer.SendEmailConfirmation(email, login, confirmationLink(base, s.opts.SPAURL, token), db.EmailConfirmationLifetime); err != nil {
		if !mailer.IsSkip(err) { // a skip was already logged by the mailer
			// The send takes no context: its failure is never the
			// request's end (NET-6 review r6 F2), so no LogFailure.
			s.logger.Warn("failed to send confirmation email",
				"user_id", a.UserID, "email", platform.RedactEmail(email), "error", err)
		}
		// Don't leave the dashboard announcing "we sent a confirmation
		// link" (operator decision, v0.29.29) — but the mail may still have
		// gone out: net/smtp reports QUIT's error after the server has
		// accepted the message. So clear the pending address only while it
		// is still this submission's (a second tab may have replaced it,
		// v0.29.30), and tell the user a link that does arrive still works:
		// its token is in the database until it expires.
		//
		// The clean-up is the second half of writes that already committed,
		// so it runs detached from the request (NET-6 review r7 F1: an SMTP
		// dial can outlast nginx and the bound, and on the cancelled request
		// context the clean-up failed at once, leaving the dashboard
		// announcing a link that was never sent) and any failure is a WARN.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), pendingEmailCleanupTimeout)
		defer cleanupCancel()
		if clearErr := s.st.ClearUserPendingEmailIf(cleanupCtx, a.UserID, email); clearErr != nil {
			s.logger.Warn("failed to clear the pending email after a failed confirmation send",
				"user_id", a.UserID, "error", clearErr)
		}
		return "We couldn't send the confirmation email. If one does arrive, its link still works; otherwise try again later."
	}
	s.audit("account email submitted", a)
	return ""
}

// pendingEmailCleanupTimeout bounds SubmitAccountEmail's detached clean-up:
// one conditional UPDATE by the user's key, which takes milliseconds unless
// the pool or the database is stalled — the case the bound exists for. The
// house precedent for such a statement is 30 s (the deploy gate's
// deployGateDialTimeout, cmd/aveloxis).
const pendingEmailCleanupTimeout = 30 * time.Second

// loopbackAuthority parses an HTTP Host header ONCE and, when it names the
// loopback interface, returns it as a URL authority for an emailed link. A
// client-set Host may be trusted for that only when it is loopback: a
// loopback Host cannot reach anyone else's machine.
//
// The same parse feeds the decision and the link, and it is strict because
// its output is mailed:
//   - a port, when present, must be a canonical decimal in 1-65535 — an
//     earlier check ignored the port, so `localhost:<any text>` passed and
//     the text rode into the link;
//   - brackets must be balanced and may hold only an IPv6 literal;
//   - every IPv6 literal, IPv4-mapped included, comes back bracketed, as a
//     URL authority requires (RFC 3986 section 3.2.2). A bare "::1" is not
//     something a browser sends, but net/http accepts it.
func loopbackAuthority(hostHeader string) (string, bool) {
	host, port := hostHeader, ""
	bracketed := strings.HasPrefix(hostHeader, "[")
	if h, p, err := net.SplitHostPort(hostHeader); err == nil {
		if !isCanonicalPort(p) {
			return "", false
		}
		host, port = h, p
	} else if bracketed {
		// "[::1]" — SplitHostPort wants a port, so strip the brackets here.
		if !strings.HasSuffix(hostHeader, "]") {
			return "", false
		}
		host = hostHeader[1 : len(hostHeader)-1]
	}
	ip := net.ParseIP(host)
	ipv6Literal := ip != nil && strings.Contains(host, ":")
	switch {
	case bracketed && !ipv6Literal:
		return "", false
	case ip != nil && !ip.IsLoopback():
		return "", false
	case ip == nil && !strings.EqualFold(host, "localhost"):
		return "", false
	}
	if port != "" {
		return net.JoinHostPort(host, port), true
	}
	if ipv6Literal {
		return "[" + host + "]", true
	}
	return host, true
}

// isCanonicalPort reports whether p is a port in 1-65535 written as a
// canonical decimal: digits only, no sign, no leading zero.
func isCanonicalPort(p string) bool {
	n, err := strconv.ParseUint(p, 10, 16)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == p
}

// ConfirmOutcome is what following a confirmation link came to.
type ConfirmOutcome int

const (
	// ConfirmConfirmed: the address is this account's confirmed email now.
	ConfirmConfirmed ConfirmOutcome = iota
	// ConfirmInvalid: not a live token for THIS account — unknown, expired,
	// used, empty, or another account's (logged at WARN); nothing changed,
	// and the owner's link stays usable.
	ConfirmInvalid
	// ConfirmError: the database failed; the transaction rolled back, so
	// the link still works.
	ConfirmError
)

// ConfirmAccountEmail confirms the click-to-confirm link for the signed-in
// account, for the web page and the API alike: one classification of the
// token's errors, so the two surfaces never disagree.
func (s *Service) ConfirmAccountEmail(ctx context.Context, a Actor, token string) ConfirmOutcome {
	token = strings.TrimSpace(token)
	if token == "" {
		return ConfirmInvalid
	}
	if _, err := s.st.ConfirmEmailToken(ctx, token, a.UserID); err != nil {
		var mismatch *db.TokenOwnerMismatchError
		if errors.As(err, &mismatch) {
			// The replay emailConfirmBase exists to prevent: say so.
			s.logger.Warn("email confirmation link belongs to another account",
				"token_user_id", mismatch.OwnerID, "session_user_id", a.UserID)
			return ConfirmInvalid
		}
		if errors.Is(err, db.ErrConfirmationTokenInvalid) {
			s.logger.Info("email confirmation link rejected: unknown, expired, or already used",
				"session_user_id", a.UserID)
			return ConfirmInvalid
		}
		httpserver.LogFailure(ctx, s.logger, slog.LevelWarn, err, "failed to confirm user email", "user_id", a.UserID, "error", err)
		return ConfirmError
	}
	s.audit("account email confirmed", a)
	return ConfirmConfirmed
}
