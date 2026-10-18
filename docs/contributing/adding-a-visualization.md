<!--
SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
SPDX-License-Identifier: MIT
-->

# Adding a visualization

The aveloxis web GUI uses [Chart.js](https://www.chartjs.org/), vendored in `internal/static/vendor` and served by the web process at `/static/vendor/chart.umd.js` (no CDN, no build step). Since 0.29.102 a page carries NO inline script: the HTML is a template in `internal/web/templates/`, its script is a file in `internal/static/` served at `/static/<name>.js`, and the page hands the script its ids through `data-*` attributes. The Content-Security-Policy names the web process as the only script source, so an inline `<script>` body, an `on*=` attribute or a CDN `<script src>` does not run (and `TestTemplatesCarryNoInlineScript` fails the build).

This chapter walks through adding a new chart to either the repo detail page or the comparison page.

## How visualizations work today

Two pages currently host charts:

- **Repo detail page** (`/groups/{gid}/repos/{rid}`): four overlaid line charts (commits, PRs opened, PRs merged, issues), all weekly time-series.
- **Comparison page** (`/compare?repos=1,2,3`): same four chart series, but for up to 5 repos overlaid, with three display modes (Raw / 100% normalized / Z-Score).

Both pull data from the REST API on `:8383`. If the API isn't running, the page renders a "REST API not available" fallback.

The pattern, end to end:

```
1. Browser loads HTML rendered from internal/web/templates/<page>.html
2. The page's <script src="/static/<page>.js"> reads its ids from a data-* element
   and fires fetch("/api/v1/repos/<id>/timeseries?...") (same origin: the web
   process reverse-proxies /api/ to the api process)
3. JSON response parsed → Chart.js renders into a <canvas>
```

There's no Vue, no React, no client-side state management. Plain JS, plain templates. Resist adding a framework.

## Walkthrough: add a "contributor activity" chart

We'll add a chart showing weekly contributor count to the repo detail page. The REST endpoint is hypothetical — see [`adding-a-rest-endpoint.md`](adding-a-rest-endpoint.md) for the API half.

### Step 1 — make sure the data is available

Add a REST endpoint that returns the weekly contributor count time-series:

```
GET /api/v1/repos/{repoID}/contributor-timeseries?since=2025-01-01
→ {
  "repo_id": 1,
  "weekly_counts": [
    {"week": "2025-W01", "active_contributors": 5},
    {"week": "2025-W02", "active_contributors": 7},
    ...
  ]
}
```

Implement the endpoint following [`adding-a-rest-endpoint.md`](adding-a-rest-endpoint.md). The store-side SQL aggregates `commits` by `date_trunc('week', cmt_author_timestamp)` and `cmt_ght_author_id`.

### Step 2 — find the right template and its script

`internal/web/templates/repo_detail.html` is the repository page; its script is `internal/static/repo-detail.js`, loaded at the end of the page after `<div id="repo-page" data-repo-id="{{.RepoID}}" hidden></div>` (the id the script reads) and after `/static/vendor/chart.umd.js`. The page's shape:

```html
{{define "repo_detail"}}
{{template "head" (dict "Title" .Repo.Name)}}
<script src="/static/vendor/chart.umd.js"></script>
…
<div class="chart-row">
  <canvas id="chart-commits"></canvas>
  <canvas id="chart-prs-opened"></canvas>
</div>
…
<div id="repo-page" data-repo-id="{{.RepoID}}" hidden></div>
<script src="/static/repo-detail.js"></script>
</body></html>
{{end}}
```

and the script's head:

```js
const API_BASE = '';
const REPO_ID = Number(document.getElementById('repo-page').dataset.repoId);

function makeChart(canvasId, label, color, data) { /* the existing factory */ }

fetch(API_BASE + '/api/v1/repos/' + REPO_ID + '/timeseries')
  .then(r => r.json())
  .then(ts => {
    makeChart('chart-commits', 'Commits / week', COLORS.commits, ts.commits || []);
    // …
  })
  .catch(err => console.error('Failed to load time series:', err));
```

A control the script reacts to binds with `addEventListener` on a `data-*` attribute (`th[data-sort-col]`, `.preset-btn[data-preset]`), never an `onclick=`. Anything the script puts on the page from an API answer goes through `avxEl` / `textContent` (the H-1 rule; `TestTemplateScriptsWriteOnlyLiteralsIntoHTML` scans the page scripts).

### Step 3 — add the canvas + the fetch

```html
<!-- Add to the chart-row divs: -->
<div class="chart-row">
  <canvas id="chart-contributors"></canvas>
</div>
```

```js
// Add to COLORS:
const COLORS = {
  commits: '#1f77b4',
  prsOpened: '#2ca02c',
  prsMerged: '#9467bd',
  issues: '#d62728',
  contributors: '#ff7f0e',  // new (matplotlib tab:orange)
};

// Add a second fetch (parallel to the existing one is fine — both fire when the page loads):
fetch(API_BASE + '/api/v1/repos/' + REPO_ID + '/contributor-timeseries?since=2024-01-01')
  .then(r => r.json())
  .then(ts => {
    makeChart('chart-contributors', 'Active contributors / week',
              COLORS.contributors, ts.weekly_counts || []);
  })
  .catch(err => console.error('contributor chart load failed', err));
```

The `makeChart` factory expects an array of `{x: 'YYYY-WW', y: N}` objects — make sure your API response shape matches OR adapt the factory.

### Step 4 — handle the data-shape mismatch

The existing `makeChart` factory probably expects a specific shape. Read its implementation:

```js
function makeChart(canvasId, label, color, data) {
  new Chart(document.getElementById(canvasId), {
    type: 'line',
    data: {
      labels: data.map(d => d.week),    // expects {week: '2025-W01', ...}
      datasets: [{
        label: label,
        data: data.map(d => d.count),   // expects {count: N}
        borderColor: color,
        // ...
      }],
    },
    options: { /* ... */ },
  });
}
```

If your API returns `active_contributors` instead of `count`, either:

- **Adapt the response shape** in your REST endpoint to use `count`. Cleanest.
- **Map at fetch time**: `.then(ts => makeChart(..., ts.weekly_counts.map(d => ({week: d.week, count: d.active_contributors}))))`. Smaller change but the mapping clutters the template.

Preference: API returns the shape the factory expects. The factory is consumed by every chart; matching it keeps the templates simple.

## Patterns to follow

### Color palette

The existing charts use matplotlib's "tab10" palette:

| Series | Hex | matplotlib name |
|---|---|---|
| Commits | `#1f77b4` | tab:blue |
| PRs opened | `#2ca02c` | tab:green |
| PRs merged | `#9467bd` | tab:purple |
| Issues | `#d62728` | tab:red |
| Contributors (new) | `#ff7f0e` | tab:orange |

Stay in the palette. Avoid colors that don't differ enough for colorblind users.

### Chart options

Use the existing `makeChart` defaults unless you have a specific reason to deviate. Common deviations:

- `type: 'bar'` instead of `'line'` for categorical comparisons.
- `stacked: true` on the y-axis for cumulative views.
- `scales.y.beginAtZero: true` for counts (don't truncate; it lies about magnitude).

### Comparison page differences

If your chart should also appear on `/compare`, that template is separate. The comparison page wraps `makeChart` with a `renderComparisonChart` that handles multiple data series + the mode toggle (Raw / 100% / Z-Score). Search for `renderComparisonChart` in the template file.

The contract: each repo's data is a separate dataset in the same chart. The mode toggle is a UI control above the charts that re-renders with normalized values. Implement the normalization in JS.

### Time zones

API responses use ISO week numbers (`YYYY-WW`) — already timezone-agnostic. Don't return raw timestamps that the browser interprets in local time; the charts get misaligned across users in different zones.

### Empty data

A repo with no data yet (newly added, collection in progress) should render an empty chart with a message, not a blank canvas. The existing pattern:

```js
if (!data || data.length === 0) {
  document.getElementById(canvasId).parentElement.innerHTML =
    '<div class="chart-empty">No data yet — collection may still be in progress</div>';
  return;
}
```

Add `.chart-empty` CSS for the styling.

## What NOT to do

- **Don't add a frontend framework.** No React, no Vue, no Svelte, no Alpine. The current setup builds in milliseconds and has zero deploy complexity. Resist.
- **Don't put a script body in a template.** The page scripts are files in `internal/static/` served at `/static/`; an inline `<script>` body or an `on*=` attribute is blocked by the Content-Security-Policy and fails `TestTemplatesCarryNoInlineScript`.
- **Don't hardcode the API base URL.** The scripts fetch relative `/api/v1/...` URLs; the web process reverse-proxies them to the api process (`web.api_internal_url`), so the page works behind nginx and TLS with no CORS.
- **Don't skip the "no data" empty state.** A blank canvas is a worse UX than an explicit message.
- **Don't load Chart.js per chart.** It's already loaded once at the top of each template; just reuse.
- **Don't load anything from a CDN.** Chart.js is vendored (`internal/static/vendor`, hash pinned by `scripts/vendored_assets_test.go`); a Chart.js plugin would be vendored the same way, and only with explicit maintainer approval — another security surface, another version-pin to maintain.

## Testing visualizations

Go template tests verify the template renders cleanly with sample data:

```go
func TestRepoDetailTemplateRenders(t *testing.T) {
    srv := newTestWebServer(t)
    req := httptest.NewRequest(http.MethodGet, "/groups/1/repos/1", nil)
    req = req.WithContext(authedContext(t, srv))  // session helper
    rec := httptest.NewRecorder()
    srv.ServeHTTP(rec, req)

    if rec.Code != http.StatusOK {
        t.Errorf("expected 200, got %d", rec.Code)
    }
    body := rec.Body.String()
    if !strings.Contains(body, `id="chart-contributors"`) {
        t.Error("expected chart-contributors canvas to be present")
    }
    if !strings.Contains(body, `/contributor-timeseries`) {
        t.Error("expected fetch to /contributor-timeseries endpoint")
    }
}
```

But you CANNOT meaningfully unit-test JS chart rendering. For that:

1. Manual browser testing during development. Open `http://localhost:8082` and check the chart renders.
2. Console errors check (`console.error('chart load failed', ...)`) — keep these in production code; they surface in browser DevTools.
3. If the chart breaks, browser DevTools' Network tab shows the failed fetch.

House rule: **for UI/frontend changes, use the feature in a browser before reporting the task as complete.** Type checking and test suites verify code correctness, not feature correctness.

## Updating the comparison page

If your chart should also appear in `/compare`:

1. Find the comparison template (search for `renderComparisonChart`).
2. Add a `<canvas id="cmp-contributors"></canvas>` next to the existing comparison canvases.
3. Add a `renderComparisonChart('cmp-contributors', 'Active contributors / week', 'weekly_counts');` call alongside the existing ones.
4. Make sure your REST API endpoint accepts the comparison page's repos-list pattern (`ids=1,2,3`).
5. Test in a browser with 2+ repos.

## Documentation

Update `docs/guide/visualizations.md` with the new chart's name, axis labels, what it shows, what data it pulls from, and known limitations (e.g. "only shows weeks with at least one commit").
