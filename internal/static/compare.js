// The compare page reads the API's one comparison (E2 of summary/59, stage
// 3e, 0.29.109): GET /api/v1/compare answers every entity's weekly series
// for one metric — the entity resolution, the window clamped to each
// entity's first activity, the caching and the Comparisons record are the
// API's, the same for aveloxis-gui and this page. The page keeps only its
// display transform (raw counts, or a per-entity z-score over the visible
// window, as aveloxis-gui does). Same-origin fetch: the web server
// reverse-proxies /api/* to the api process.
const API_BASE = '';
const CHART_COLORS = ['#2563eb','#10b981','#f59e0b','#ef4444','#8b5cf6'];
// The four charts, each one catalog metric (see /api/v1/metrics).
const CHARTS = [
  {canvas: 'cmp-commits', title: 'Commits / week', metric: 'code_change_commits'},
  {canvas: 'cmp-prs-opened', title: 'PRs Opened / week', metric: 'change_requests'},
  {canvas: 'cmp-prs-merged', title: 'PRs Merged / week', metric: 'change_requests_merged'},
  {canvas: 'cmp-issues', title: 'Issues / week', metric: 'issues'},
];
let selectedRepos = [];   // {id, owner, name}
let seriesByMetric = {};  // metric id -> the /compare answer's series
let fetchGeneration = 0;  // bumped per refetchAll; an older answer is dropped (the v0.29.66 stale-response class)
let currentMode = 'raw';
let charts = {};

// Date-range state. Default window matches the API default (last 2 years, no upper bound).
// The inputs display the computed dates; fetch requests use whatever is currently in them.
function fmtDate(d) {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  return y + '-' + m + '-' + day;
}
function initDateRange() {
  const now = new Date();
  const since = new Date(now);
  since.setFullYear(since.getFullYear() - 2);
  document.getElementById('date-since').value = fmtDate(since);
  document.getElementById('date-until').value = fmtDate(now);
}
function applyPreset(preset) {
  document.querySelectorAll('.preset-btn').forEach(b => b.classList.remove('active'));
  const btn = document.querySelector('.preset-btn[data-preset="' + preset + '"]');
  if (btn) btn.classList.add('active');
  const now = new Date();
  const since = new Date(now);
  if (preset === '1m') since.setMonth(since.getMonth() - 1);
  else if (preset === '6m') since.setMonth(since.getMonth() - 6);
  else if (preset === '1y') since.setFullYear(since.getFullYear() - 1);
  else if (preset === '2y') since.setFullYear(since.getFullYear() - 2);
  else if (preset === '5y') since.setFullYear(since.getFullYear() - 5);
  else if (preset === 'all') since.setFullYear(1970);
  document.getElementById('date-since').value = fmtDate(since);
  document.getElementById('date-until').value = fmtDate(now);
  applyDateRange();
}
function clearPresetHighlight() {
  document.querySelectorAll('.preset-btn').forEach(b => b.classList.remove('active'));
}
document.getElementById('date-since').addEventListener('change', clearPresetHighlight);
document.getElementById('date-until').addEventListener('change', clearPresetHighlight);

// comparePath is the API's comparison for one metric over the selected
// repositories and the visible window.
function comparePath(metric) {
  const q = ['metric=' + encodeURIComponent(metric),
             'entities=' + encodeURIComponent(selectedRepos.map(r => 'repo:' + r.id).join(',')),
             'bucket=week'];
  const s = document.getElementById('date-since').value;
  const u = document.getElementById('date-until').value;
  if (s) q.push('since=' + encodeURIComponent(s));
  if (u) q.push('until=' + encodeURIComponent(u));
  return API_BASE + '/api/v1/compare?' + q.join('&');
}

function applyDateRange() {
  const s = document.getElementById('date-since').value;
  const u = document.getElementById('date-until').value;
  if (s && u && s >= u) {
    pageMessage('The "From" date must be earlier than the "To" date.');
    return;
  }
  pageMessage('');
  refetchAll();
}
initDateRange();

// The search box is the shared one (repo-search.js): a pick adds the
// repository to the comparison.
avxRepoSearch(document.getElementById('repo-search'), document.getElementById('search-results'), addRepo);

function addRepo(repo) {
  if (selectedRepos.length >= 5) { pageMessage('Maximum 5 repos for comparison.'); return; }
  if (selectedRepos.find(r => r.id === repo.id)) return;
  selectedRepos.push(repo);
  pageMessage('');
  renderTags();
  refetchAll();
}

function removeRepo(id) {
  selectedRepos = selectedRepos.filter(r => r.id !== id);
  pageMessage('');
  renderTags();
  refetchAll();
}

function renderTags() {
  const container = document.getElementById('selected-repos');
  container.textContent = '';
  selectedRepos.forEach((r, i) => {
    container.append(avxEl('span', { className: 'repo-tag', style: 'background:' + CHART_COLORS[i] + '20;color:' + CHART_COLORS[i] },
      (r.owner ? r.owner + '/' : '') + r.name + ' ',
      avxEl('button', { type: 'button', onclick: () => removeRepo(r.id) }, '×')));
  });
}

// refetchAll asks the API for every chart's comparison of the selected
// repositories (one request per metric, the entities together), then
// renders. A failed request empties that chart and says so.
function refetchAll() {
  const generation = ++fetchGeneration;
  seriesByMetric = {};
  if (selectedRepos.length === 0) { renderAllCharts(); return; }
  const series = {};
  Promise.all(CHARTS.map(c =>
    fetch(comparePath(c.metric))
      .then(r => {
        // The web's /api/ proxy answers a signed-out session with a redirect
        // to the login page (HTML, 200): say so, never "try again".
        if (r.redirected && new URL(r.url).pathname === '/login') throw {metric: c.metric, status: 401, body: {}};
        return r.ok ? r.json() : r.json().catch(() => ({})).then(body => { throw {metric: c.metric, status: r.status, body: body}; });
      })
      .then(body => { series[c.metric] = body.series || []; })
      .catch(err => {
        if (generation !== fetchGeneration) return;
        console.error('compare failed:', err); pageMessage(refusalMessage(err));
      })
  )).then(() => {
    if (generation !== fetchGeneration) return; // a newer selection's answers are in flight or drawn
    seriesByMetric = series;
    adoptLabels(series);
    renderAllCharts();
  });
}

// refusalMessage is the line under the controls for a failed comparison:
// the API's own words for a definitive per-entity refusal (an id outside
// the caller's groups answers 403 with the entity and what to do — round-4
// review of 0.29.109: "try again" told the user to retry an answer that
// refuses identically), "try again" for a transport or server failure.
function refusalMessage(err) {
  const body = err && err.body;
  if (err && err.status === 403 && body && body.error === 'entity_out_of_scope' && body.entity) {
    return body.entity + ' is not in your groups: remove it, or ' + (body.hint || 'add it to one of your groups') + '.';
  }
  if (err && err.status === 401) return 'Your session has ended; sign in again to compare.';
  return 'The comparison could not be loaded; try again.';
}

// adoptLabels names a repository the URL pre-populated by its id (owner
// empty) from the API's owner/name entity label in any answer, and redraws
// the tags when one was named; a label without an owner (the API's
// fallback for an unknown id) leaves the id as the tag. One path for a
// typed and a pre-populated selection (round-3 review of 0.29.109: a
// separate first request left ids selected with no tag to remove them
// when it failed).
function adoptLabels(series) {
  let named = false;
  Object.values(series).forEach(list => list.forEach(s => {
    const repo = s.entity && selectedRepos.find(r => r.owner === '' && r.id === s.entity.repo_id);
    const slash = repo && s.entity.label ? s.entity.label.indexOf('/') : -1;
    if (slash > 0) {
      repo.owner = s.entity.label.substring(0, slash);
      repo.name = s.entity.label.substring(slash + 1);
      named = true;
    }
  }));
  if (named) renderTags();
}

function setMode(mode) {
  currentMode = mode;
  document.querySelectorAll('.mode-toggle button').forEach(b => b.classList.remove('active'));
  document.getElementById('mode-' + mode).classList.add('active');
  renderAllCharts();
}

function renderAllCharts() {
  Object.values(charts).forEach(c => c.destroy());
  charts = {};
  if (selectedRepos.length === 0) return;
  CHARTS.forEach(c => renderComparisonChart(c));
}

// zscore is the per-entity display transform aveloxis-gui applies too:
// mean and σ over the entity's points in the visible window (buckets
// before its first activity are absent from the API's answer, so they
// never deflate the mean); σ = 0 is a flat 0.
function zscore(values) {
  const present = values.filter(v => v !== null);
  const n = present.length;
  if (n === 0) return values;
  const mean = present.reduce((a, b) => a + b, 0) / n;
  const variance = present.reduce((a, b) => a + (b - mean) ** 2, 0) / n;
  const sigma = Math.sqrt(variance);
  return values.map(v => v === null ? null : (sigma === 0 ? 0 : (v - mean) / sigma));
}

function renderComparisonChart(chart) {
  const series = seriesByMetric[chart.metric] || [];
  // The union of every entity's buckets, as dates.
  const allWeeks = new Set();
  series.forEach(s => (s.points || []).forEach(p => allWeeks.add(String(p.bucket).substring(0, 10))));
  const labels = Array.from(allWeeks).sort();
  if (labels.length === 0) return;

  const datasets = series.map((s, i) => {
    const byWeek = {};
    (s.points || []).forEach(p => { byWeek[String(p.bucket).substring(0, 10)] = p.value; });
    let values = labels.map(w => (w in byWeek) ? byWeek[w] : null);
    if (currentMode === 'zscore') values = zscore(values);
    return {
      label: s.entity && s.entity.label ? s.entity.label : ('repo ' + (s.entity ? s.entity.repo_id : i)),
      data: values,
      borderColor: CHART_COLORS[i % CHART_COLORS.length],
      backgroundColor: CHART_COLORS[i % CHART_COLORS.length] + '20',
      fill: false,
      tension: 0.3,
      pointRadius: 0,
      borderWidth: 2,
      spanGaps: false,
    };
  });

  const yLabel = currentMode === 'zscore' ? 'Z-Score (std dev)' : 'Count';
  charts[chart.canvas] = new Chart(document.getElementById(chart.canvas), {
    type: 'line',
    data: { labels, datasets },
    options: {
      responsive: true,
      plugins: {
        legend: {display: true, position: 'top', labels: {font: {size: 11}}},
        title: {display: true, text: chart.title, font: {size: 14}},
      },
      scales: {
        x: {ticks: {maxTicksLimit: 12, font: {size: 10}}},
        y: {beginAtZero: currentMode !== 'zscore', title: {display: true, text: yLabel, font: {size: 11}}},
      },
      interaction: {intersect: false, mode: 'index'},
    }
  });
}

// Pre-populate from the URL's repos= ids: the tags show the ids at once
// (each with its remove button, so a dead id can be dropped), and the
// comparison's answers name them (adoptLabels) — the same fetch path a
// typed selection takes.
const urlRepos = String(document.getElementById('compare-page').dataset.repoIds || '').split(',').filter(Boolean);
if (urlRepos.length > 0) {
  selectedRepos = urlRepos.slice(0, 5).map(id => ({id: parseInt(id, 10), owner: '', name: String(id)}));
  renderTags();
  refetchAll();
}

// pageMessage shows a line under the controls instead of a browser dialog
// (D-19 of the admin-console program: no dialogs).
function pageMessage(text) {
  const el = document.getElementById('compare-msg');
  el.textContent = text;
  el.hidden = !text;
}

// The mode, preset and apply controls bind here (no on*= attributes).
document.querySelectorAll('.mode-toggle button[data-mode]').forEach(function (b) {
  b.addEventListener('click', function () { setMode(b.dataset.mode); });
});
document.querySelectorAll('.preset-btn[data-preset]').forEach(function (b) {
  b.addEventListener('click', function () { applyPreset(b.dataset.preset); });
});
document.getElementById('apply-range').addEventListener('click', applyDateRange);
