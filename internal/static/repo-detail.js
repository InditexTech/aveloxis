// Same-origin fetch: the web server reverse-proxies /api/* to the
// configured api process (web.api_internal_url), so relative URLs
// work behind nginx/TLS and eliminate CORS concerns.
const API_BASE = '';
// The repository id travels on the page element, never in script text
// (0.29.102, stage 2e: no inline script, so the CSP can name one source).
const REPO_ID = Number(document.getElementById('repo-page').dataset.repoId);

// Chart color palette.
const COLORS = {
  commits: {bg: 'rgba(37, 99, 235, 0.15)', border: '#2563eb'},
  prsOpened: {bg: 'rgba(16, 185, 129, 0.15)', border: '#10b981'},
  prsMerged: {bg: 'rgba(139, 92, 246, 0.15)', border: '#8b5cf6'},
  issues: {bg: 'rgba(245, 158, 11, 0.15)', border: '#f59e0b'},
};

function makeChart(canvasId, label, color, data) {
  const labels = data.map(d => d.week_start.substring(0, 10));
  const values = data.map(d => d.count);
  new Chart(document.getElementById(canvasId), {
    type: 'line',
    data: {
      labels: labels,
      datasets: [{
        label: label,
        data: values,
        borderColor: color.border,
        backgroundColor: color.bg,
        fill: true,
        tension: 0.3,
        pointRadius: 0,
        borderWidth: 2,
      }]
    },
    options: {
      responsive: true,
      plugins: {legend: {display: true, position: 'top'}},
      scales: {
        x: {display: true, ticks: {maxTicksLimit: 12, font: {size: 10}}},
        y: {display: true, beginAtZero: true, ticks: {font: {size: 10}}}
      },
      interaction: {intersect: false, mode: 'index'},
    }
  });
}

// Fetch time series and render charts.
fetch(API_BASE + '/api/v1/repos/' + REPO_ID + '/timeseries')
  .then(r => r.json())
  .then(ts => {
    makeChart('chart-commits', 'Commits / week', COLORS.commits, ts.commits || []);
    makeChart('chart-prs-opened', 'PRs Opened / week', COLORS.prsOpened, ts.prs_opened || []);
    makeChart('chart-prs-merged', 'PRs Merged / week', COLORS.prsMerged, ts.prs_merged || []);
    makeChart('chart-issues', 'Issues / week', COLORS.issues, ts.issues || []);
  })
  .catch(err => {
    console.error('Failed to load time series:', err);
    document.querySelectorAll('canvas').forEach(c => {
      c.parentElement.innerHTML = '<p class="empty">Chart data unavailable. Is <code>aveloxis api</code> running?</p>';
    });
  });

// One table row of a license table: name (text or a node), a count, an OSI mark.
function osiMark(isOSI) {
  return isOSI ? avxEl('span', { style: 'color:#059669' }, '\u2713') : avxEl('span', { style: 'color:#d1d5da' }, '\u2014');
}
function licenseRow(table, name, count, isOSI) {
  (table.tBodies[0] || table).append(avxEl('tr', null,
    avxEl('td', null, name),
    avxEl('td', { style: 'text-align:right' }, String(Number(count) || 0)),
    avxEl('td', { style: 'text-align:center' }, osiMark(isOSI))));
}

// Fetch license data.
fetch(API_BASE + '/api/v1/repos/' + REPO_ID + '/licenses')
  .then(r => r.json())
  .then(licenses => {
    const table = document.getElementById('license-table');
    if (!licenses || licenses.length === 0) {
      table.innerHTML = '<tr><th>License</th><th style="text-align:right">Count</th><th style="text-align:center">OSI</th></tr>' +
        '<tr><td colspan="3" class="empty">No dependency license data yet.</td></tr>';
      return;
    }
    table.innerHTML = '<tr><th>License</th><th style="text-align:right">Count</th><th style="text-align:center">OSI Compliant</th></tr>';
    // License names are written by package authors: text nodes only (H-1).
    licenses.forEach(l => {
      // Style "Unknown" licenses distinctly so undeclared deps stand out.
      const name = l.license === 'Unknown'
        ? avxEl('span', { style: 'color:#b45309;font-style:italic', title: 'No license declared by this dependency' }, 'Unknown')
        : String(l.license || '');
      licenseRow(table, name, l.count, l.is_osi);
    });
  })
  .catch(() => {
    document.getElementById('license-table').innerHTML =
      '<tr><td colspan="3" class="empty">License data unavailable.</td></tr>';
  });

// Fetch ScanCode source code license data.
fetch(API_BASE + '/api/v1/repos/' + REPO_ID + '/scancode-licenses')
  .then(r => r.json())
  .then(data => {
    const table = document.getElementById('scancode-license-table');
    const licenses = data.licenses || [];
    const copyrights = data.copyrights || [];

    // v0.21.0 freshness signal — show when scancode last ran on
    // this repo so the user can interpret "stale" data correctly
    // (vs assuming it represents today's source).
    const freshEl = document.getElementById('scancode-freshness');
    if (freshEl) {
      if (data.last_run) {
        freshEl.textContent = 'Last run: ';
        freshEl.append(avxEl('strong', null, String(data.last_run)),
          data.scancode_version ? ' (scancode ' + data.scancode_version + ')' : '');
      } else {
        freshEl.innerHTML = 'Last run: <em>not yet run — will run in the next scancode worker cycle</em>';
      }
    }

    if (licenses.length === 0) {
      table.innerHTML = '<tr><th>License (SPDX)</th><th style="text-align:right">Files</th><th style="text-align:center">OSI</th></tr>' +
        '<tr><td colspan="3" class="empty">No ScanCode data yet. Install scancode via <code>aveloxis install-tools</code>.</td></tr>';
      return;
    }

    table.innerHTML = '<tr><th>License (SPDX)</th><th style="text-align:right">Files</th><th style="text-align:center">OSI</th></tr>';
    licenses.forEach(l => {
      const name = l.license === 'Unknown'
        ? avxEl('span', { style: 'color:#b45309;font-style:italic', title: 'No license detected in these files' }, 'Unknown')
        : String(l.license || '');
      licenseRow(table, name, l.file_count, l.is_osi);
    });

    // Render copyright holders if present.
    if (copyrights.length > 0) {
      const section = document.getElementById('copyright-section');
      section.style.display = 'block';
      const list = document.getElementById('copyright-list');
      // Copyright holders come from file contents: text nodes only (H-1).
      list.textContent = '';
      copyrights.forEach(c => {
        const n = Number(c.file_count) || 0;
        list.append(avxEl('li', null, String(c.holder || ''), ' ',
          avxEl('span', { style: 'color:#9ca3af' }, '(' + n + ' file' + (n !== 1 ? 's' : '') + ')')));
      });
    }

  })
  .catch(() => {
    document.getElementById('scancode-license-table').innerHTML =
      '<tr><td colspan="3" class="empty">Source code license data unavailable.</td></tr>';
  });

// Fetch per-file scancode data for the sortable table.
let scancodeFilesData = [];
let scancodeSortCol = 0;
let scancodeSortAsc = true;

function renderScancodeFiles() {
  const tbody = document.getElementById('scancode-files-body');
  if (scancodeFilesData.length === 0) {
    tbody.innerHTML = '<tr><td colspan="3" class="empty">No file-level ScanCode data.</td></tr>';
    return;
  }
  const sorted = [...scancodeFilesData].sort((a, b) => {
    const keys = ['path', 'license', 'copyright'];
    const key = keys[scancodeSortCol];
    const cmp = (a[key] || '').localeCompare(b[key] || '');
    return scancodeSortAsc ? cmp : -cmp;
  });
  // File paths, licenses and copyright lines come from the repository's
  // own files: text nodes only (H-1).
  tbody.textContent = '';
  sorted.forEach(f => {
    const lic = f.license === 'Unknown'
      ? avxEl('span', { style: 'color:#b45309;font-style:italic' }, 'Unknown')
      : String(f.license || '');
    const cr = f.copyright
      ? avxEl('span', { title: String(f.copyright) }, String(f.copyright))
      : avxEl('span', { style: 'color:#d1d5da' }, '\u2014');
    tbody.append(avxEl('tr', null,
      avxEl('td', { style: 'padding:4px 8px;word-break:break-all;max-width:280px;font-family:monospace;font-size:11px' }, String(f.path || '')),
      avxEl('td', { style: 'padding:4px 8px;max-width:160px' }, lic),
      avxEl('td', { style: 'padding:4px 8px;max-width:300px;font-size:11px' }, cr)));
  });
}

function sortScancodeFiles(col) {
  if (scancodeSortCol === col) {
    scancodeSortAsc = !scancodeSortAsc;
  } else {
    scancodeSortCol = col;
    scancodeSortAsc = true;
  }
  renderScancodeFiles();
}

fetch(API_BASE + '/api/v1/repos/' + REPO_ID + '/scancode-files')
  .then(r => r.json())
  .then(files => {
    scancodeFilesData = files || [];
    renderScancodeFiles();
  })
  .catch(() => {
    document.getElementById('scancode-files-body').innerHTML =
      '<tr><td colspan="3" class="empty">File data unavailable.</td></tr>';
  });

// The sortable file-list headers bind here (no on*= attributes).
document.querySelectorAll('th[data-sort-col]').forEach(function (th) {
  th.addEventListener('click', function () { sortScancodeFiles(Number(th.dataset.sortCol)); });
});
