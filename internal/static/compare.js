// Same-origin fetch: the web server reverse-proxies /api/* to the
// configured api process (web.api_internal_url), so relative URLs
// work behind nginx/TLS and eliminate CORS concerns.
const API_BASE = '';
const CHART_COLORS = ['#2563eb','#10b981','#f59e0b','#ef4444','#8b5cf6'];
let selectedRepos = [];
let allRepoData = {};
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
function getDateRangeQuery() {
  const s = document.getElementById('date-since').value;
  const u = document.getElementById('date-until').value;
  const parts = [];
  if (s) parts.push('since=' + encodeURIComponent(s));
  if (u) parts.push('until=' + encodeURIComponent(u));
  return parts.length ? '?' + parts.join('&') : '';
}
function applyDateRange() {
  const s = document.getElementById('date-since').value;
  const u = document.getElementById('date-until').value;
  if (s && u && s >= u) {
    pageMessage('The "From" date must be earlier than the "To" date.');
    return;
  }
  pageMessage('');
  // Refetch all currently selected repos with the new range, then re-render.
  allRepoData = {};
  if (selectedRepos.length === 0) { renderAllCharts(); return; }
  Promise.all(selectedRepos.map(repo =>
    fetch(API_BASE + '/api/v1/repos/' + repo.id + '/timeseries' + getDateRangeQuery())
      .then(r => r.json())
      .then(ts => { allRepoData[repo.id] = ts; })
      .catch(err => console.error('Failed to refresh repo ' + repo.id + ':', err))
  )).then(() => renderAllCharts());
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
  fetchAndRender(repo);
}

function removeRepo(id) {
  selectedRepos = selectedRepos.filter(r => r.id !== id);
  delete allRepoData[id];
  pageMessage('');
  renderTags();
  renderAllCharts();
}

function renderTags() {
  const container = document.getElementById('selected-repos');
  container.textContent = '';
  selectedRepos.forEach((r, i) => {
    container.append(avxEl('span', { className: 'repo-tag', style: 'background:' + CHART_COLORS[i] + '20;color:' + CHART_COLORS[i] },
      r.owner + '/' + r.name + ' ',
      avxEl('button', { type: 'button', onclick: () => removeRepo(r.id) }, '\u00d7')));
  });
}

function fetchAndRender(repo) {
  fetch(API_BASE + '/api/v1/repos/' + repo.id + '/timeseries' + getDateRangeQuery())
    .then(r => r.json())
    .then(ts => {
      allRepoData[repo.id] = ts;
      renderAllCharts();
    });
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

  renderComparisonChart('cmp-commits', 'Commits / week', 'commits');
  renderComparisonChart('cmp-prs-opened', 'PRs Opened / week', 'prs_opened');
  renderComparisonChart('cmp-prs-merged', 'PRs Merged / week', 'prs_merged');
  renderComparisonChart('cmp-issues', 'Issues / week', 'issues');
}

function renderComparisonChart(canvasId, title, metricKey) {
  // Collect all unique weeks across all repos.
  const allWeeks = new Set();
  selectedRepos.forEach(r => {
    const ts = allRepoData[r.id];
    if (ts && ts[metricKey]) ts[metricKey].forEach(d => allWeeks.add(d.week_start.substring(0, 10)));
  });
  const labels = Array.from(allWeeks).sort();
  if (labels.length === 0) return;

  const datasets = selectedRepos.map((repo, i) => {
    const ts = allRepoData[repo.id];
    const dataMap = {};
    if (ts && ts[metricKey]) ts[metricKey].forEach(d => dataMap[d.week_start.substring(0, 10)] = d.count);

    let values = labels.map(w => dataMap[w] || 0);

    if (currentMode === 'pct') {
      // 100% mode: normalize each repo so its max value is 100%.
      const max = Math.max(...values, 1);
      values = values.map(v => (v / max) * 100);
    } else if (currentMode === 'zscore') {
      // Z-score: (value - mean) / stddev.
      const mean = values.reduce((a, b) => a + b, 0) / values.length;
      const variance = values.reduce((a, b) => a + (b - mean) ** 2, 0) / values.length;
      const stddev = Math.sqrt(variance) || 1;
      values = values.map(v => (v - mean) / stddev);
    }

    return {
      label: repo.owner + '/' + repo.name,
      data: values,
      borderColor: CHART_COLORS[i],
      backgroundColor: CHART_COLORS[i] + '20',
      fill: false,
      tension: 0.3,
      pointRadius: 0,
      borderWidth: 2,
    };
  });

  const yLabel = currentMode === 'pct' ? '% of max' : currentMode === 'zscore' ? 'Z-Score (std dev)' : 'Count';

  charts[canvasId] = new Chart(document.getElementById(canvasId), {
    type: 'line',
    data: { labels, datasets },
    options: {
      responsive: true,
      plugins: {
        legend: {display: true, position: 'top', labels: {font: {size: 11}}},
        title: {display: true, text: title, font: {size: 14}},
      },
      scales: {
        x: {ticks: {maxTicksLimit: 12, font: {size: 10}}},
        y: {beginAtZero: currentMode !== 'zscore', title: {display: true, text: yLabel, font: {size: 11}}},
      },
      interaction: {intersect: false, mode: 'index'},
    }
  });
}

// Pre-populate from URL if repos param provided.
// Fetches timeseries directly (includes repo_owner and repo_name).
// Previous version nested inside a /stats fetch which broke silently on error.
const urlRepos = String(document.getElementById('compare-page').dataset.repoIds || '').split(',').filter(Boolean);
if (urlRepos.length > 0) {
  urlRepos.forEach(id => {
    fetch(API_BASE + '/api/v1/repos/' + id + '/timeseries' + getDateRangeQuery())
      .then(r => r.json())
      .then(ts => {
        const repo = {id: parseInt(id), owner: ts.repo_owner || 'unknown', name: ts.repo_name || id};
        selectedRepos.push(repo);
        allRepoData[repo.id] = ts;
        renderTags();
        renderAllCharts();
      })
      .catch(err => console.error('Failed to load repo ' + id + ':', err));
  });
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
