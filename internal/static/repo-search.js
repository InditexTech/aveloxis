// avxRepoSearch wires a repository search box (0.29.102, stage 2e of the
// admin-console program, WEB-9: ONE search script for the dashboard, the
// group page and the compare page — the compare page carried its own copy,
// and the two had drifted once). input: the text box; results: the dropdown
// container; onPick(repo): called with {id, owner, name} when a result is
// chosen. Owner and name are text nodes, never markup (H-1).
function avxRepoSearch(input, results, onPick) {
  // Same-origin fetch: the web server reverse-proxies /api/* to the
  // configured api process (web.api_internal_url), so relative URLs
  // work behind nginx/TLS and eliminate CORS concerns.
  const API = '';
  let timer;
  input.addEventListener('input', function () {
    clearTimeout(timer);
    const q = this.value.trim();
    if (q.length < 2) { results.style.display = 'none'; return; }
    timer = setTimeout(function () {
      fetch(API + '/api/v1/repos/search?q=' + encodeURIComponent(q))
        .then(function (r) { return r.json(); })
        .then(function (data) {
          results.textContent = '';
          if (!data || data.length === 0) {
            results.append(avxEl('div', { className: 'search-empty' }, 'No repos found'));
            results.style.display = 'block';
            return;
          }
          data.slice(0, 15).forEach(function (r) {
            const repo = { id: Number(r.id), owner: String(r.owner), name: String(r.name) };
            results.append(avxEl('div', {
              className: 'search-hit',
              onclick: function () {
                onPick(repo);
                results.style.display = 'none';
                input.value = '';
              },
            }, repo.owner + '/', avxEl('strong', null, repo.name)));
          });
          results.style.display = 'block';
        })
        .catch(function () { results.style.display = 'none'; });
    }, 200);
  });
  input.addEventListener('focus', function () {
    if (results.childNodes.length && this.value.length >= 2) results.style.display = 'block';
  });
  document.addEventListener('click', function (e) {
    if (!input.contains(e.target) && !results.contains(e.target)) results.style.display = 'none';
  });
}

// The compare widget (compareSearchWidget.html): picks become tags and a
// hidden comma list the Compare form submits; at most five.
document.querySelectorAll('form[data-search-widget]').forEach(function (form) {
  if (form.dataset.wired) return;
  form.dataset.wired = '1';
  const prefix = form.dataset.prefix;
  const input = document.getElementById(prefix + '-repo-search');
  const results = document.getElementById(prefix + '-search-results');
  const selected = document.getElementById(prefix + '-selected');
  const hiddenIds = document.getElementById(prefix + '-repo-ids');
  const msg = document.getElementById(prefix + '-search-msg');
  const COLORS = ['#2563eb', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6'];
  let repos = [];
  function say(text) { msg.textContent = text; msg.hidden = !text; }
  function render() {
    selected.textContent = '';
    repos.forEach(function (r, i) {
      selected.append(avxEl('span', {
        className: 'repo-tag',
        style: 'background:' + COLORS[i] + '20;color:' + COLORS[i],
      }, r.owner + '/' + r.name + ' ',
      avxEl('button', { type: 'button', onclick: function () { repos = repos.filter(function (x) { return x.id !== r.id; }); say(''); render(); } }, '\u00d7')));
    });
    hiddenIds.value = repos.map(function (r) { return r.id; }).join(',');
  }
  avxRepoSearch(input, results, function (repo) {
    if (repos.length >= 5) { say('Maximum 5 repos.'); return; }
    if (repos.find(function (r) { return r.id === repo.id; })) return;
    repos.push(repo);
    say('');
    render();
  });
});
