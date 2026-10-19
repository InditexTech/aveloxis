// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package scheduler

// v0.29.57, round 1 on the Copilot review 5260961848 fixes: the web scan and
// the CLI gained a "is this org on the deployment's GitHub host" gate, but the
// two PERIODIC paths kept enumerating the org NAME on the configured base
// whatever host the registered URL named. On an Enterprise deployment an org
// registered as https://github.com/acme was enumerated as the Enterprise
// host's "acme" — a same-named org's repos linked into the user's group. The
// consumer enforces it too (SR-18; the rejected-group gate has the same
// belt-and-braces shape): a row whose host is not the deployment's is
// skipped, logged, and left unstamped.

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aveloxis/aveloxis/internal/config"
	"github.com/aveloxis/aveloxis/internal/db"
	"github.com/aveloxis/aveloxis/internal/platform"
)

func TestRefreshUserOrgsSkipsAnOrgOffTheConfiguredGitHubHost(t *testing.T) {
	fx, ctx := newOrgScanFixture(t)
	suffix := time.Now().UnixNano()
	home := fmt.Sprintf("_avhome%d", suffix)
	foreign := fmt.Sprintf("_avforeign%d", suffix)
	fx.orgRepos[home] = []string{"alpha"}
	fx.orgRepos[foreign] = []string{"beta"}
	fx.cleanupOrgRepos(t, ctx, home)
	fx.cleanupOrgRepos(t, ctx, foreign)

	u, g := fx.seedUserAndGroup(t, ctx, fmt.Sprintf("_avhost%d", suffix), "H", "approved")
	fx.registerOrg(t, ctx, u, g, home) // on the deployment's host
	fx.registerOrgURL(t, ctx, u, g, "https://github.com/"+foreign, foreign)

	fx.s.refreshUserOrgs(ctx, false)

	if n := fx.hitCount("/orgs/" + foreign + "/repos"); n != 0 {
		t.Errorf("an org registered on github.com was enumerated %d times on the configured Enterprise base — the name is not the org", n)
	}
	if n := fx.hitCount("/orgs/" + home + "/repos"); n == 0 {
		t.Error("the org on the deployment's own host was never enumerated")
	}
	if n := fx.linkCount(t, ctx, g, foreign); n != 0 {
		t.Errorf("repos linked from the foreign-host org = %d, want 0", n)
	}
	if n := fx.linkCount(t, ctx, g, home); n != 1 {
		t.Errorf("repos linked from the home-host org = %d, want 1", n)
	}
	var foreignStamped, homeStamped bool
	if err := fx.pool.QueryRow(ctx, `SELECT last_scanned IS NOT NULL FROM aveloxis_ops.user_org_requests WHERE group_id = $1 AND org_name = $2`, g, foreign).Scan(&foreignStamped); err != nil {
		t.Fatal(err)
	}
	if err := fx.pool.QueryRow(ctx, `SELECT last_scanned IS NOT NULL FROM aveloxis_ops.user_org_requests WHERE group_id = $1 AND org_name = $2`, g, home).Scan(&homeStamped); err != nil {
		t.Fatal(err)
	}
	if foreignStamped {
		t.Error("the skipped row was stamped as scanned (SR-3: a marker only over work proven done)")
	}
	if !homeStamped {
		t.Error("the enumerated row was not stamped")
	}
}

func TestRefreshGitHubOrgSkipsAnOrgOffTheConfiguredGitHubHost(t *testing.T) {
	fx, ctx := newOrgScanFixture(t)
	suffix := time.Now().UnixNano()
	home := fmt.Sprintf("_avlhome%d", suffix)
	foreign := fmt.Sprintf("_avlforeign%d", suffix)
	fx.orgRepos[home] = []string{"alpha"}
	fx.orgRepos[foreign] = []string{"beta"}
	fx.cleanupOrgRepos(t, ctx, home)
	fx.cleanupOrgRepos(t, ctx, foreign)

	if n := fx.s.refreshGitHubOrg(ctx, db.OrgGroup{Name: foreign, Type: "github_org", Website: "https://github.com/" + foreign}); n != 0 {
		t.Errorf("legacy repo_group on github.com yielded %d new repos from the configured Enterprise base", n)
	}
	if n := fx.hitCount("/orgs/" + foreign + "/repos"); n != 0 {
		t.Errorf("legacy repo_group on github.com was enumerated %d times on the configured base", n)
	}
	fx.s.refreshGitHubOrg(ctx, db.OrgGroup{Name: home, Type: "github_org", Website: "https://" + fx.webHost() + "/" + home})
	if n := fx.hitCount("/orgs/" + home + "/repos"); n == 0 {
		t.Error("legacy repo_group on the deployment's own host was never enumerated")
	}
}

// TestRefreshUserOrgsSkipsAnOrgWhoseURLCarriesCredentials (AVELOXIS_TEST_DB)
// — round 3 on the Copilot 5267x fixes: a user_org_requests row registered
// before the store refused credentialed URLs was still enumerated by name
// every pass (ParseOrgURL read the host past the userinfo). The parser
// refuses now, so the host gate skips it: not enumerated, nothing linked,
// unstamped (SR-3). (Its log line is redacted by the URL-keyed log pin,
// scripts/url_log_redaction_test.go, not asserted here.)
func TestRefreshUserOrgsSkipsAnOrgWhoseURLCarriesCredentials(t *testing.T) {
	fx, ctx := newOrgScanFixture(t)
	suffix := time.Now().UnixNano()
	home := fmt.Sprintf("_avhome%d", suffix)
	cred := fmt.Sprintf("_avcred%d", suffix)
	fx.orgRepos[home] = []string{"alpha"}
	fx.orgRepos[cred] = []string{"beta"}
	fx.cleanupOrgRepos(t, ctx, home)
	fx.cleanupOrgRepos(t, ctx, cred)

	u, g := fx.seedUserAndGroup(t, ctx, fmt.Sprintf("_avcredu%d", suffix), "C", "approved")
	fx.registerOrg(t, ctx, u, g, home)
	// Planted by INSERT: every writer refuses such a URL now.
	fx.registerOrgURL(t, ctx, u, g, "https://user:s3cret@"+fx.webHost()+"/"+cred, cred)

	fx.s.refreshUserOrgs(ctx, false)

	if n := fx.hitCount("/orgs/" + cred + "/repos"); n != 0 {
		t.Errorf("an org whose registered URL carries credentials was enumerated %d times", n)
	}
	if n := fx.hitCount("/orgs/" + home + "/repos"); n == 0 {
		t.Error("the clean org was never enumerated")
	}
	if n := fx.linkCount(t, ctx, g, cred); n != 0 {
		t.Errorf("repos linked from the credentialed org = %d, want 0", n)
	}
	var stamped bool
	if err := fx.pool.QueryRow(ctx, `SELECT last_scanned IS NOT NULL FROM aveloxis_ops.user_org_requests WHERE group_id = $1 AND org_name = $2`, g, cred).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if stamped {
		t.Error("the skipped row was stamped as scanned (SR-3)")
	}
	// Nor does it count as a never-scanned org that would re-fire the
	// demand scan every poll tick.
	if never, err := fx.s.store.HasNeverScannedOrgs(ctx, fx.s.ghAPIBase, ""); err != nil || never {
		t.Errorf("HasNeverScannedOrgs = %v, %v; a credentialed row must not count", never, err)
	}
}

// TestDemandProbeCountsNoGitHubRowWithoutAKey (0.29.106 whole-branch review
// P2): with a never-scanned GitHub registration and no usable GitHub key,
// the demand probe counts nothing (an empty base names no host), so the
// 10 s poll neither announces a scan nor runs one — the pass would only
// skip the row unstamped and the probe would fire again. The GitLab arm
// had this gate since stage 1e; the GitHub arm had it in the pass only.
func TestDemandProbeCountsNoGitHubRowWithoutAKey(t *testing.T) {
	fx, ctx := newOrgScanFixture(t)
	suffix := time.Now().UnixNano()
	org := fmt.Sprintf("_avnokey%d", suffix)
	fx.orgRepos[org] = []string{"alpha"}
	fx.cleanupOrgRepos(t, ctx, org)
	u, g := fx.seedUserAndGroup(t, ctx, fmt.Sprintf("_avnokeyu%d", suffix), "K", "approved")
	fx.registerOrg(t, ctx, u, g, org)

	if pending, err := fx.s.store.HasNeverScannedOrgs(ctx, platform.NoGitHubHost, ""); err != nil || pending {
		t.Errorf("the probe handed NoGitHubHost counts the row (pending=%v, %v); it must not, or the poll loops", pending, err)
	}
	if pending, err := fx.s.store.HasNeverScannedOrgs(ctx, fx.s.ghAPIBase, ""); err != nil || !pending {
		t.Errorf("the probe with the deployment's base must count the registration (pending=%v, %v)", pending, err)
	}
	// Through the scheduler: no GitHub key, one poll tick, nothing
	// announced and nothing enumerated.
	var logs lockedBuffer
	noKeys := NewWithKeys(fx.store, nil, nil, platform.NewKeyPool(nil, slog.New(slog.NewTextHandler(io.Discard, nil))), platform.NewKeyPool(nil, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(&logs, nil)), Config{Workers: 1, Collection: &config.CollectionConfig{}, GitHub: &config.PlatformConfig{BaseURL: fx.s.ghAPIBase}})
	noKeys.ghAPIBase = fx.s.ghAPIBase // the fixture's fake forge (the config accessor keeps only an https base)
	noKeys.dbHealthy.Store(true)
	noKeys.maybeScanNewOrgs(ctx)
	// The demand scan is launched under its single-flight flag, set before
	// the goroutine starts: the flag right after the call says whether the
	// poll launched one. (Then wait the goroutine out, so a launched scan
	// never outlives the test.)
	launched := noKeys.userOrgDemandActive.Load()
	for deadline := time.Now().Add(5 * time.Second); noKeys.userOrgDemandActive.Load() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if launched {
		t.Errorf("without a GitHub key the poll launched the demand scan it cannot run (announced every 10 s):\n%s", logs.String())
	}
	if n := fx.hitCount("/orgs/" + org + "/repos"); n != 0 {
		t.Errorf("without a GitHub key the org was enumerated %d times", n)
	}
	if got := logs.String(); strings.Contains(got, "new org registrations detected") || strings.Contains(got, "scanning user org requests") {
		t.Errorf("without a GitHub key the poll announced a scan it cannot run (this line repeats every 10 s):\n%s", got)
	}
}

// lockedBuffer is a log sink safe to read while a logging goroutine writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
