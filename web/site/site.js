'use strict';
(() => {
  // Show this site's own address in the examples (works on localhost and on the server).
  document.querySelectorAll('[data-origin]').forEach((el) => { el.textContent = location.origin; });

  // API status (the health check needs no API key).
  const status = document.getElementById('status');
  fetch('/api/healthz', { cache: 'no-store' })
    .then((r) => (r.ok ? r.json() : Promise.reject(new Error(String(r.status)))))
    .then((d) => {
      status.classList.add('up');
      status.lastChild.textContent = ' API online';
      if (d.version) document.getElementById('version').textContent = 'API version ' + d.version;
    })
    .catch(() => {
      status.classList.add('down');
      status.lastChild.textContent = ' API unreachable';
    });

  // Code tabs.
  const tabs = document.querySelectorAll('.tabs button');
  tabs.forEach((b) => b.addEventListener('click', () => {
    tabs.forEach((x) => x.classList.toggle('on', x === b));
    document.querySelectorAll('[data-pane]').forEach((p) => p.classList.toggle('hidden', p.dataset.pane !== b.dataset.tab));
  }));

  // Copy buttons.
  document.querySelectorAll('.copy').forEach((b) => b.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(b.parentElement.querySelector('pre').innerText);
      b.textContent = 'Copied';
    } catch {
      b.textContent = 'Select to copy';
    }
    setTimeout(() => { b.textContent = 'Copy'; }, 1500);
  }));

  // Decorative month preview (Asoj 2083 starts on a Thursday, 17 Sep 2026).
  const grid = document.getElementById('mock-grid');
  const ne = (n) => String(n).replace(/\d/g, (d) => '०१२३४५६७८९'[d]);
  ['आइत', 'सोम', 'मंगल', 'बुध', 'बिही', 'शुक्र', 'शनि'].forEach((d) => {
    const el = document.createElement('div');
    el.className = 'dow';
    el.textContent = d;
    grid.append(el);
  });
  const firstWeekday = 4;
  for (let i = 0; i < firstWeekday; i++) grid.append(document.createElement('div'));
  for (let d = 1; d <= 30; d++) {
    const el = document.createElement('div');
    const wd = (firstWeekday + d - 1) % 7;
    const ad = new Date(Date.UTC(2026, 8, 16 + d));
    el.className = 'd' + (wd === 6 ? ' sat' : '') + (d === 25 ? ' hol' : '') + (d === 16 ? ' today' : '');
    el.textContent = ne(d);
    const small = document.createElement('small');
    small.textContent = String(ad.getUTCDate());
    el.append(small);
    grid.append(el);
  }
})();
