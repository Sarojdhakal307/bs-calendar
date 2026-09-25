'use strict';
(() => {
  const $ = (id) => document.getElementById(id);
  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  };
  const ne = (n) => String(n).replace(/\d/g, (d) => '०१२३४५६७८९'[d]);
  const dark = () => window.matchMedia('(prefers-color-scheme: dark)').matches;

  // The website's own keyless endpoints (read-only, limited per IP).
  const cache = new Map();
  function api(path) {
    if (!cache.has(path)) {
      const p = fetch('/site/v1' + path).then(async (r) => {
        const body = await r.json().catch(() => ({}));
        if (!r.ok) throw Object.assign(new Error(body.detail || body.title || 'HTTP ' + r.status), { status: r.status });
        return body;
      });
      p.catch(() => cache.delete(path));
      cache.set(path, p);
    }
    return cache.get(path);
  }

  const BS_MONTHS = {
    en: ['Baisakh', 'Jestha', 'Asar', 'Shrawan', 'Bhadra', 'Ashwin', 'Kartik', 'Mangsir', 'Poush', 'Magh', 'Falgun', 'Chaitra'],
    ne: ['बैशाख', 'जेठ', 'असार', 'साउन', 'भदौ', 'असोज', 'कात्तिक', 'मंसिर', 'पुस', 'माघ', 'फागुन', 'चैत'],
  };
  const AD_MONTHS = {
    en: ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'],
    ne: ['जनवरी', 'फेब्रुअरी', 'मार्च', 'अप्रिल', 'मे', 'जुन', 'जुलाई', 'अगस्ट', 'सेप्टेम्बर', 'अक्टोबर', 'नोभेम्बर', 'डिसेम्बर'],
  };
  const WEEKDAYS = {
    en: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'],
    ne: ['आइत', 'सोम', 'मंगल', 'बुध', 'बिही', 'शुक्र', 'शनि'],
  };
  const ymd = (s) => s.split('-').map(Number);

  // ---- shared page chrome (also used by the docs page) ------------------------------

  document.querySelectorAll('[data-origin]').forEach((e) => { e.textContent = location.origin; });

  const menu = document.querySelector('.menu-btn');
  const nav = document.querySelector('.top nav');
  if (menu && nav) {
    const set = (open) => { nav.classList.toggle('open', open); menu.setAttribute('aria-expanded', String(open)); };
    menu.addEventListener('click', () => set(!nav.classList.contains('open')));
    nav.addEventListener('click', (e) => { if (e.target.closest('a')) set(false); });
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape') set(false); });
  }

  // Code tabs: each .tabs row switches the [data-pane] blocks next to it.
  document.querySelectorAll('.tabs').forEach((row) => {
    const scope = row.closest('.tabset') || row.parentElement;
    const buttons = row.querySelectorAll('button');
    buttons.forEach((b) => b.addEventListener('click', () => {
      buttons.forEach((x) => { x.classList.toggle('on', x === b); x.setAttribute('aria-selected', String(x === b)); });
      scope.querySelectorAll(':scope > [data-pane]').forEach((p) => p.classList.toggle('hidden', p.dataset.pane !== b.dataset.tab));
    }));
  });

  document.addEventListener('click', async (e) => {
    const b = e.target.closest('.copy');
    if (!b) return;
    try {
      await navigator.clipboard.writeText(b.closest('.code').querySelector('pre').innerText);
      b.textContent = 'Copied';
    } catch {
      b.textContent = 'Select to copy';
    }
    setTimeout(() => { b.textContent = 'Copy'; }, 1500);
  });

  const status = $('status');
  if (status) {
    fetch('/api/healthz', { cache: 'no-store' })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(String(r.status)))))
      .then((d) => {
        status.classList.add('up');
        status.lastChild.textContent = ' API online';
        if (d.version && $('version')) $('version').textContent = 'API version ' + d.version;
      })
      .catch(() => {
        status.classList.add('down');
        status.lastChild.textContent = ' API unreachable';
      });
  }

  if (!$('converter')) return; // the rest is for the home page

  // ---- range and today ----------------------------------------------------------------

  const info = api('/info');

  info.then((i) => {
    const [bsMin] = ymd(i.bs.min);
    const [bsMax] = ymd(i.bs.max);
    const [adMin] = ymd(i.ad.min);
    const [adMax] = ymd(i.ad.max);
    document.querySelectorAll('[data-range]').forEach((e) => {
      e.textContent = `BS ${bsMin}–${bsMax} (AD ${adMin}–${adMax})`;
    });
  }).catch(() => {});

  // ---- hero preview: the real current month ------------------------------------------

  function heroPreview(i) {
    const grid = $('mock-grid');
    const [y, m] = ymd(i.today.bs.date);
    api(`/months/BS/${y}/${m}?include=events`).then((g) => {
      document.querySelector('.mock-head strong').textContent = `${g.monthName.ne} ${ne(g.year)}`;
      grid.replaceChildren();
      WEEKDAYS.ne.forEach((d) => grid.append(el('div', 'dow', d)));
      g.cells.forEach((c) => {
        if (!c.inMonth) { grid.append(el('div')); return; }
        const d = el('div', 'd' + (c.isWeekend ? ' sat' : '') + (c.isHoliday ? ' hol' : '') + (c.isToday ? ' today' : ''), ne(c.day));
        d.append(el('small', '', String(ymd(c.ad)[2])));
        grid.append(d);
      });
      const evs = document.querySelectorAll('.mock-event');
      g.events.slice(0, evs.length).forEach((ev, idx) => {
        const row = evs[idx];
        row.replaceChildren();
        const tag = el('span', 'tag');
        tag.style.background = dark() ? ev.color.dark : ev.color.light;
        row.append(tag, document.createTextNode(' ' + (ev.title.ne || ev.title.en) + ' '), el('em', '', ev.isHoliday ? 'public holiday' : 'event'));
      });
    }).catch(() => {});
  }

  // ---- converter ----------------------------------------------------------------------

  const bsForm = $('conv-bs');
  const adForm = $('conv-ad');
  const result = $('conv-result');
  let bounds = null; // { bsMin: [y,m,d], bsMax, adMin: 'YYYY-MM-DD', adMax }

  function fillSelect(sel, items, value) {
    sel.replaceChildren(...items.map(([v, label]) => {
      const o = el('option', '', label);
      o.value = String(v);
      return o;
    }));
    if (value !== undefined) sel.value = String(value);
  }

  async function fillBSDays(keepDay) {
    const y = +bsForm.year.value;
    const m = +bsForm.month.value;
    let days = 32;
    try {
      days = (await api(`/months/BS/${y}/${m}`)).daysInMonth;
    } catch { /* keep 32; the API validates on convert */ }
    let lo = 1;
    let hi = days;
    if (bounds && y === bounds.bsMin[0] && m === bounds.bsMin[1]) lo = bounds.bsMin[2];
    if (bounds && y === bounds.bsMax[0] && m === bounds.bsMax[1]) hi = Math.min(hi, bounds.bsMax[2]);
    const items = [];
    for (let d = lo; d <= hi; d++) items.push([d, `${d} · ${ne(d)}`]);
    fillSelect(bsForm.day, items, Math.min(Math.max(keepDay || +bsForm.day.value || 1, lo), hi));
  }

  function fillBSMonths(keep) {
    const y = +bsForm.year.value;
    const lo = bounds && y === bounds.bsMin[0] ? bounds.bsMin[1] : 1;
    const hi = bounds && y === bounds.bsMax[0] ? bounds.bsMax[1] : 12;
    const items = [];
    for (let m = lo; m <= hi; m++) items.push([m, `${m}. ${BS_MONTHS.en[m - 1]} · ${BS_MONTHS.ne[m - 1]}`]);
    fillSelect(bsForm.month, items, Math.min(Math.max(keep || +bsForm.month.value || lo, lo), hi));
  }

  async function setBS(date) {
    const [y, m, d] = ymd(date);
    bsForm.year.value = String(y);
    fillBSMonths(m);
    await fillBSDays(d);
  }

  function renderResult(c) {
    result.className = 'result ok';
    const bsNe = `${c.bs.monthName.ne} ${ne(c.bs.day)}, ${ne(c.bs.year)}`;
    const bsEn = `${c.bs.day} ${c.bs.monthName.en} ${c.bs.year}`;
    const adEn = `${c.ad.day} ${c.ad.monthName.en} ${c.ad.year}`;
    const grid = el('div', 'res-grid');
    const bsItem = el('div', 'res-item');
    bsItem.append(el('small', '', 'Bikram Sambat'), el('strong', '', bsNe), el('span', '', `${bsEn} · ${c.bs.date}`));
    const adItem = el('div', 'res-item');
    adItem.append(el('small', '', 'Gregorian'), el('strong', '', adEn), el('span', '', c.ad.date));
    grid.append(bsItem, adItem);

    const foot = el('div', 'res-foot');
    foot.append(el('span', '', `${c.weekday.name.en} · ${c.weekday.name.ne}`));
    foot.append(el('span', '', `${c.bs.monthName.en} has ${c.bs.daysInMonth} days`));
    if (c.bs.yearStatus === 'projected') foot.append(el('span', '', `BS ${c.bs.year} is projected and may change`));
    const copy = el('button', 'link-btn', 'Copy');
    copy.type = 'button';
    copy.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(`${c.bs.date} BS = ${c.ad.date} AD (${c.weekday.name.en})`);
        copy.textContent = 'Copied';
      } catch { copy.textContent = 'Copy failed'; }
      setTimeout(() => { copy.textContent = 'Copy'; }, 1500);
    });
    const show = el('button', 'link-btn', 'Show in calendar');
    show.type = 'button';
    show.addEventListener('click', () => {
      calendar.goTo(c);
      $('calendar').scrollIntoView({ behavior: 'smooth' });
    });
    foot.append(copy, show);
    result.replaceChildren(grid, foot);
  }

  function renderError(err) {
    result.className = 'result err';
    result.replaceChildren(el('p', '', err.message || 'Could not convert this date.'));
  }

  async function convert(query, from) {
    result.classList.remove('err');
    try {
      const c = await api('/convert?' + new URLSearchParams(query));
      renderResult(c);
      if (from !== 'bs') await setBS(c.bs.date);
      if (from !== 'ad') adForm.date.value = c.ad.date;
      return c;
    } catch (err) {
      renderError(err);
      return null;
    }
  }

  bsForm.year.addEventListener('change', async () => { fillBSMonths(); await fillBSDays(); });
  bsForm.month.addEventListener('change', () => fillBSDays());
  bsForm.addEventListener('submit', (e) => {
    e.preventDefault();
    const p = (n) => String(n).padStart(2, '0');
    convert({ bs: `${bsForm.year.value}-${p(bsForm.month.value)}-${p(bsForm.day.value)}` }, 'bs');
  });
  adForm.addEventListener('submit', (e) => {
    e.preventDefault();
    if (adForm.date.value) convert({ ad: adForm.date.value }, 'ad');
  });

  // ---- calendar -----------------------------------------------------------------------

  const calendar = (() => {
    const grid = $('cal-grid');
    const title = $('cal-title');
    const monthSel = $('cal-month');
    const yearSel = $('cal-year');
    const prev = $('cal-prev');
    const next = $('cal-next');
    const dayCard = $('cal-day');
    const list = $('cal-events');
    const note = $('cal-note');
    const st = { basis: 'BS', lang: 'en', year: 0, month: 0, selected: null, grid: null, today: null, lim: null };
    let seq = 0;

    const digits = (n) => (st.lang === 'ne' ? ne(n) : String(n));
    const monthNames = (basis) => (basis === 'BS' ? BS_MONTHS : AD_MONTHS)[st.lang];
    const color = (ev) => (dark() ? ev.color.dark : ev.color.light);
    const titleOf = (ev) => (st.lang === 'ne' && ev.title.ne) || ev.title.en;
    const key = (y, m) => y * 12 + m - 1;

    function limits() {
      const i = st.lim;
      const a = st.basis === 'BS' ? [ymd(i.bs.min), ymd(i.bs.max)] : [ymd(i.ad.min), ymd(i.ad.max)];
      // A month is served only when its first day is in range, so a partial first month is skipped.
      const lo = key(a[0][0], a[0][1]) + (a[0][2] > 1 ? 1 : 0);
      return { lo, hi: key(a[1][0], a[1][1]), minY: Math.floor(lo / 12), maxY: a[1][0] };
    }

    function fillPickers() {
      const { minY, maxY } = limits();
      const years = [];
      for (let y = maxY; y >= minY; y--) years.push(y);
      yearSel.replaceChildren(...years.map((y) => { const o = el('option', '', digits(y)); o.value = String(y); return o; }));
      monthSel.replaceChildren(...monthNames(st.basis).map((n, i) => { const o = el('option', '', n); o.value = String(i + 1); return o; }));
    }

    function setMonth(y, m) {
      const { lo, hi } = limits();
      const k = Math.min(Math.max(key(y, m), lo), hi);
      st.year = Math.floor(k / 12);
      st.month = (k % 12) + 1;
      load();
    }

    async function load() {
      const my = ++seq;
      const { lo, hi } = limits();
      prev.disabled = key(st.year, st.month) <= lo;
      next.disabled = key(st.year, st.month) >= hi;
      yearSel.value = String(st.year);
      monthSel.value = String(st.month);
      grid.classList.add('loading');
      try {
        const g = await api(`/months/${st.basis}/${st.year}/${st.month}?include=events`);
        if (my !== seq) return;
        st.grid = g;
        render();
      } catch (err) {
        if (my !== seq) return;
        grid.replaceChildren(el('p', 'muted', err.message || 'Could not load this month.'));
        list.replaceChildren();
      } finally {
        if (my === seq) grid.classList.remove('loading');
      }
    }

    function spanLabel(g) {
      const inMonth = g.cells.filter((c) => c.inMonth);
      const first = inMonth[0];
      const last = inMonth[inMonth.length - 1];
      if (g.basis === 'BS') {
        const [y1, m1] = ymd(first.ad);
        const [y2, m2] = ymd(last.ad);
        const names = AD_MONTHS[st.lang];
        return y1 === y2
          ? `${names[m1 - 1]}–${names[m2 - 1]} ${digits(y1)}`
          : `${names[m1 - 1]} ${digits(y1)} – ${names[m2 - 1]} ${digits(y2)}`;
      }
      if (!first.bs || !last.bs) return '';
      const [y1, m1] = ymd(first.bs);
      const [y2, m2] = ymd(last.bs);
      const names = BS_MONTHS[st.lang];
      return y1 === y2
        ? `${names[m1 - 1]}–${names[m2 - 1]} ${digits(y1)}`
        : `${names[m1 - 1]} ${digits(y1)} – ${names[m2 - 1]} ${digits(y2)}`;
    }

    function render() {
      const g = st.grid;
      const byId = new Map(g.events.map((e) => [e.id, e]));
      title.replaceChildren(document.createTextNode(`${g.monthName[st.lang]} ${digits(g.year)}`), el('small', '', spanLabel(g)));

      const nodes = WEEKDAYS[st.lang].map((d, i) => el('div', 'dow' + (i === 6 ? ' we' : ''), d));
      g.cells.forEach((c) => {
        if (g.basis === 'BS' && !c.bs) { // before or after the supported BS range
          nodes.push(el('div', 'cell none'));
          return;
        }
        const b = el('button', 'cell');
        b.type = 'button';
        const primary = c.day;
        let alt = '';
        if (g.basis === 'BS') {
          const [, am, ad] = ymd(c.ad);
          alt = ad === 1 || (c.inMonth && primary === 1) ? `${AD_MONTHS.en[am - 1].slice(0, 3)} ${ad}` : String(ad);
        } else if (c.bs) {
          const [, bm, bd] = ymd(c.bs);
          alt = bd === 1 || (c.inMonth && primary === 1) ? `${BS_MONTHS[st.lang][bm - 1]} ${digits(bd)}` : digits(bd);
        }
        if (!c.inMonth) b.classList.add('out');
        if (c.isWeekend) b.classList.add('we');
        if (c.isHoliday) b.classList.add('hol');
        if (c.isToday) b.classList.add('today');
        if (st.selected === c.ad) b.classList.add('sel');
        b.append(el('span', 'n', digits(primary)), el('span', 'alt', alt));
        if (c.eventIds.length) {
          const dots = el('span', 'dots');
          c.eventIds.slice(0, 4).forEach((id) => {
            const i = el('i');
            const ev = byId.get(id);
            if (ev) i.style.setProperty('--c', color(ev));
            dots.append(i);
          });
          b.append(dots);
        }
        const names = c.eventIds.map((id) => byId.get(id)).filter(Boolean).map(titleOf);
        b.setAttribute('aria-label', `${c.ad}${c.bs ? ' / BS ' + c.bs : ''}${names.length ? ': ' + names.join(', ') : ''}`);
        b.addEventListener('click', () => {
          if (!c.inMonth) {
            const target = g.basis === 'BS' ? c.bs : c.ad;
            if (!target) return; // outside the supported range
            st.selected = c.ad;
            const [y, m] = ymd(target);
            setMonth(y, m);
            return;
          }
          select(c);
        });
        nodes.push(b);
      });
      grid.replaceChildren(...nodes);

      list.replaceChildren();
      if (!g.events.length) list.append(el('li', 'muted', 'No holidays or events published for this month.'));
      [...g.events].sort((a, b) => a.start.ad.localeCompare(b.start.ad)).forEach((ev) => list.append(eventItem(ev)));

      note.textContent = g.yearStatus === 'projected'
        ? `BS ${g.year} is a projected year: its month lengths are estimates until the official calendar is published.`
        : '';

      const sel = g.cells.find((c) => c.ad === st.selected && c.inMonth) || g.cells.find((c) => c.isToday) || null;
      showDay(sel);
    }

    function eventItem(ev) {
      const li = el('li');
      const box = el('div', 'ev');
      const dot = el('i');
      dot.style.setProperty('--c', color(ev));
      const body = el('div');
      const date = st.basis === 'BS' && ev.start.bs ? ev.start.bs : ev.start.ad;
      const [, m, d] = ymd(date);
      const when = `${monthNames(st.basis)[m - 1]} ${digits(d)}` + (ev.end.ad !== ev.start.ad ? ' →' : '');
      body.append(el('b', '', titleOf(ev)));
      const meta = el('span', '', when + ' · ');
      meta.append(el('span', ev.isHoliday ? 'h' : '', ev.isHoliday ? 'Holiday' : ev.category));
      body.append(meta);
      box.append(dot, body);
      li.append(box);
      return li;
    }

    function select(c) {
      st.selected = c.ad;
      grid.querySelectorAll('.cell.sel').forEach((x) => x.classList.remove('sel'));
      const idx = st.grid.cells.indexOf(c);
      grid.querySelectorAll('.cell')[idx]?.classList.add('sel');
      showDay(c);
    }

    function showDay(c) {
      dayCard.replaceChildren();
      if (!c) {
        dayCard.append(el('div', 'sub2', 'Click a day to see both dates.'));
        return;
      }
      const [ay, am, ad] = ymd(c.ad);
      const wd = WEEKDAYS[st.lang][c.weekday];
      if (c.bs) {
        const [by, bm, bd] = ymd(c.bs);
        dayCard.append(el('div', 'big', `${BS_MONTHS.ne[bm - 1]} ${ne(bd)}, ${ne(by)}`));
        dayCard.append(el('div', 'sub2', `${bd} ${BS_MONTHS.en[bm - 1]} ${by} BS`));
      }
      dayCard.append(el('div', 'sub2', `${wd} · ${ad} ${AD_MONTHS.en[am - 1]} ${ay} AD`));
      const byId = new Map(st.grid.events.map((e) => [e.id, e]));
      const evs = c.eventIds.map((id) => byId.get(id)).filter(Boolean);
      if (evs.length) {
        const ul = el('ul', 'ev-list');
        evs.forEach((ev) => ul.append(eventItem(ev)));
        dayCard.append(ul);
      }
      const conv = el('button', 'link-btn', 'Open in converter');
      conv.type = 'button';
      conv.addEventListener('click', () => {
        convert({ ad: c.ad });
        $('converter').scrollIntoView({ behavior: 'smooth' });
      });
      dayCard.append(conv);
    }

    function setBasis(basis) {
      if (basis === st.basis) return;
      // Stay on the same days: jump to the month that holds the selected (or first) day.
      const anchor = (st.grid && (st.grid.cells.find((c) => c.ad === st.selected) || st.grid.cells.find((c) => c.inMonth))) || null;
      st.basis = basis;
      document.querySelectorAll('[data-basis]').forEach((b) => b.setAttribute('aria-checked', String(b.dataset.basis === basis)));
      fillPickers();
      const date = anchor && (basis === 'BS' ? anchor.bs : anchor.ad);
      if (date) {
        const [y, m] = ymd(date);
        setMonth(y, m);
      } else {
        load();
      }
    }

    function setLang(lang) {
      st.lang = lang;
      document.querySelectorAll('[data-lang]').forEach((b) => b.setAttribute('aria-checked', String(b.dataset.lang === lang)));
      fillPickers();
      yearSel.value = String(st.year);
      monthSel.value = String(st.month);
      if (st.grid) render();
    }

    function goToday() {
      st.selected = st.today.ad.date;
      const [y, m] = ymd(st.basis === 'BS' ? st.today.bs.date : st.today.ad.date);
      setMonth(y, m);
    }

    prev.addEventListener('click', () => setMonth(st.year, st.month - 1));
    next.addEventListener('click', () => setMonth(st.year, st.month + 1));
    monthSel.addEventListener('change', () => setMonth(st.year, +monthSel.value));
    yearSel.addEventListener('change', () => setMonth(+yearSel.value, st.month));
    $('cal-today').addEventListener('click', goToday);
    document.querySelectorAll('[data-basis]').forEach((b) => b.addEventListener('click', () => setBasis(b.dataset.basis)));
    document.querySelectorAll('[data-lang]').forEach((b) => b.addEventListener('click', () => setLang(b.dataset.lang)));

    // Swipe left/right on phones to change month.
    let touchX = null;
    grid.addEventListener('touchstart', (e) => { touchX = e.touches[0].clientX; }, { passive: true });
    grid.addEventListener('touchend', (e) => {
      if (touchX === null) return;
      const dx = e.changedTouches[0].clientX - touchX;
      touchX = null;
      if (Math.abs(dx) > 60) (dx < 0 ? next : prev).click();
    });
    // Arrow keys change month while the calendar has focus (but not inside a select).
    grid.closest('.cal').addEventListener('keydown', (e) => {
      if (e.target.tagName === 'SELECT') return;
      if (e.key === 'PageUp') { e.preventDefault(); prev.click(); }
      if (e.key === 'PageDown') { e.preventDefault(); next.click(); }
    });

    return {
      init(i) {
        st.lim = i;
        st.today = i.today;
        fillPickers();
        goToday();
      },
      goTo(c) {
        st.selected = c.ad.date;
        const [y, m] = ymd(st.basis === 'BS' ? c.bs.date : c.ad.date);
        setMonth(y, m);
      },
    };
  })();

  // ---- start ---------------------------------------------------------------------------

  info.then(async (i) => {
    bounds = { bsMin: ymd(i.bs.min), bsMax: ymd(i.bs.max) };
    adForm.date.min = i.ad.min;
    adForm.date.max = i.ad.max;
    const years = [];
    for (let y = bounds.bsMax[0]; y >= bounds.bsMin[0]; y--) years.push([y, `${y} · ${ne(y)}`]);
    fillSelect(bsForm.year, years);
    $('conv-today').addEventListener('click', () => convert({ ad: i.today.ad.date }));
    await convert({ ad: i.today.ad.date });
    calendar.init(i);
    heroPreview(i);
  }).catch((err) => {
    renderError(new Error('The calendar service is unavailable right now. ' + (err.message || '')));
    $('cal-grid').replaceChildren(el('p', 'muted', 'The calendar could not be loaded.'));
  });

  // Decorative month preview until (or if) the live month loads (Asoj 2083 starts on a Thursday).
  const mock = $('mock-grid');
  WEEKDAYS.ne.forEach((d) => mock.append(el('div', 'dow', d)));
  for (let i = 0; i < 4; i++) mock.append(el('div'));
  for (let d = 1; d <= 30; d++) {
    const wd = (4 + d - 1) % 7;
    const cell = el('div', 'd' + (wd === 6 ? ' sat' : ''), ne(d));
    cell.append(el('small', '', String(new Date(Date.UTC(2026, 8, 16 + d)).getUTCDate())));
    mock.append(cell);
  }
})();
