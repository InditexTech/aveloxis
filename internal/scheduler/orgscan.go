// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scheduler

import (
	"context"
	"errors"
	"net/url"

	"github.com/aveloxis/aveloxis/internal/capacity"
	"github.com/aveloxis/aveloxis/internal/model"
	"github.com/aveloxis/aveloxis/internal/platform"
	"github.com/aveloxis/aveloxis/internal/platform/github"
	"github.com/aveloxis/aveloxis/internal/platform/gitlab"
)

// The one way a listed repository enters the catalog (stage 1 of
// summary/59, eval-3 target 3): the legacy GitHub org refresh, the legacy
// GitLab group refresh and the user-org pass all list through the forge
// clients (platform/github ListOwnerRepos, platform/gitlab
// ListGroupProjects) and record what they list through ingestDiscovered.
// Before stage 1 the three passes, the web GUI and the CLI each carried
// their own copy of the sequence, with four error policies and three
// silent failure arms between them.

// ingestTargets names where a discovered repository goes: the legacy
// repo_groups row a NEW repository is recorded under (0: none — the
// user-org pass records none) and the user groups it is linked into.
type ingestTargets struct {
	legacyGroupID int64
	userGroupIDs  []int64
}

// ingestOutcome is what ingestDiscovered did: the repository's id, whether
// this call recorded and queued it, and the user groups whose link row this
// call inserted (a repository already linked is not "new" for that group —
// the v0.27.91 rule: 9.3M bogus "new" repositories in the Aug 7–16 2026
// run came from counting the no-op re-link).
type ingestOutcome struct {
	repoID  int64
	created bool
	linked  map[int64]bool
}

// ingestDiscovered records one listed repository in one sequence:
// FindRepoByURL → an existing row gets its forge id backfilled
// (SetPlatformRepoIDIfEmptySeen, fill-empty-only: the org-tracked cohort
// is the population at risk of rename re-discovery, v0.27.102) | a new row
// is recorded (UpsertRepo with the forge id, so the rename-heal inside it
// applies) and queued (EnqueueRepo; a failed enqueue is logged with the
// operator's repair — the next pass takes the existing-row branch, which
// never re-queues, so nothing revives a gone or renamed repository — and
// the repository is still linked) and a waiting mailing-list signal is
// resolved onto it (ResolveSignaledRepoForURL, §5c) → the repository is
// linked into every target user group (AddOrgRepoToGroupByID; the
// account's repository quota skips a group, logged once a day by the
// store). Every failure arm is logged; a cancellation is returned
// unlogged, and a lookup failure skips the item (SR-5: a failed
// FindRepoByURL is not "not tracked" — recording would mint a duplicate).
// Not one transaction: that would change lock scope on hot tables for no
// correctness gain.
func (s *Scheduler) ingestDiscovered(ctx context.Context, item platform.OwnerRepo, plat model.Platform, t ingestTargets) (ingestOutcome, error) {
	out := ingestOutcome{linked: map[int64]bool{}}
	repoID, err := s.store.FindRepoByURL(ctx, item.URL)
	if errors.Is(err, context.Canceled) {
		return out, err
	}
	if err != nil {
		s.logger.Warn("org scan: repository lookup failed — not recorded this pass", "url", platform.RedactURLUserinfo(item.URL), "error", err)
		return out, err
	}
	if repoID > 0 {
		out.repoID = repoID
		if item.ForgeID != "" {
			idErr := s.store.SetPlatformRepoIDIfEmptySeen(ctx, repoID, item.ForgeID, item.CreatedAt)
			if errors.Is(idErr, context.Canceled) {
				return out, idErr
			}
			if idErr != nil {
				s.logger.Warn("org scan: forge-ID backfill failed", "repo_id", repoID, "error", idErr)
			}
		}
	} else {
		repoID, err = s.store.UpsertRepo(ctx, &model.Repo{
			Platform:   plat,
			GitURL:     item.URL,
			Name:       item.Name,
			Owner:      item.Owner,
			GroupID:    t.legacyGroupID,
			PlatformID: item.ForgeID,
		})
		if errors.Is(err, context.Canceled) {
			return out, err
		}
		if err != nil {
			s.logger.Warn("org scan: could not record discovered repository", "url", platform.RedactURLUserinfo(item.URL), "error", err)
			return out, err
		}
		out.repoID, out.created = repoID, true
		if enqErr := s.store.EnqueueRepo(ctx, repoID, 100); errors.Is(enqErr, context.Canceled) {
			return out, enqErr
		} else if enqErr != nil {
			s.logger.Warn("org scan: could not queue discovered repository — it will not be collected until `aveloxis reconcile-repos` queues it", "repo_id", repoID, "url", platform.RedactURLUserinfo(item.URL), "error", enqErr)
		}
		s.logger.Info("new repo discovered", "repo", platform.RedactURLUserinfo(item.URL), "repo_id", repoID)
		if n, rerr := s.store.ResolveSignaledRepoForURL(ctx, repoID, item.URL); errors.Is(rerr, context.Canceled) {
			return out, rerr
		} else if rerr != nil {
			s.logger.Warn("org scan: signaled-repository resolution failed", "repo_id", repoID, "error", rerr)
		} else if n > 0 {
			s.logger.Info("resolved signaled_repo for new repo", "repo", platform.RedactURLUserinfo(item.URL), "messages", n)
		}
	}
	for _, gid := range t.userGroupIDs {
		inserted, lerr := s.store.AddOrgRepoToGroupByID(ctx, gid, repoID)
		if errors.Is(lerr, context.Canceled) {
			return out, lerr
		}
		if _, capped := capacity.AsExceeded(lerr); capped {
			continue // the account's repository quota (the store logs it once a day)
		}
		if lerr != nil {
			s.logger.Warn("failed to link discovered repo into user_repos", "repo_id", repoID, "group_id", gid, "error", lerr)
			continue
		}
		if inserted {
			out.linked[gid] = true
		}
	}
	return out, nil
}

// githubOrgClient is the client the org passes list through: the
// deployment's GitHub host and key pool, WITHOUT the collection client's
// rename hook (an org listing that answers 301 must not move a
// repository). nil when the scheduler has no GitHub keys.
func (s *Scheduler) githubOrgClient() *github.Client {
	if s.ghKeys == nil {
		return nil
	}
	s.ghOrgClientOnce.Do(func() { s.ghOrgClient = github.New(s.ghAPIBase, s.ghKeys, s.logger) })
	return s.ghOrgClient
}

// gitlabClientForWebURL returns a GitLab client for the instance a web URL
// (a group's website, an org registration) lives on, or ok=false with the
// reason: no GitLab keys, no usable host, or a host other than the
// configured instance's — GitLab keys are only ever sent to that instance
// (platform.GitLabAPIBaseForHost, v0.29.11). On the v0.30 rebase this body
// becomes the instances registry's lookup.
func (s *Scheduler) gitlabClientForWebURL(webURL string) (*gitlab.Client, bool, string) {
	if s.glKeys == nil || s.glKeys.IsEmpty() {
		return nil, false, "no GitLab API keys configured"
	}
	u, err := url.Parse(webURL)
	if err != nil || u.Host == "" {
		return nil, false, "the URL has no usable host"
	}
	configuredBase := ""
	if s.cfg.GitLab != nil {
		configuredBase = s.cfg.GitLab.BaseURL
	}
	apiBase, ok := platform.GitLabAPIBaseForHost(configuredBase, u.Host)
	if !ok {
		return nil, false, "the host is not the configured GitLab instance (gitlab.base_url), and GitLab API keys are only sent to that instance's host"
	}
	return gitlab.New(apiBase, s.glKeys, s.logger), true, ""
}
