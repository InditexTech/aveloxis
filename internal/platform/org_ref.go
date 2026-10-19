// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package platform

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/aveloxis/aveloxis/internal/model"
)

// OrgRef is an organisation (GitHub) or group (GitLab) named by a web URL,
// classified for the deployment: which forge, on which host, and the path
// the forge enumerates it by.
type OrgRef struct {
	Platform model.Platform // GitHub or GitLab; never generic
	Host     string         // lower-cased, as written (a port stays)
	WebBase  string         // the instance's web base: the matched GitLab hint, or scheme://host
	// Path is what the forge's listing takes: GitHub's account name (one
	// segment), GitLab's FULL group path ("group/subgroup", N14) — a
	// subgroup registration enumerates the subgroup, never its parent
	// (eval-3's C4 blocker: the first segment alone listed and queued the
	// whole parent group).
	Path string
}

// ParseOrgRef classifies an organisation URL the way ParseRepoURLWithHints
// classifies a repository URL: a configured GitLab instance's web base
// (gitlabWebBases) wins, then the host decides (github.com and its
// subdomains; a host naming gitlab). A missing scheme is supplied
// (WithDefaultScheme); a URL carrying credentials is refused before
// url.Parse (whose error would quote it); a host of neither forge is
// ErrUnknownPlatform. GitLab's web spellings are accepted: a leading
// "groups/" segment is dropped, and anything from a "-" segment on
// ("/-/issues") is not part of the path.
func ParseOrgRef(raw string, gitlabWebBases []string) (OrgRef, error) {
	raw = WithDefaultScheme(strings.TrimSpace(raw))
	if raw == "" {
		return OrgRef{}, fmt.Errorf("%w: empty URL", ErrInvalidRepoURL)
	}
	if uerr := RefuseURLUserinfo(raw); uerr != nil {
		return OrgRef{}, fmt.Errorf("%w: %w", ErrInvalidRepoURL, uerr)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return OrgRef{}, fmt.Errorf("%w: %v", ErrInvalidRepoURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return OrgRef{}, fmt.Errorf("%w: scheme must be http or https, got %q", ErrInvalidRepoURL, u.Scheme)
	}
	if u.Host == "" {
		return OrgRef{}, fmt.Errorf("%w: missing host", ErrInvalidRepoURL)
	}
	ref := OrgRef{Host: strings.ToLower(u.Host)}
	path := u.Path
	if base, rest, ok := model.MatchInstanceWebBase(raw, gitlabWebBases); ok {
		ref.Platform, ref.WebBase, path = model.PlatformGitLab, base, rest
	} else {
		// The forge is decided on the canonical host (a default port, a
		// www. prefix name the same host; CanonicalWebHost), as the host
		// gate compares; Host itself stays as written.
		ref.Platform = detectPlatform(CanonicalWebHost(ref.Host))
		if ref.Platform == 0 {
			return OrgRef{}, fmt.Errorf("%w: host %q", ErrUnknownPlatform, ref.Host)
		}
		ref.WebBase, _ = model.NormalizeInstanceWebBase(u.Scheme + "://" + u.Host)
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	switch ref.Platform {
	case model.PlatformGitHub:
		if strings.EqualFold(segments[0], "orgs") { // GitHub's /orgs/<org>/… web spelling (the org page's own URL; paths are case-insensitive there); "orgs" alone names nothing
			segments = segments[1:]
		}
		if len(segments) == 0 || segments[0] == "" {
			return OrgRef{}, fmt.Errorf("%w: missing org name in path", ErrInvalidRepoURL)
		}
		ref.Path = segments[0]
	case model.PlatformGitLab:
		if segments[0] == "groups" { // GitLab's /groups/<path> web spelling; "groups" alone names nothing
			segments = segments[1:]
		}
		for i, seg := range segments {
			if seg == "-" {
				segments = segments[:i]
				break
			}
		}
		if len(segments) == 0 || segments[0] == "" {
			return OrgRef{}, fmt.Errorf("%w: missing group path", ErrInvalidRepoURL)
		}
		ref.Path = strings.Join(segments, "/")
	}
	return ref, nil
}

// OrgURLFor is the organisation URL a repository belongs to, on the
// repository's own host: "https://github.com/apache/arrow" →
// "https://github.com/apache", "https://gitlab.example/g/sub/p" →
// "https://gitlab.example/g/sub" (the full group path, N14). Empty for a
// generic git host (nothing enumerates it) or an owner-less parse. One
// spelling (CMD-12): the loader pointed every GitLab repository's group at
// gitlab.com, whatever its host.
func OrgURLFor(r RepoURL) string {
	if r.Owner == "" || r.WebBase == "" {
		return ""
	}
	switch r.Platform {
	case model.PlatformGitHub, model.PlatformGitLab:
		return r.WebBase + "/" + r.Owner
	}
	return ""
}
