/* BS/AD Calendar admin dashboard. Plain JavaScript, no build step.
 * Talks to the API on the same origin (/v1/admin/...). Tokens live in sessionStorage (cleared when the tab closes). */
'use strict';
(() => {
  // ------------------------------------------------------------------ constants
  const BS_MONTHS = ['Baisakh', 'Jestha', 'Asar', 'Shrawan', 'Bhadra', 'Asoj', 'Kartik', 'Mangsir', 'Poush', 'Magh', 'Falgun', 'Chaitra'];
  const BS_MONTHS_NE = ['बैशाख', 'जेठ', 'असार', 'साउन', 'भदौ', 'असोज', 'कार्तिक', 'मंसिर', 'पुष', 'माघ', 'फागुन', 'चैत'];
  const AD_MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];
  const DOW = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
  const ROLES = ['viewer', 'editor', 'designer', 'calendar_admin', 'super_admin'];
  const ROLE_HELP = {
    viewer: 'Read only',
    editor: 'Events',
    designer: 'UI config (draft and publish)',
    calendar_admin: 'Events, categories, year table',
    super_admin: 'Everything, including users and API keys',
  };
  const PALETTE_KEYS = ['bg', 'surface', 'text', 'muted', 'border', 'primary', 'onPrimary', 'today', 'holiday', 'weekend', 'disabled'];
  const DEFAULT_UI = {
    schemaVersion: 1,
    defaults: { mode: 'BS', locale: 'ne', digits: 'devanagari', weekStart: 0, weekendDays: [6], todayTimeZone: 'device', colorScheme: 'system' },
    display: { allowModeSwitch: true, showSecondaryDate: true, showEventDots: true, maxDotsPerDay: 3, highlightHolidays: true, showProjectedWarning: false, views: ['month', 'agenda'], monthHeaderFormat: 'MMMM YYYY' },
    theme: {
      light: { bg: '#FFFFFF', surface: '#F6F7F9', text: '#111827', muted: '#4B5563', border: '#E5E7EB', primary: '#1D4ED8', onPrimary: '#FFFFFF', today: '#1D4ED8', holiday: '#B91C1C', weekend: '#B91C1C', disabled: '#9CA3AF' },
      dark: { bg: '#0B0F17', surface: '#141A24', text: '#F3F4F6', muted: '#9CA3AF', border: '#273041', primary: '#60A5FA', onPrimary: '#0B0F17', today: '#60A5FA', holiday: '#F87171', weekend: '#F87171', disabled: '#4B5563' },
    },
    shape: { radius: 12, density: 'comfortable' },
    font: { family: 'system' },
  };
  const CSV_HEADER = 'category,title_en,title_ne,basis,start,end,all_day,start_time,end_time,recurrence,rrule,recur_until,is_holiday,description_en,description_ne';
  const DAY_MS = 86400000;
  // Every API call goes through /api (the web container strips it before the API sees the request).
  const API = '/api';

  const $root = document.getElementById('root');

  // ------------------------------------------------------------------ state and storage
  const S = { access: null, refresh: null, expAt: 0, me: null, categories: null, years: null, allEvents: null };
  const cal = { mode: 'BS', y: 0, m: 0 };

  function sessGet(k) { try { return sessionStorage.getItem(k); } catch { return null; } }
  function sessSet(k, v) { try { if (v == null) sessionStorage.removeItem(k); else sessionStorage.setItem(k, v); } catch { /* ignore */ } }
  function prefGet(k) { try { return localStorage.getItem(k); } catch { return null; } }
  function prefSet(k, v) { try { localStorage.setItem(k, v); } catch { /* ignore */ } }

  function saveSession() {
    sessSet('bscal.admin', S.refresh ? JSON.stringify({ access: S.access, refresh: S.refresh, expAt: S.expAt }) : null);
  }
  function loadSession() {
    try {
      const v = JSON.parse(sessGet('bscal.admin') || 'null');
      if (v && v.refresh) Object.assign(S, v);
    } catch { /* ignore */ }
  }
  function setTokens(tp) {
    S.access = tp.accessToken;
    S.refresh = tp.refreshToken;
    S.expAt = Date.now() + (tp.expiresIn || 600) * 1000;
    saveSession();
  }
  function clearSession() {
    Object.assign(S, { access: null, refresh: null, expAt: 0, me: null, categories: null, years: null, allEvents: null });
    saveSession();
  }

  // ------------------------------------------------------------------ theme
  function applyTheme() {
    const t = prefGet('bscal.theme') || 'system';
    if (t === 'system') document.documentElement.removeAttribute('data-theme');
    else document.documentElement.setAttribute('data-theme', t);
  }
  function isDark() {
    const t = prefGet('bscal.theme') || 'system';
    if (t === 'system') return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
    return t === 'dark';
  }
  function cycleTheme() {
    const order = ['system', 'light', 'dark'];
    const cur = prefGet('bscal.theme') || 'system';
    prefSet('bscal.theme', order[(order.indexOf(cur) + 1) % 3]);
    applyTheme();
    render();
  }
  applyTheme();

  // ------------------------------------------------------------------ DOM helpers
  function h(tag, props, ...kids) {
    const el = document.createElement(tag);
    let value;
    if (props) {
      for (const [k, v] of Object.entries(props)) {
        if (v == null || v === false) continue;
        if (k === 'class') el.className = v;
        else if (k === 'text') el.textContent = v;
        else if (k === 'style') Object.assign(el.style, v);
        else if (k === 'value') value = v;
        else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
        else if (k in el && typeof el[k] !== 'function' && k !== 'list') el[k] = v;
        else el.setAttribute(k, v === true ? '' : v);
      }
    }
    for (const kid of kids.flat(Infinity)) {
      if (kid == null || kid === false) continue;
      el.append(kid instanceof Node ? kid : String(kid));
    }
    if (value !== undefined) el.value = value;
    return el;
  }
  const pad = (n) => String(n).padStart(2, '0');
  function field(label, input, hint) {
    return h('label', { class: 'field' }, h('span', { text: label }), input, hint ? h('div', { class: 'hint' }, hint) : null);
  }
  function selectEl(options, value, props) {
    return h('select', Object.assign({}, props, { value }),
      options.map((o) => (Array.isArray(o) ? h('option', { value: o[0] }, o[1]) : h('option', { value: o }, o))));
  }
  function checkbox(label, checked, props) {
    const input = h('input', Object.assign({ type: 'checkbox', checked: !!checked }, props));
    return { el: h('label', { class: 'check' }, input, label), input };
  }
  function badge(text, kind) { return h('span', { class: 'badge ' + (kind || '') }, text); }
  function statusBadge(s) {
    const kind = { published: 'ok', approved: 'ok', delivered: 'ok', draft: 'info', pending: 'warn', in_review: 'warn',
      archived: '', superseded: '', rejected: 'bad', dead: 'bad', rolled_back: 'bad', deleted: 'bad' }[s];
    return badge(String(s).replace('_', ' '), kind);
  }
  function fmtDT(iso) {
    if (!iso) return '—';
    const d = new Date(iso);
    return isNaN(d) ? iso : d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  }
  function swatch(color) { return h('span', { class: 'swatch', style: { background: color } }); }
  function textOn(hex) {
    const m = /^#?([0-9a-f]{6})$/i.exec(hex || '');
    if (!m) return '#fff';
    const n = parseInt(m[1], 16);
    const l = (0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) / 255;
    return l > 0.6 ? '#111827' : '#ffffff';
  }
  function table(headers, rows, emptyText) {
    if (!rows.length) return h('div', { class: 'table-wrap' }, h('div', { class: 'empty', text: emptyText || 'Nothing here yet.' }));
    return h('div', { class: 'table-wrap' }, h('table', null,
      h('thead', null, h('tr', null, headers.map((x) => h('th', null, x)))),
      h('tbody', null, rows)));
  }
  function pageHead(title, sub, ...actions) {
    return h('div', { class: 'row between', style: { marginBottom: '16px' } },
      h('div', null, h('h1', { text: title }), sub ? h('div', { class: 'muted' }, sub) : null),
      h('div', { class: 'row' }, actions));
  }

  // ------------------------------------------------------------------ toasts, errors, modals
  function toast(msg, kind) {
    const el = h('div', { class: 'toast ' + (kind || '') }, msg);
    document.getElementById('toasts').append(el);
    setTimeout(() => el.remove(), kind === 'bad' ? 8000 : 3500);
  }
  function errorBox(err) {
    const p = (err && err.p) || { title: String((err && err.message) || err) };
    const box = h('div', { class: 'alert bad' },
      h('strong', null, p.title || 'Something went wrong'),
      p.detail && p.detail !== p.title ? h('div', null, p.detail) : null);
    const items = [];
    for (const e of p.errors || []) items.push(`${e.field}: ${e.message}`);
    for (const e of (p.report && p.report.errors) || []) items.push(`${e.path}: ${e.message}`);
    for (const e of p.issues || []) items.push(`${e.year ? e.year + ': ' : ''}${e.message}`);
    for (const e of p.invalidEvents || []) items.push(`${e.title} (${e.start}): ${e.problem || 'date would no longer exist'}`);
    if (items.length) box.append(h('ul', null, items.slice(0, 30).map((t) => h('li', null, t))));
    if (err && err.code === 'VERSION_CONFLICT') box.append(h('div', null, 'Someone else changed this item. Close and open it again to get the latest version.'));
    if (p.requestId) box.append(h('div', { class: 'small muted' }, 'Request id: ' + p.requestId));
    return box;
  }
  function toastErr(err) {
    const p = (err && err.p) || {};
    let msg = p.title || String((err && err.message) || err);
    if (p.detail && p.detail !== p.title) msg += ' — ' + p.detail;
    if (p.errors && p.errors.length) msg += ' (' + p.errors.map((e) => `${e.field}: ${e.message}`).join('; ') + ')';
    toast(msg, 'bad');
  }

  function modal({ title, body, actions, wide }) {
    const errSlot = h('div');
    const footer = h('footer');
    let busy = false;
    const back = h('div', { class: 'modal-back' });
    const close = () => { back.remove(); document.removeEventListener('keydown', onKey); };
    const onKey = (e) => { if (e.key === 'Escape' && !busy) close(); };
    const box = h('div', { class: 'modal' + (wide ? ' wide' : ''), role: 'dialog', 'aria-modal': 'true' },
      h('header', null, h('h2', { text: title }), h('button', { class: 'ghost', 'aria-label': 'Close', onclick: () => !busy && close() }, '✕')),
      h('div', { class: 'body' }, errSlot, body),
      footer);
    const buttons = [];
    for (const a of actions || [{ label: 'Close', onClick: () => true }]) {
      const b = h('button', { class: a.class || '', onclick: async () => {
        errSlot.replaceChildren();
        busy = true;
        buttons.forEach((x) => { x.disabled = true; });
        try {
          const r = await a.onClick(close);
          if (r !== false) close();
        } catch (err) {
          errSlot.replaceChildren(errorBox(err));
          errSlot.scrollIntoView({ block: 'nearest' });
        } finally {
          busy = false;
          buttons.forEach((x) => { x.disabled = false; });
        }
      } }, a.label);
      if (a.hidden) b.classList.add('hidden');
      buttons.push(b);
      footer.append(b);
    }
    back.addEventListener('mousedown', (e) => { if (e.target === back && !busy) close(); });
    document.addEventListener('keydown', onKey);
    back.append(box);
    document.body.append(back);
    const first = box.querySelector('input, select, textarea');
    if (first) first.focus();
    return { close, errSlot, buttons };
  }
  function confirmBox(text, { okLabel = 'Confirm', danger = false } = {}) {
    return new Promise((resolve) => {
      let answered = false;
      const m = modal({ title: 'Please confirm', body: h('p', null, text), actions: [
        { label: 'Cancel', onClick: () => { answered = true; resolve(false); } },
        { label: okLabel, class: danger ? 'danger' : 'primary', onClick: () => { answered = true; resolve(true); } },
      ] });
      const obs = new MutationObserver(() => { if (!document.body.contains(m.errSlot) && !answered) { obs.disconnect(); resolve(false); } });
      obs.observe(document.body, { childList: true });
    });
  }
  function askText(title, label, { okLabel = 'OK', danger = false, required = true, initial = '', type = 'text' } = {}) {
    return new Promise((resolve) => {
      const input = type === 'textarea' ? h('textarea', { value: initial }) : h('input', { type, value: initial });
      let answered = false;
      const m = modal({ title, body: field(label, input), actions: [
        { label: 'Cancel', onClick: () => { answered = true; resolve(null); } },
        { label: okLabel, class: danger ? 'danger' : 'primary', onClick: () => {
          if (required && !input.value.trim()) { input.focus(); return false; }
          answered = true; resolve(input.value.trim()); return true;
        } },
      ] });
      const obs = new MutationObserver(() => { if (!document.body.contains(m.errSlot) && !answered) { obs.disconnect(); resolve(null); } });
      obs.observe(document.body, { childList: true });
    });
  }
  function showSecret(title, intro, secret) {
    const copyBtn = h('button', { onclick: async () => {
      try { await navigator.clipboard.writeText(secret); copyBtn.textContent = 'Copied'; } catch { copyBtn.textContent = 'Select and copy manually'; }
    } }, 'Copy');
    modal({ title, body: h('div', { class: 'stack' },
      h('div', { class: 'alert warn' }, intro),
      h('div', { class: 'secret' }, secret),
      copyBtn),
    actions: [{ label: 'I have saved it', class: 'primary', onClick: () => true }] });
  }

  // ------------------------------------------------------------------ API client
  class ApiError extends Error {
    constructor(status, p) { super(p.detail || p.title || 'HTTP ' + status); this.status = status; this.p = p; this.code = p.code; }
  }
  let refreshing = null;
  function refreshTokens() {
    if (!refreshing) {
      refreshing = (async () => {
        const r = await fetch(API + '/v1/admin/auth/refresh', {
          method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
          body: JSON.stringify({ refreshToken: S.refresh }), cache: 'no-store',
        });
        if (!r.ok) {
          clearSession();
          toast('Your session ended. Please sign in again.', 'bad');
          render();
          throw new ApiError(401, { title: 'Session expired', code: 'UNAUTHORIZED' });
        }
        setTokens(await r.json());
      })().finally(() => { refreshing = null; });
    }
    return refreshing;
  }
  async function api(method, path, opts = {}) {
    const isAuth = path.startsWith('/v1/admin/auth/login') || path.startsWith('/v1/admin/auth/refresh');
    if (!isAuth && S.refresh && Date.now() > S.expAt - 30000) await refreshTokens();
    const send = () => {
      const headers = Object.assign({ Accept: 'application/json' }, opts.headers);
      if (S.access && !isAuth) headers.Authorization = 'Bearer ' + S.access;
      let body;
      if (opts.body !== undefined) {
        if (typeof opts.body === 'string') body = opts.body;
        else { body = JSON.stringify(opts.body); if (!headers['Content-Type']) headers['Content-Type'] = 'application/json'; }
      }
      return fetch(API + path, { method, headers, body, cache: 'no-store' });
    };
    let res = await send();
    if (res.status === 401 && !isAuth && S.refresh) {
      await refreshTokens();
      res = await send();
    }
    let data = null;
    if (res.status !== 204) {
      const txt = await res.text();
      if (txt) { try { data = JSON.parse(txt); } catch { data = txt; } }
    }
    if (!res.ok) {
      const p = data && typeof data === 'object' ? data : { title: `Request failed (${res.status})`, detail: typeof data === 'string' ? data.slice(0, 300) : '' };
      if (res.status === 429 && res.headers.get('Retry-After')) p.detail = (p.detail ? p.detail + ' ' : '') + `Try again in ${res.headers.get('Retry-After')} s.`;
      if (res.status === 401 && !isAuth) { clearSession(); render(); }
      throw new ApiError(res.status, p);
    }
    return { data, etag: res.headers.get('ETag'), status: res.status };
  }
  const get = async (path) => (await api('GET', path)).data;
  const qs = (o) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(o)) if (v !== '' && v != null) p.set(k, v);
    const s = p.toString();
    return s ? '?' + s : '';
  };
  const ifMatch = (version) => ({ 'If-Match': `"v${version}"` });
  const can = (perm) => !!(S.me && S.me.permissions.includes(perm));

  // ------------------------------------------------------------------ shared data
  async function loadCategories(force) {
    if (!S.categories || force) {
      const d = await get('/v1/admin/categories');
      S.categories = d.items.slice().sort((a, b) => a.sortOrder - b.sortOrder || a.key.localeCompare(b.key));
    }
    return S.categories;
  }
  const catByKey = (key) => (S.categories || []).find((c) => c.key === key);
  const catColor = (key) => { const c = catByKey(key); return c ? (isDark() ? c.colorDark : c.colorLight) : '#6b7280'; };

  async function loadYears(force) {
    if (!S.years || force) {
      const d = await get('/v1/admin/years');
      const list = d.items.map((y) => ({ ...y, startEpoch: adToEpoch(y.adStart) }));
      S.years = { dataVersion: d.dataVersion, sha256: d.sha256, list, byBs: new Map(list.map((y) => [y.bsYear, y])) };
    }
    return S.years;
  }
  async function loadAllEvents(force) {
    if (!S.allEvents || force) {
      const items = [];
      let cursor = '';
      for (let i = 0; i < 20; i++) {
        const d = await get('/v1/admin/events' + qs({ limit: 200, cursor }));
        items.push(...d.items);
        if (!d.nextCursor) break;
        cursor = d.nextCursor;
      }
      S.allEvents = items;
    }
    return S.allEvents;
  }
  function eventsChanged() { S.allEvents = null; }

  // ------------------------------------------------------------------ date conversion (from the year table)
  function parseYmd(s) { const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s || ''); return m ? { y: +m[1], m: +m[2], d: +m[3] } : null; }
  function adToEpoch(s) { const p = parseYmd(s); return p ? Math.floor(Date.UTC(p.y, p.m - 1, p.d) / DAY_MS) : null; }
  function epochToAd(e) { return new Date(e * DAY_MS).toISOString().slice(0, 10); }
  function adValid(s) { const e = adToEpoch(s); return e != null && epochToAd(e) === s; }
  function bsToEpoch(y, m, d) {
    const Y = S.years && S.years.byBs.get(y);
    if (!Y || m < 1 || m > 12 || d < 1 || d > Y.days[m - 1]) return null;
    let e = Y.startEpoch;
    for (let i = 0; i < m - 1; i++) e += Y.days[i];
    return e + d - 1;
  }
  function bsStrToEpoch(s) { const p = parseYmd(s); return p ? bsToEpoch(p.y, p.m, p.d) : null; }
  function epochToBs(e) {
    if (!S.years) return null;
    for (const Y of S.years.list) {
      if (e >= Y.startEpoch && e < Y.startEpoch + Y.length) {
        let off = e - Y.startEpoch;
        for (let m = 0; m < 12; m++) {
          if (off < Y.days[m]) return { y: Y.bsYear, m: m + 1, d: off + 1 };
          off -= Y.days[m];
        }
      }
    }
    return null;
  }
  const fmtBs = (b) => (b ? `${b.y}-${pad(b.m)}-${pad(b.d)}` : null);
  const weekday = (e) => (((e + 4) % 7) + 7) % 7; // 1970-01-01 was a Thursday
  function todayAd() {
    try { return new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Kathmandu', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date()); } catch { return new Date().toISOString().slice(0, 10); }
  }
  function describeDate(basis, s) {
    if (!s) return '';
    if (basis === 'BS') {
      const e = bsStrToEpoch(s);
      if (e == null) return S.years ? 'Not a valid BS date in the year table' : '';
      const p = parseYmd(s);
      return `${p.d} ${BS_MONTHS[p.m - 1]} ${p.y} BS = ${epochToAd(e)} AD (${DOW[weekday(e)]})`;
    }
    if (!adValid(s)) return 'Not a valid AD date';
    const e = adToEpoch(s);
    const b = epochToBs(e);
    return b ? `${s} AD = ${b.d} ${BS_MONTHS[b.m - 1]} ${b.y} BS (${DOW[weekday(e)]})` : `${s} AD is outside the BS year table`;
  }

  // ------------------------------------------------------------------ login
  function renderLogin() {
    const email = h('input', { type: 'email', autocomplete: 'username', required: true, placeholder: 'admin@example.com' });
    const password = h('input', { type: 'password', autocomplete: 'current-password', required: true });
    const err = h('div');
    const btn = h('button', { class: 'primary', type: 'submit', style: { width: '100%', justifyContent: 'center' } }, 'Sign in');
    const form = h('form', { class: 'stack', onsubmit: async (e) => {
      e.preventDefault();
      err.replaceChildren();
      btn.disabled = true;
      btn.textContent = 'Signing in…';
      try {
        const { data } = await api('POST', '/v1/admin/auth/login', { body: { email: email.value.trim(), password: password.value } });
        setTokens(data);
        S.me = await get('/v1/admin/me');
        if (!location.hash || location.hash === '#/') location.hash = '#/overview';
        render();
      } catch (ex) {
        err.replaceChildren(errorBox(ex));
      } finally {
        btn.disabled = false;
        btn.textContent = 'Sign in';
      }
    } },
    h('div', null, h('h1', { text: 'Calendar Admin' }), h('div', { class: 'muted' }, 'BS/AD calendar — sign in to manage events, holidays and the app theme.')),
    err,
    field('Email', email),
    field('Password', password),
    btn);
    $root.replaceChildren(h('div', { class: 'login' }, h('div', { class: 'card' }, form)));
    email.focus();
  }

  // ------------------------------------------------------------------ layout and router
  const NAV = [
    { group: 'Calendar' },
    { id: 'overview', label: 'Overview' },
    { id: 'calendar', label: 'Calendar' },
    { id: 'events', label: 'Events' },
    { id: 'categories', label: 'Categories' },
    { id: 'years', label: 'Year table' },
    { group: 'Apps' },
    { id: 'ui', label: 'UI & theme' },
    { id: 'keys', label: 'API keys', perm: 'platform:manage' },
    { id: 'webhooks', label: 'Webhooks', perm: 'platform:manage' },
    { group: 'Admin' },
    { id: 'users', label: 'Users', perm: 'platform:manage' },
    { id: 'audit', label: 'Audit log' },
  ];
  const PAGES = {};
  let renderSeq = 0;

  function currentRoute() { return (location.hash.replace(/^#\/?/, '') || 'overview').split('?')[0]; }

  async function render() {
    if (!S.access) { renderLogin(); return; }
    const seq = ++renderSeq;
    if (!S.me) {
      try { S.me = await get('/v1/admin/me'); } catch (e) { if (!S.access) return; $root.replaceChildren(h('div', { class: 'content' }, errorBox(e))); return; }
    }
    let route = currentRoute();
    const item = NAV.find((n) => n.id === route);
    if (!item || (item.perm && !can(item.perm))) route = 'overview';

    const side = h('aside', { class: 'side' },
      h('div', { class: 'brand' }, 'Calendar Admin', h('small', null, 'BS / AD calendar API')),
      h('nav', { class: 'nav' }, NAV.filter((n) => !n.perm || can(n.perm)).map((n) => (n.group
        ? h('div', { class: 'group', text: n.group })
        : h('a', { href: '#/' + n.id, class: n.id === route ? 'active' : '', onclick: () => side.classList.remove('open') }, n.label)))));
    const themeLabel = { system: 'Theme: auto', light: 'Theme: light', dark: 'Theme: dark' }[prefGet('bscal.theme') || 'system'];
    const top = h('div', { class: 'topbar' },
      h('div', { class: 'row' },
        h('button', { class: 'menu-btn ghost', 'aria-label': 'Menu', onclick: () => side.classList.toggle('open') }, '☰'),
        h('strong', { text: (NAV.find((n) => n.id === route) || {}).label || '' })),
      h('div', { class: 'row' },
        h('span', { class: 'muted small' }, S.me.email + ' · ' + S.me.role.replace('_', ' ')),
        h('button', { class: 'sm', onclick: cycleTheme }, themeLabel),
        h('button', { class: 'sm', onclick: logout }, 'Sign out')));
    const content = h('div', { class: 'content' }, h('p', { class: 'muted' }, 'Loading…'));
    $root.replaceChildren(h('div', { class: 'app' }, side, h('div', { class: 'main' }, top, content)));
    try {
      await PAGES[route](content, () => seq === renderSeq);
    } catch (e) {
      if (seq === renderSeq && S.access) content.replaceChildren(errorBox(e));
    }
  }
  async function logout() {
    try { await api('POST', '/v1/admin/auth/logout'); } catch { /* ignore */ }
    clearSession();
    location.hash = '';
    render();
  }
  window.addEventListener('hashchange', render);
  if (window.matchMedia) window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => { if ((prefGet('bscal.theme') || 'system') === 'system' && S.access) render(); });

  // ------------------------------------------------------------------ overview
  PAGES.overview = async (el, alive) => {
    const [health, drafts] = await Promise.all([
      get('/v1/admin/health/data'),
      get('/v1/admin/years/drafts?state=pending').catch(() => ({ items: [] })),
    ]);
    if (!alive()) return;
    const stat = (label, value, kind, sub) => h('div', { class: 'card stat ' + (kind || '') },
      h('div', { class: 'label', text: label }), h('div', { class: 'value', text: String(value) }), sub ? h('div', { class: 'small muted' }, sub) : null);
    const ny = health.nextYear || {};
    const projWarn = health.daysUntilProjected != null && health.daysUntilProjected < 90;
    const holidayWarn = ny.startsInDays != null && ny.startsInDays <= 60 && !ny.publishedHolidays;
    const alerts = [];
    if (projWarn) alerts.push(h('div', { class: 'alert warn' }, `Projected (unverified) BS years start in ${health.daysUntilProjected} days. Verify BS ${health.firstProjectedBsYear} against the official calendar in the Year table.`));
    if (holidayWarn) alerts.push(h('div', { class: 'alert warn' }, `BS ${ny.bsYear} starts in ${ny.startsInDays} days and has no published holidays yet. Use Events → Copy year, review the dates, then publish.`));
    if (health.outbox.dead > 0) alerts.push(h('div', { class: 'alert bad' }, `${health.outbox.dead} webhook deliveries failed permanently. Check Webhooks.`));
    if (drafts.items.length) alerts.push(h('div', { class: 'alert' }, `${drafts.items.length} year-table change(s) wait for approval. `, h('a', { href: '#/years' }, 'Review')));
    el.replaceChildren(
      pageHead('Overview', `Signed in as ${S.me.email} (${S.me.role.replace('_', ' ')})`),
      alerts.length ? h('div', { class: 'stack', style: { marginBottom: '16px' } }, alerts) : null,
      h('div', { class: 'cards' },
        stat('Current BS year', health.currentBsYear),
        stat('Next BS year', ny.bsYear || '—', holidayWarn ? 'warn' : '', ny.startsOn ? `Starts ${ny.startsOn} (${ny.startsInDays} days) · ${ny.publishedHolidays || 0} holidays` : ''),
        stat('Supported range', `${health.supportedRange.minBsYear}–${health.supportedRange.maxBsYear}`, '', `${health.supportedRange.minAd} to ${health.supportedRange.maxAd} AD`),
        stat('First projected year', health.firstProjectedBsYear || 'none', projWarn ? 'warn' : '', health.daysUntilProjected != null ? `in ${health.daysUntilProjected} days` : 'All years verified'),
        stat('Data version', health.dataVersion),
        stat('Webhook queue', `${health.outbox.pending} pending`, health.outbox.dead ? 'bad' : '', `${health.outbox.dead} failed`)),
      h('div', { class: 'card', style: { marginTop: '16px' } },
        h('h2', { text: 'Quick actions' }),
        h('div', { class: 'row' },
          can('events:write') ? h('button', { class: 'primary', onclick: () => openEventForm(null, {}, () => {}) }, 'New event') : null,
          h('a', { class: 'btn', href: '#/calendar' }, 'Open calendar'),
          can('config:draft') ? h('a', { class: 'btn', href: '#/ui' }, 'Edit app theme') : null,
          can('platform:manage') ? h('a', { class: 'btn', href: '#/keys' }, 'Create API key') : null,
          h('a', { class: 'btn', href: '/docs', target: '_blank', rel: 'noopener' }, 'API reference'))));
  };

  // ------------------------------------------------------------------ calendar
  function occurrencesInRange(ev, from, to) {
    // Returns [{start, end}] epoch ranges overlapping [from, to]. Recurring rules are expanded here for display;
    // the server is the source of truth for what clients see.
    const s0 = adToEpoch(ev.ad.start);
    const dur = adToEpoch(ev.ad.end) - s0;
    const until = ev.recurUntil ? adToEpoch(ev.recurUntil) : Infinity;
    const out = [];
    const push = (s) => { if (s != null && s >= s0 && s <= until && s <= to && s + dur >= from) out.push({ start: s, end: s + dur }); };
    if (ev.recurrence === 'yearly_bs' && ev.bs && ev.bs.start) {
      const b = parseYmd(ev.bs.start);
      const a = epochToBs(from); const z = epochToBs(to);
      if (b && a && z) {
        for (let y = a.y - 1; y <= z.y; y++) {
          const Y = S.years.byBs.get(y);
          if (!Y) continue;
          push(bsToEpoch(y, b.m, Math.min(b.d, Y.days[b.m - 1])));
        }
      }
    } else if (ev.recurrence === 'yearly_ad') {
      const b = parseYmd(ev.ad.start);
      const ya = new Date(from * DAY_MS).getUTCFullYear(); const yz = new Date(to * DAY_MS).getUTCFullYear();
      for (let y = ya - 1; y <= yz; y++) {
        let d = b.d;
        if (b.m === 2 && d === 29 && !(y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0))) d = 28;
        push(Math.floor(Date.UTC(y, b.m - 1, d) / DAY_MS));
      }
    } else {
      push(s0);
    }
    return out;
  }
  function isHolidayEvent(ev) {
    if (ev.isHoliday != null) return ev.isHoliday;
    const c = catByKey(ev.category);
    return !!(c && c.isHoliday);
  }

  PAGES.calendar = async (el, alive) => {
    await Promise.all([loadYears(), loadCategories(), loadAllEvents()]);
    if (!alive()) return;
    if (!cal.y) {
      const t = adToEpoch(todayAd());
      const b = epochToBs(t);
      if (b) Object.assign(cal, { mode: 'BS', y: b.y, m: b.m });
      else { const p = parseYmd(todayAd()); Object.assign(cal, { mode: 'AD', y: p.y, m: p.m }); }
    }
    drawCalendar(el);
  };

  function drawCalendar(el) {
    const todayE = adToEpoch(todayAd());
    let first; let days; let title; let sub;
    if (cal.mode === 'BS') {
      const Y = S.years.byBs.get(cal.y);
      if (!Y) { cal.mode = 'AD'; const p = parseYmd(todayAd()); cal.y = p.y; cal.m = p.m; return drawCalendar(el); }
      first = bsToEpoch(cal.y, cal.m, 1);
      days = Y.days[cal.m - 1];
      title = `${BS_MONTHS[cal.m - 1]} ${cal.y}`;
      sub = `${BS_MONTHS_NE[cal.m - 1]} · ${epochToAd(first)} to ${epochToAd(first + days - 1)} AD${Y.status === 'projected' ? ' · projected year' : ''}`;
    } else {
      first = Math.floor(Date.UTC(cal.y, cal.m - 1, 1) / DAY_MS);
      days = new Date(Date.UTC(cal.y, cal.m, 0)).getUTCDate();
      title = `${AD_MONTHS[cal.m - 1]} ${cal.y}`;
      const a = epochToBs(first); const z = epochToBs(first + days - 1);
      sub = a && z ? `${BS_MONTHS[a.m - 1]} ${a.y} – ${BS_MONTHS[z.m - 1]} ${z.y} BS` : '';
    }
    const gridStart = first - weekday(first);
    const gridEnd = gridStart + 41;
    const byDay = new Map();
    let recurringRules = 0;
    for (const ev of S.allEvents) {
      if (ev.recurrence === 'rrule') recurringRules++;
      for (const o of occurrencesInRange(ev, gridStart, gridEnd)) {
        for (let e = Math.max(o.start, gridStart); e <= Math.min(o.end, gridEnd); e++) {
          if (!byDay.has(e)) byDay.set(e, []);
          byDay.get(e).push(ev);
        }
      }
    }
    const move = (delta) => {
      let m = cal.m + delta; let y = cal.y;
      if (m < 1) { m = 12; y--; } if (m > 12) { m = 1; y++; }
      if (cal.mode === 'BS' && !S.years.byBs.has(y)) { toast('That year is outside the year table.', 'bad'); return; }
      cal.y = y; cal.m = m; drawCalendar(el);
    };
    const setMode = (mode) => {
      if (mode === cal.mode) return;
      const mid = first + 14;
      if (mode === 'BS') { const b = epochToBs(mid); if (!b) { toast('This month is outside the BS year table.', 'bad'); return; } cal.y = b.y; cal.m = b.m; } else { const d = new Date(mid * DAY_MS); cal.y = d.getUTCFullYear(); cal.m = d.getUTCMonth() + 1; }
      cal.mode = mode; drawCalendar(el);
    };
    const goToday = () => { cal.y = 0; cal.m = 0; const t = epochToBs(todayE); if (cal.mode === 'BS' && t) { cal.y = t.y; cal.m = t.m; } else { const p = parseYmd(todayAd()); cal.mode = 'AD'; cal.y = p.y; cal.m = p.m; } drawCalendar(el); };

    const cells = [];
    for (let i = 0; i < 42; i++) {
      const e = gridStart + i;
      const inMonth = e >= first && e < first + days;
      const ad = epochToAd(e); const bs = epochToBs(e);
      const main = cal.mode === 'BS' ? (bs ? bs.d : '') : +ad.slice(8);
      const second = cal.mode === 'BS' ? +ad.slice(8) : (bs ? `${bs.d} ${BS_MONTHS[bs.m - 1].slice(0, 3)}` : '');
      const evs = byDay.get(e) || [];
      const holiday = evs.some((x) => x.status === 'published' && isHolidayEvent(x));
      const cls = ['cell', inMonth ? '' : 'out', e === todayE ? 'today' : '', holiday ? 'holiday' : '', weekday(e) === 6 ? 'weekend' : ''].join(' ');
      const chips = evs.slice(0, 3).map((ev) => {
        const color = catColor(ev.category);
        return h('span', { class: 'chip ' + ev.status, title: `${ev.title.en}${ev.title.ne ? ' / ' + ev.title.ne : ''} (${ev.status})`,
          style: ev.status === 'draft' ? { color } : { background: color, color: textOn(color) },
          onclick: (x) => { x.stopPropagation(); openEventForm(ev, {}, () => drawCalendarFresh(el)); } },
        (ev.recurrence !== 'none' ? '↻ ' : '') + ev.title.en);
      });
      if (evs.length > 3) chips.push(h('span', { class: 'small muted' }, `+${evs.length - 3} more`));
      cells.push(h('div', { class: cls, onclick: () => openDay(e, evs, el) },
        h('div', { class: 'num' }, h('span', null, String(main)), h('small', null, String(second))),
        chips));
    }
    el.replaceChildren(
      h('div', { class: 'cal-head' },
        h('div', { class: 'row' },
          h('button', { onclick: () => move(-1), 'aria-label': 'Previous month' }, '‹'),
          h('button', { onclick: goToday }, 'Today'),
          h('button', { onclick: () => move(1), 'aria-label': 'Next month' }, '›'),
          h('div', { class: 'cal-title' }, title, h('small', null, sub))),
        h('div', { class: 'row' },
          h('div', { class: 'seg' },
            h('button', { class: cal.mode === 'BS' ? 'on' : '', onclick: () => setMode('BS') }, 'BS'),
            h('button', { class: cal.mode === 'AD' ? 'on' : '', onclick: () => setMode('AD') }, 'AD')),
          can('events:write') ? h('button', { class: 'primary', onclick: () => openEventForm(null, { basis: cal.mode, start: cal.mode === 'BS' ? fmtBs(epochToBs(Math.max(first, Math.min(todayE, first + days - 1)))) : epochToAd(Math.max(first, Math.min(todayE, first + days - 1))) }, () => drawCalendarFresh(el)) }, 'New event') : null)),
      h('div', { class: 'cal' }, DOW.map((d) => h('div', { class: 'dow', text: d })), cells),
      h('div', { class: 'legend' },
        h('span', null, 'Solid = published'), h('span', null, 'Dashed = draft'), h('span', null, 'Faded = archived'), h('span', null, '↻ = repeats'),
        h('span', null, 'Red day = holiday or Saturday'),
        recurringRules ? h('span', null, `${recurringRules} custom-rule event(s) show only their first date here`) : null,
        can('events:write') ? h('span', null, 'Click a day to add an event') : null));
  }
  async function drawCalendarFresh(el) {
    eventsChanged();
    try { await loadAllEvents(true); drawCalendar(el); } catch (e) { toastErr(e); }
  }
  function openDay(e, evs, el) {
    const ad = epochToAd(e); const bs = epochToBs(e);
    const title = `${bs ? `${bs.d} ${BS_MONTHS[bs.m - 1]} ${bs.y} BS · ` : ''}${ad} AD`;
    if (!evs.length) {
      if (can('events:write')) openEventForm(null, { basis: cal.mode, start: cal.mode === 'BS' && bs ? fmtBs(bs) : ad }, () => drawCalendarFresh(el));
      return;
    }
    const list = h('div', { class: 'stack' }, evs.map((ev) => h('div', { class: 'row between card', style: { padding: '10px' } },
      h('div', null, swatch(catColor(ev.category)), ' ', h('strong', null, ev.title.en), ev.title.ne ? h('span', { class: 'muted' }, ' · ' + ev.title.ne) : null,
        h('div', { class: 'small muted' }, `${ev.category} · ${ev.recurrence !== 'none' ? 'repeats ' + ev.recurrence.replace('_', ' ') + ' · ' : ''}`, statusBadge(ev.status))),
      h('button', { class: 'sm', onclick: () => { m.close(); openEventForm(ev, {}, () => drawCalendarFresh(el)); } }, can('events:write') ? 'Edit' : 'View'))));
    const m = modal({ title, body: list, actions: [
      { label: 'Close', onClick: () => true },
      can('events:write') ? { label: 'Add event on this day', class: 'primary', onClick: () => { openEventForm(null, { basis: cal.mode, start: cal.mode === 'BS' && bs ? fmtBs(bs) : ad }, () => drawCalendarFresh(el)); return true; } } : null,
    ].filter(Boolean) });
  }

  // ------------------------------------------------------------------ event form
  async function openEventForm(ev, preset, onSaved) {
    try { await Promise.all([loadCategories(), loadYears()]); } catch (e) { toastErr(e); return; }
    const editable = can('events:write') && !(ev && ev.deletedAt);
    const v = ev ? {
      category: ev.category, titleEn: ev.title.en, titleNe: ev.title.ne || '', descEn: (ev.description && ev.description.en) || '', descNe: (ev.description && ev.description.ne) || '',
      basis: ev.basis, start: ev.start, end: ev.end === ev.start ? '' : ev.end, allDay: ev.allDay, startTime: ev.startTime || '', endTime: ev.endTime || '',
      tz: ev.tz, recurrence: ev.recurrence, rrule: ev.rrule || '', recurUntil: ev.recurUntil || '', isHoliday: ev.isHoliday == null ? '' : String(ev.isHoliday),
    } : {
      category: (S.categories[0] || {}).key || '', titleEn: '', titleNe: '', descEn: '', descNe: '', basis: preset.basis || 'BS', start: preset.start || '', end: '',
      allDay: true, startTime: '', endTime: '', tz: 'Asia/Kathmandu', recurrence: 'none', rrule: '', recurUntil: '', isHoliday: '',
    };
    const dis = !editable;
    const category = selectEl(S.categories.map((c) => [c.key, `${c.name.en} (${c.key})`]), v.category, { disabled: dis });
    const titleEn = h('input', { value: v.titleEn, maxlength: 5000, disabled: dis, placeholder: 'Dashain' });
    const titleNe = h('input', { value: v.titleNe, maxlength: 5000, disabled: dis, placeholder: 'दशैं' });
    const descEn = h('textarea', { value: v.descEn, disabled: dis });
    const descNe = h('textarea', { value: v.descNe, disabled: dis });
    const basis = selectEl([['BS', 'BS (Bikram Sambat)'], ['AD', 'AD (Gregorian)']], v.basis, { disabled: dis });
    const start = h('input', { value: v.start, placeholder: 'YYYY-MM-DD', disabled: dis });
    const end = h('input', { value: v.end, placeholder: 'same as start', disabled: dis });
    const startHint = h('div', { class: 'hint' }); const endHint = h('div', { class: 'hint' });
    const allDay = checkbox('All day', v.allDay, { disabled: dis });
    const startTime = h('input', { type: 'time', value: v.startTime, disabled: dis });
    const endTime = h('input', { type: 'time', value: v.endTime, disabled: dis });
    const tz = h('input', { value: v.tz, disabled: dis });
    const recurrence = selectEl([['none', 'Does not repeat'], ['yearly_bs', 'Every year on this BS date'], ['yearly_ad', 'Every year on this AD date'], ['rrule', 'Custom rule (RRULE, AD)']], v.recurrence, { disabled: dis });
    const rrule = h('input', { value: v.rrule, placeholder: 'FREQ=WEEKLY;BYDAY=FR', disabled: dis });
    const recurUntil = h('input', { type: 'date', value: v.recurUntil, disabled: dis });
    const isHoliday = selectEl([['', 'Same as category'], ['true', 'Yes, a holiday'], ['false', 'Not a holiday']], v.isHoliday, { disabled: dis });
    const timeRow = h('div', { class: 'grid3' }, field('Start time', startTime), field('End time', endTime), field('Time zone', tz));
    const rruleRow = field('Rule', rrule, 'RFC 5545 rule without DTSTART, for example FREQ=MONTHLY;BYDAY=2FR');
    const untilRow = field('Repeat until (AD, optional)', recurUntil);
    const sync = () => {
      startHint.textContent = describeDate(basis.value, start.value.trim());
      endHint.textContent = end.value.trim() ? describeDate(basis.value, end.value.trim()) : '';
      timeRow.classList.toggle('hidden', allDay.input.checked);
      rruleRow.classList.toggle('hidden', recurrence.value !== 'rrule');
      untilRow.classList.toggle('hidden', recurrence.value === 'none');
    };
    [basis, start, end, recurrence, allDay.input].forEach((x) => { x.addEventListener('input', sync); x.addEventListener('change', sync); });
    sync();
    const startWrap = h('div', null, field('Start date', start), startHint);
    const endWrap = h('div', null, field('End date (optional, inclusive)', end), endHint);
    const body = h('div', { class: 'stack' },
      ev ? h('div', { class: 'row' }, statusBadge(ev.deletedAt ? 'deleted' : ev.status), h('span', { class: 'small muted' }, `Version ${ev.version} · updated ${fmtDT(ev.updatedAt)}`),
        ev.bs && ev.ad ? h('span', { class: 'small muted' }, `· BS ${ev.bs.start}${ev.bs.end !== ev.bs.start ? ' to ' + ev.bs.end : ''} · AD ${ev.ad.start}${ev.ad.end !== ev.ad.start ? ' to ' + ev.ad.end : ''}`) : null) : null,
      h('div', { class: 'grid2' }, field('Title (English) *', titleEn), field('Title (Nepali)', titleNe)),
      h('div', { class: 'grid2' }, field('Category *', category), field('Holiday', isHoliday)),
      h('div', { class: 'grid3' }, field('Calendar of the dates', basis), startWrap, endWrap),
      allDay.el, timeRow,
      h('div', { class: 'grid2' }, field('Repeats', recurrence), untilRow), rruleRow,
      h('div', { class: 'grid2' }, field('Description (English)', descEn), field('Description (Nepali)', descNe)));

    const collect = (forPatch) => {
      if (!titleEn.value.trim()) throw new ApiError(0, { title: 'The English title is required.' });
      if (!start.value.trim()) throw new ApiError(0, { title: 'The start date is required.' });
      if (recurrence.value === 'yearly_bs' && basis.value !== 'BS') throw new ApiError(0, { title: '"Every year on this BS date" needs BS dates.' });
      if ((recurrence.value === 'yearly_ad' || recurrence.value === 'rrule') && basis.value !== 'AD') throw new ApiError(0, { title: 'This kind of repeat needs AD dates.' });
      const title = { en: titleEn.value.trim() };
      if (titleNe.value.trim()) title.ne = titleNe.value.trim();
      const hasDesc = descEn.value.trim() || descNe.value.trim();
      const desc = hasDesc ? { en: descEn.value.trim() || titleEn.value.trim() } : null;
      if (hasDesc && descNe.value.trim()) desc.ne = descNe.value.trim();
      const p = { category: category.value, title, basis: basis.value, start: start.value.trim(), allDay: allDay.input.checked, tz: tz.value.trim() || 'Asia/Kathmandu', recurrence: recurrence.value };
      const endV = end.value.trim();
      if (forPatch) {
        p.description = desc;
        p.end = endV || p.start;
        p.startTime = p.allDay ? null : startTime.value || null;
        p.endTime = p.allDay ? null : endTime.value || null;
        p.rrule = p.recurrence === 'rrule' ? rrule.value.trim() : null;
        p.recurUntil = p.recurrence !== 'none' && recurUntil.value ? recurUntil.value : null;
        p.isHoliday = isHoliday.value === '' ? null : isHoliday.value === 'true';
      } else {
        if (desc) p.description = desc;
        if (endV) p.end = endV;
        if (!p.allDay) { if (startTime.value) p.startTime = startTime.value; if (endTime.value) p.endTime = endTime.value; }
        if (p.recurrence === 'rrule') p.rrule = rrule.value.trim();
        if (p.recurrence !== 'none' && recurUntil.value) p.recurUntil = recurUntil.value;
        if (isHoliday.value !== '') p.isHoliday = isHoliday.value === 'true';
      }
      return p;
    };
    const save = async () => {
      if (ev) {
        const r = await api('PATCH', `/v1/admin/events/${ev.id}`, { body: collect(true), headers: Object.assign({ 'Content-Type': 'application/merge-patch+json' }, ifMatch(ev.version)) });
        return r.data;
      }
      return (await api('POST', '/v1/admin/events', { body: collect(false) })).data;
    };
    const done = (msg) => { eventsChanged(); toast(msg, 'ok'); onSaved && onSaved(); };
    const actions = [{ label: editable ? 'Cancel' : 'Close', onClick: () => true }];
    if (editable && ev) {
      actions.push({ label: 'Delete', class: 'danger', onClick: async () => {
        if (!(await confirmBox(`Delete "${ev.title.en}"? You can restore it from Events → Status: deleted.`, { okLabel: 'Delete', danger: true }))) return false;
        await api('DELETE', `/v1/admin/events/${ev.id}`, { headers: ifMatch(ev.version) });
        done('Event deleted.');
        return true;
      } });
      if (ev.status === 'published') {
        actions.push({ label: 'Archive', onClick: async () => { await api('POST', `/v1/admin/events/${ev.id}/archive`, { headers: ifMatch(ev.version) }); done('Event archived (hidden from apps).'); } });
      }
    }
    if (ev && ev.deletedAt && can('events:write')) {
      actions.push({ label: 'Restore as draft', class: 'primary', onClick: async () => { await api('POST', `/v1/admin/events/${ev.id}/restore`, { headers: ifMatch(ev.version) }); done('Event restored as a draft.'); } });
    }
    if (editable) {
      actions.push({ label: ev ? 'Save' : 'Save as draft', class: ev && ev.status === 'published' ? 'primary' : '', onClick: async () => { await save(); done(ev && ev.status === 'published' ? 'Saved. Apps get the change on their next sync.' : 'Saved as a draft. Publish it to show it in apps.'); } });
      if (!ev || ev.status !== 'published') {
        actions.push({ label: ev ? 'Save and publish' : 'Create and publish', class: 'primary', onClick: async () => {
          const saved = await save();
          await api('POST', `/v1/admin/events/${saved.id}/publish`, { headers: ifMatch(saved.version) });
          done('Published. Apps get it on their next sync.');
        } });
      }
    }
    modal({ title: ev ? (editable ? 'Edit event' : 'Event') : 'New event', body, actions, wide: true });
  }

  // ------------------------------------------------------------------ events list
  PAGES.events = async (el, alive) => {
    await Promise.all([loadCategories(), loadYears()]);
    if (!alive()) return;
    const f = {
      status: selectEl([['', 'All (not deleted)'], 'draft', 'published', 'archived', 'deleted'], ''),
      category: selectEl([['', 'All categories'], ...S.categories.map((c) => [c.key, c.name.en])], ''),
      q: h('input', { type: 'search', placeholder: 'Search title' }),
      from: h('input', { type: 'date', title: 'From (AD)' }),
      to: h('input', { type: 'date', title: 'To (AD)' }),
    };
    const tbody = h('tbody');
    const more = h('button', { class: 'hidden', onclick: () => load(false) }, 'Load more');
    const count = h('span', { class: 'muted small' });
    let cursor = null; let shown = 0;
    const row = (ev) => {
      const deleted = !!ev.deletedAt;
      const acts = [];
      if (can('events:write') && !deleted) {
        if (ev.status !== 'published') acts.push(h('button', { class: 'sm', onclick: () => act(ev, 'publish', 'Published.') }, 'Publish'));
        if (ev.status === 'published') acts.push(h('button', { class: 'sm', onclick: () => act(ev, 'archive', 'Archived.') }, 'Archive'));
      }
      if (can('events:write') && deleted) acts.push(h('button', { class: 'sm', onclick: () => act(ev, 'restore', 'Restored as a draft.') }, 'Restore'));
      acts.push(h('button', { class: 'sm', onclick: () => openEventForm(ev, {}, reload) }, can('events:write') && !deleted ? 'Edit' : 'View'));
      return h('tr', null,
        h('td', null, h('strong', null, ev.title.en), ev.title.ne ? h('div', { class: 'small muted' }, ev.title.ne) : null),
        h('td', null, swatch(catColor(ev.category)), ' ', ev.category, isHolidayEvent(ev) ? h('div', null, badge('holiday', 'bad')) : null),
        h('td', { class: 'mono' }, `BS ${ev.bs.start}${ev.bs.end !== ev.bs.start ? ' → ' + ev.bs.end : ''}`, h('div', { class: 'muted' }, `AD ${ev.ad.start}${ev.ad.end !== ev.ad.start ? ' → ' + ev.ad.end : ''}`)),
        h('td', null, ev.recurrence === 'none' ? '—' : ev.recurrence.replace('_', ' '), ev.allDay ? '' : h('div', { class: 'small muted' }, `${ev.startTime || ''}${ev.endTime ? '–' + ev.endTime : ''}`)),
        h('td', null, statusBadge(deleted ? 'deleted' : ev.status)),
        h('td', { class: 'actions' }, acts));
    };
    const act = async (ev, action, msg) => {
      try { await api('POST', `/v1/admin/events/${ev.id}/${action}`, { headers: ifMatch(ev.version) }); eventsChanged(); toast(msg, 'ok'); reload(); } catch (e) { toastErr(e); }
    };
    const load = async (reset) => {
      if (reset) { cursor = null; shown = 0; tbody.replaceChildren(); }
      try {
        const d = await get('/v1/admin/events' + qs({ status: f.status.value, category: f.category.value, q: f.q.value.trim(), from: f.from.value, to: f.to.value, limit: 50, cursor }));
        d.items.forEach((ev) => tbody.append(row(ev)));
        shown += d.items.length;
        cursor = d.nextCursor;
        more.classList.toggle('hidden', !cursor);
        count.textContent = shown ? `${shown} shown${cursor ? ' (more available)' : ''}` : '';
        if (!shown) tbody.append(h('tr', null, h('td', { colspan: 6, class: 'empty' }, 'No events match these filters.')));
      } catch (e) { toastErr(e); }
    };
    const reload = () => load(true);
    let t;
    Object.values(f).forEach((x) => x.addEventListener(x === f.q ? 'input' : 'change', () => { clearTimeout(t); t = setTimeout(reload, 250); }));
    el.replaceChildren(
      pageHead('Events', 'Holidays, festivals and other events. New events start as drafts; apps only see published events.',
        can('events:write') ? h('button', { onclick: () => openImport(reload) }, 'Import CSV') : null,
        can('events:write') ? h('button', { onclick: () => openCopyYear(reload) }, 'Copy year') : null,
        can('events:write') ? h('button', { class: 'primary', onclick: () => openEventForm(null, {}, reload) }, 'New event') : null),
      h('div', { class: 'filters' }, f.q, f.status, f.category, f.from, f.to),
      h('div', { class: 'table-wrap' }, h('table', null,
        h('thead', null, h('tr', null, ['Title', 'Category', 'Dates', 'Repeats', 'Status', ''].map((x) => h('th', null, x)))), tbody)),
      h('div', { class: 'row between', style: { marginTop: '10px' } }, count, more));
    await load(true);
  };

  function openImport(onDone) {
    const file = h('input', { type: 'file', accept: '.csv,text/csv' });
    const text = h('textarea', { class: 'code', style: { minHeight: '180px' }, placeholder: CSV_HEADER + '\npublic_holiday,Dashain,दशैं,BS,2083-06-24,2083-06-28,true,,,none,,,true,,' });
    const skipDup = checkbox('Skip rows that already exist', true);
    const report = h('div');
    file.addEventListener('change', async () => { if (file.files[0]) text.value = await file.files[0].text(); });
    const run = async (dryRun) => {
      if (!text.value.trim()) throw new ApiError(0, { title: 'Choose a CSV file or paste CSV text first.' });
      let res;
      try {
        res = await api('POST', '/v1/admin/events/import' + qs({ dryRun, skipDuplicates: skipDup.input.checked }), { body: text.value, headers: { 'Content-Type': 'text/csv' } });
      } catch (e) {
        if (e.p && e.p.report) { showReport(e.p.report); }
        throw e;
      }
      showReport(res.data);
      if (!dryRun) { eventsChanged(); toast(`Imported ${res.data.created} events as drafts.`, 'ok'); onDone && onDone(); return true; }
      return false;
    };
    const showReport = (r) => {
      const bad = r.rows.filter((x) => (x.errors && x.errors.length) || x.duplicate);
      report.replaceChildren(h('div', { class: 'alert ' + (r.invalid ? 'bad' : 'ok') },
        `${r.total} rows · ${r.valid} valid · ${r.invalid} invalid · ${r.duplicates} duplicates${r.dryRun ? ' (check only, nothing saved)' : ` · ${r.created} created`}`,
        bad.length ? h('ul', null, bad.slice(0, 50).map((x) => h('li', null, `Line ${x.row} (${x.title || '?'} ${x.start || ''}): ${x.duplicate ? 'already exists' : x.errors.map((e) => `${e.field} ${e.message}`).join('; ')}`))) : null));
    };
    modal({ title: 'Import events from CSV', wide: true, body: h('div', { class: 'stack' },
      h('p', { class: 'muted' }, 'Rows are checked first. When you import, every valid row becomes a draft in one step; nothing is saved if any row is invalid.'),
      h('div', null, h('div', { class: 'small muted' }, 'Columns (first line of the file):'), h('pre', null, CSV_HEADER)),
      field('CSV file', file), field('…or paste CSV', text), skipDup.el, report),
    actions: [
      { label: 'Cancel', onClick: () => true },
      { label: 'Check file', onClick: () => run(true) },
      { label: 'Import as drafts', class: 'primary', onClick: () => run(false) },
    ] });
  }

  function openCopyYear(onDone) {
    const cur = epochToBs(adToEpoch(todayAd()));
    const from = h('input', { type: 'number', value: cur ? cur.y : '' });
    const to = h('input', { type: 'number', value: cur ? cur.y + 1 : '' });
    const cats = S.categories.map((c) => ({ c, box: checkbox(c.name.en, false) }));
    const result = h('div');
    modal({ title: 'Copy one-off events to another BS year', body: h('div', { class: 'stack' },
      h('p', { class: 'muted' }, 'Copies events that do not repeat, keeping the same BS month and day, as drafts. Lunar festivals move every year, so check each date before publishing. Running it twice does not create duplicates.'),
      h('div', { class: 'grid2' }, field('From BS year', from), field('To BS year', to)),
      h('div', null, h('div', { class: 'small muted' }, 'Only these categories (leave all unticked for every category)'), h('div', { class: 'row' }, cats.map((x) => x.box.el))),
      result),
    actions: [
      { label: 'Close', onClick: () => true },
      { label: 'Copy as drafts', class: 'primary', onClick: async () => {
        const body = { fromBsYear: +from.value, toBsYear: +to.value };
        const picked = cats.filter((x) => x.box.input.checked).map((x) => x.c.key);
        if (picked.length) body.categories = picked;
        const { data } = await api('POST', '/v1/admin/events/copy-year', { body });
        eventsChanged();
        result.replaceChildren(h('div', { class: 'alert ok' }, `${data.created.length} drafts created, ${data.skipped.length} skipped.`,
          data.skipped.length ? h('ul', null, data.skipped.slice(0, 30).map((s) => h('li', null, `${s.title} (${s.start}): ${s.problem || 'skipped'}`))) : null));
        onDone && onDone();
        return false;
      } },
    ] });
  }

  // ------------------------------------------------------------------ categories
  PAGES.categories = async (el, alive) => {
    const cats = await loadCategories(true);
    if (!alive()) return;
    const w = can('categories:write');
    const reload = () => render();
    el.replaceChildren(
      pageHead('Categories', 'Group events and set their colours. A holiday category makes its events red days in the apps.',
        w ? h('button', { class: 'primary', onclick: () => openCategory(null, reload) }, 'New category') : null),
      table(['Colour', 'Key', 'Name', 'Holiday', 'Order', ''], cats.map((c) => h('tr', null,
        h('td', null, swatch(c.colorLight), ' ', swatch(c.colorDark)),
        h('td', { class: 'mono' }, c.key),
        h('td', null, c.name.en, c.name.ne ? h('div', { class: 'small muted' }, c.name.ne) : null),
        h('td', null, c.isHoliday ? badge('holiday', 'bad') : '—'),
        h('td', null, String(c.sortOrder)),
        h('td', { class: 'actions' }, w ? [
          h('button', { class: 'sm', onclick: () => openCategory(c, reload) }, 'Edit'),
          h('button', { class: 'sm danger', onclick: async () => {
            if (!(await confirmBox(`Delete category "${c.key}"? Only categories without events can be deleted.`, { okLabel: 'Delete', danger: true }))) return;
            try { await api('DELETE', `/v1/admin/categories/${c.id}`); S.categories = null; toast('Category deleted.', 'ok'); reload(); } catch (e) { toastErr(e); }
          } }, 'Delete')] : null))), 'No categories yet.'));
  };
  function openCategory(c, onDone) {
    const key = h('input', { value: c ? c.key : '', disabled: !!c, placeholder: 'public_holiday' });
    const nameEn = h('input', { value: c ? c.name.en : '' });
    const nameNe = h('input', { value: c ? c.name.ne || '' : '' });
    const light = h('input', { type: 'color', value: c ? c.colorLight.toLowerCase() : '#b91c1c' });
    const dark = h('input', { type: 'color', value: c ? c.colorDark.toLowerCase() : '#f87171' });
    const holiday = checkbox('Events in this category are holidays', c ? c.isHoliday : false);
    const order = h('input', { type: 'number', value: c ? c.sortOrder : 0 });
    modal({ title: c ? 'Edit category' : 'New category', body: h('div', { class: 'stack' },
      field('Key', key, 'Lowercase letters, digits and _. Apps filter by it, so it cannot change later.'),
      h('div', { class: 'grid2' }, field('Name (English)', nameEn), field('Name (Nepali)', nameNe)),
      h('div', { class: 'grid3' }, field('Colour in light mode', light), field('Colour in dark mode', dark), field('Sort order', order)),
      holiday.el),
    actions: [
      { label: 'Cancel', onClick: () => true },
      { label: 'Save', class: 'primary', onClick: async () => {
        const name = { en: nameEn.value.trim() };
        if (nameNe.value.trim()) name.ne = nameNe.value.trim();
        const body = { name, colorLight: light.value.toUpperCase(), colorDark: dark.value.toUpperCase(), isHoliday: holiday.input.checked, sortOrder: +order.value || 0 };
        if (c) await api('PATCH', `/v1/admin/categories/${c.id}`, { body, headers: { 'Content-Type': 'application/merge-patch+json' } });
        else await api('POST', '/v1/admin/categories', { body: Object.assign({ key: key.value.trim() }, body) });
        S.categories = null;
        toast('Category saved.', 'ok');
        onDone();
      } },
    ] });
  }

  // ------------------------------------------------------------------ year table
  PAGES.years = async (el, alive) => {
    const [years, drafts] = await Promise.all([loadYears(true), get('/v1/admin/years/drafts')]);
    if (!alive()) return;
    const cur = epochToBs(adToEpoch(todayAd()));
    const reload = () => { S.years = null; render(); };
    const draftRows = drafts.items.map((d) => h('tr', null,
      h('td', null, statusBadge(d.state)),
      h('td', null, d.changes.map((c) => c.bsYear).join(', ')),
      h('td', null, d.note || '—'),
      h('td', null, `${d.warnings.length} warnings · ${d.impact.movedEvents.length} events move · ${d.impact.invalidEvents.length} invalid`),
      h('td', null, fmtDT(d.createdAt)),
      h('td', { class: 'actions' }, h('button', { class: 'sm', onclick: () => openDraft(d, reload) }, d.state === 'pending' ? 'Review' : 'View'))));
    const yearRows = years.list.map((y) => h('tr', { style: cur && y.bsYear === cur.y ? { background: 'var(--primary-soft)' } : null },
      h('td', null, h('strong', null, String(y.bsYear))),
      h('td', { class: 'mono' }, y.adStart),
      h('td', { class: 'mono small' }, y.days.join(' ')),
      h('td', null, String(y.length)),
      h('td', null, y.status === 'verified' ? badge('verified', 'ok') : badge('projected', 'warn')),
      h('td', { class: 'small muted' }, y.source || ''),
      h('td', { class: 'actions' }, can('years:propose') ? h('button', { class: 'sm', onclick: () => openProposal([y], reload) }, 'Change') : null)));
    el.replaceChildren(
      pageHead('Year table', `Month lengths for each BS year (data version ${years.dataVersion}). Changes need a second calendar admin to approve.`,
        can('years:propose') ? h('button', { class: 'primary', onclick: () => openProposal([], reload) }, 'Propose a change') : null),
      h('h2', { text: 'Change requests' }),
      table(['State', 'Years', 'Note', 'Impact', 'Created', ''], draftRows, 'No change requests.'),
      h('h2', { text: 'Years', style: { marginTop: '24px' } }),
      table(['BS year', '1 Baisakh (AD)', 'Days per month', 'Days', 'Status', 'Source', ''], yearRows));
    const curRow = cur && yearRows[years.list.findIndex((y) => y.bsYear === cur.y)];
    if (curRow) curRow.scrollIntoView({ block: 'center' });
  };
  function openProposal(initial, onDone) {
    const rowsBox = h('div', { class: 'stack' });
    const rows = [];
    const addRow = (y) => {
      const bsYear = h('input', { type: 'number', value: y ? y.bsYear : '' });
      const dayInputs = Array.from({ length: 12 }, (_, i) => h('input', { type: 'number', min: 29, max: 32, value: y ? y.days[i] : 30 }));
      const status = selectEl(['verified', 'projected'], y ? y.status : 'verified');
      const source = h('input', { placeholder: 'Official calendar 2084, page 3', value: '' });
      const fill = () => {
        const Y = S.years.byBs.get(+bsYear.value);
        if (Y) { dayInputs.forEach((d, i) => { d.value = Y.days[i]; }); status.value = Y.status; }
      };
      bsYear.addEventListener('change', fill);
      const card = h('div', { class: 'card' },
        h('div', { class: 'grid3' }, field('BS year', bsYear), field('Status', status), field('Source of the numbers *', source)),
        h('div', { class: 'small muted', style: { margin: '10px 0 4px' } }, 'Days in each month'),
        h('div', { class: 'days-grid' }, dayInputs.map((d, i) => field(BS_MONTHS[i], d))),
        h('div', { class: 'row end', style: { marginTop: '8px' } }, h('button', { class: 'sm danger', onclick: () => { card.remove(); rows.splice(rows.indexOf(entry), 1); } }, 'Remove year')));
      const entry = { bsYear, dayInputs, status, source };
      rows.push(entry);
      rowsBox.append(card);
    };
    (initial.length ? initial : [null]).forEach(addRow);
    const note = h('input', { placeholder: 'Why this change is needed' });
    modal({ title: 'Propose a year-table change', wide: true, body: h('div', { class: 'stack' },
      h('div', { class: 'alert' }, 'Changing the total length of a year also moves the start of the next year, so fix both years in one request. Another calendar admin must approve before apps see it.'),
      rowsBox,
      h('button', { onclick: () => addRow(null) }, 'Add another year'),
      field('Note', note)),
    actions: [
      { label: 'Cancel', onClick: () => true },
      { label: 'Submit for approval', class: 'primary', onClick: async () => {
        const changes = rows.map((r) => ({ bsYear: +r.bsYear.value, days: r.dayInputs.map((d) => +d.value), status: r.status.value, source: r.source.value.trim() }));
        if (!changes.length) throw new ApiError(0, { title: 'Add at least one year.' });
        const { data } = await api('POST', '/v1/admin/years/drafts', { body: { changes, note: note.value.trim() } });
        toast('Change request created. Another admin must approve it.', 'ok');
        onDone();
        openDraft(data, onDone);
      } },
    ] });
  }
  function openDraft(d, onDone) {
    const mine = d.createdBy === S.me.id;
    const selfBlocked = mine && S.me.role !== 'super_admin';
    const imp = d.impact;
    const body = h('div', { class: 'stack' },
      h('div', { class: 'row' }, statusBadge(d.state), h('span', { class: 'muted small' }, `Based on data version ${d.baseVersion} · created ${fmtDT(d.createdAt)}${mine ? ' by you' : ''}`)),
      d.note ? h('p', null, d.note) : null,
      d.reason ? h('div', { class: 'alert' }, 'Reason: ' + d.reason) : null,
      d.warnings.length ? h('div', { class: 'alert warn' }, 'Warnings', h('ul', null, d.warnings.map((w) => h('li', null, `${w.year ? w.year + ': ' : ''}${w.message}`)))) : null,
      h('h3', { text: 'Years' }),
      table(['BS year', 'Before', 'After', 'Source'], imp.years.map((y) => h('tr', null,
        h('td', null, String(y.bsYear)),
        h('td', { class: 'mono small' }, y.before ? `${y.before.adStart} · ${y.before.days.join(' ')} (${y.before.length})` : 'new'),
        h('td', { class: 'mono small' }, `${y.after.adStart} · ${y.after.days.join(' ')} (${y.after.length})`),
        h('td', { class: 'small' }, (d.changes.find((c) => c.bsYear === y.bsYear) || {}).source || '')))),
      h('h3', { text: `Events that move (${imp.movedEvents.length}${imp.eventsTruncated ? '+' : ''})` }),
      imp.movedEvents.length ? table(['Event', 'Date', 'AD before', 'AD after'], imp.movedEvents.map((e) => h('tr', null, h('td', null, e.title), h('td', { class: 'mono' }, `${e.basis} ${e.start}`), h('td', { class: 'mono' }, e.oldAdStart), h('td', { class: 'mono' }, e.newAdStart || '—')))) : h('p', { class: 'muted' }, 'None.'),
      imp.invalidEvents.length ? h('div', { class: 'alert bad' }, 'These events would fall on a date that no longer exists. Fix them before approving:',
        h('ul', null, imp.invalidEvents.map((e) => h('li', null, `${e.title} (${e.basis} ${e.start}) ${e.problem || ''}`)))) : null);
    const actions = [{ label: 'Close', onClick: () => true }];
    if (d.state === 'pending' && can('years:propose')) {
      actions.push({ label: mine ? 'Withdraw' : 'Reject', class: 'danger', onClick: async () => {
        const reason = await askText(mine ? 'Withdraw change request' : 'Reject change request', 'Reason', { okLabel: mine ? 'Withdraw' : 'Reject', danger: true });
        if (!reason) return false;
        await api('POST', `/v1/admin/years/drafts/${d.id}/reject`, { body: { reason } });
        toast('Change request closed.', 'ok'); onDone();
      } });
    }
    if (d.state === 'pending' && can('years:approve')) {
      actions.push({ label: selfBlocked ? 'Approve (needs another admin)' : 'Approve and publish', class: 'primary', onClick: async () => {
        if (selfBlocked) throw new ApiError(403, { title: 'Another calendar admin must approve this.', detail: 'Four-eyes rule: the author cannot approve their own change.' });
        if (!(await confirmBox('Publish this year-table change? Apps pick up the new data on their next sync, and event dates are recalculated.', { okLabel: 'Approve and publish' }))) return false;
        await api('POST', `/v1/admin/years/drafts/${d.id}/approve`);
        S.years = null; eventsChanged();
        toast('Year table updated.', 'ok'); onDone();
      } });
    }
    modal({ title: 'Year-table change', body, actions, wide: true });
  }

  // ------------------------------------------------------------------ UI config
  let uiApp = 'web';
  PAGES.ui = async (el, alive) => {
    const appInput = h('input', { value: uiApp, list: 'ui-apps', style: { width: '160px' } });
    const apps = h('datalist', { id: 'ui-apps' }, h('option', { value: 'web' }), h('option', { value: 'mobile' }));
    const body = h('div', null, h('p', { class: 'muted' }, 'Loading…'));
    const loadApp = async () => {
      uiApp = appInput.value.trim().toLowerCase() || 'web';
      try {
        const d = await get(`/v1/admin/ui-configs/${encodeURIComponent(uiApp)}`);
        if (alive()) drawUi(body, d);
      } catch (e) { body.replaceChildren(errorBox(e)); }
    };
    appInput.addEventListener('change', loadApp);
    el.replaceChildren(
      pageHead('UI & theme', 'Colours, default calendar mode and display options for the date picker and calendar in each app. Changes are drafted, reviewed, then rolled out.',
        h('label', { class: 'row' }, h('span', { class: 'muted' }, 'App'), appInput, apps)),
      body);
    await loadApp();
  };
  function drawUi(el, d) {
    const ch = d.channel;
    const reload = () => render();
    const stable = d.items.find((x) => x.version === ch.stableVersion);
    const base = () => JSON.parse(JSON.stringify((stable && stable.config) || DEFAULT_UI));
    const pub = can('config:publish');
    const rows = d.items.map((v) => {
      const acts = [];
      acts.push(h('button', { class: 'sm', onclick: () => openUiEditor(v.app || uiApp, v, reload) }, v.status === 'draft' && can('config:draft') ? 'Edit' : 'View'));
      if (v.status === 'draft' && can('config:draft')) acts.push(h('button', { class: 'sm', onclick: () => uiAction(`/${v.version}/submit`, null, 'Sent for review.', reload) }, 'Submit'));
      if (v.status === 'in_review' && pub) {
        acts.push(h('button', { class: 'sm', onclick: async () => {
          const pct = await askText('Approve and publish', 'Roll out to what percentage of users? (100 = everyone)', { okLabel: 'Publish', initial: '100', type: 'number' });
          if (pct == null) return;
          uiAction(`/${v.version}/approve`, { rolloutPercent: Math.max(1, Math.min(100, +pct || 100)) }, 'Published.', reload);
        } }, 'Approve'));
      }
      if ((v.status === 'in_review' || v.status === 'draft') && pub) {
        acts.push(h('button', { class: 'sm danger', onclick: async () => {
          const reason = await askText('Reject version ' + v.version, 'Reason', { okLabel: 'Reject', danger: true });
          if (reason) uiAction(`/${v.version}/reject`, { reason }, 'Rejected.', reload);
        } }, 'Reject'));
      }
      if (pub && (v.status === 'superseded' || (v.status === 'published' && v.version !== ch.stableVersion))) {
        acts.push(h('button', { class: 'sm', onclick: async () => {
          const reason = await askText('Roll back to version ' + v.version, 'Reason (recorded in the audit log)', { okLabel: 'Roll back now', danger: true });
          if (reason) uiAction('/rollback', { toVersion: v.version, reason }, 'Rolled back. Apps switch on their next sync.', reload);
        } }, 'Roll back to this'));
      }
      if (can('config:draft') && v.status !== 'draft') acts.push(h('button', { class: 'sm', onclick: () => openUiEditor(uiApp, { config: v.config, minClientVersion: v.minClientVersion, fromVersion: v.version }, reload) }, 'Copy to new draft'));
      return h('tr', null,
        h('td', null, h('strong', null, 'v' + v.version), v.version === ch.stableVersion ? h('div', null, badge('live', 'ok')) : null, v.version === ch.candidateVersion ? h('div', null, badge(`rollout ${ch.candidatePercent}%`, 'warn')) : null),
        h('td', null, statusBadge(v.status), v.origin === 'rollback' ? h('div', { class: 'small muted' }, 'rollback') : null),
        h('td', null, v.note || '—', v.rejectReason ? h('div', { class: 'small', style: { color: 'var(--danger)' } }, 'Rejected: ' + v.rejectReason) : null),
        h('td', null, v.config && v.config.theme ? [swatch(v.config.theme.light.primary), ' ', swatch(v.config.theme.light.bg), ' ', swatch(v.config.theme.dark.primary), ' ', swatch(v.config.theme.dark.bg)] : '—'),
        h('td', { class: 'small' }, v.minClientVersion),
        h('td', { class: 'small' }, fmtDT(v.publishedAt || v.updatedAt)),
        h('td', { class: 'actions' }, acts));
    });
    let rollout = null;
    if (ch.candidateVersion && pub) {
      const pct = h('input', { type: 'number', min: 0, max: 100, value: ch.candidatePercent, style: { width: '90px' } });
      rollout = h('div', { class: 'alert warn row' },
        h('span', { class: 'grow' }, `Version ${ch.candidateVersion} is rolling out to ${ch.candidatePercent}% of users.`),
        pct, h('button', { class: 'sm', onclick: () => uiAction('/rollout', { percent: +pct.value }, 'Rollout updated.', reload) }, 'Set %'),
        h('button', { class: 'sm', onclick: () => uiAction('/rollout', { percent: 0 }, 'Rollout paused.', reload) }, 'Pause'),
        h('button', { class: 'sm primary', onclick: () => uiAction('/rollout', { percent: 100 }, 'Promoted to everyone.', reload) }, 'Promote to 100%'));
    }
    el.replaceChildren(
      h('div', { class: 'cards', style: { marginBottom: '16px' } },
        h('div', { class: 'card stat' }, h('div', { class: 'label' }, 'Live version'), h('div', { class: 'value' }, ch.stableVersion ? 'v' + ch.stableVersion : 'built-in default')),
        h('div', { class: 'card stat' }, h('div', { class: 'label' }, 'Rolling out'), h('div', { class: 'value' }, ch.candidateVersion ? `v${ch.candidateVersion} · ${ch.candidatePercent}%` : 'none'))),
      rollout,
      h('div', { class: 'row between', style: { margin: '8px 0 10px' } },
        h('h2', { text: 'Versions', style: { margin: 0 } }),
        can('config:draft') ? h('button', { class: 'primary', onclick: () => openUiEditor(uiApp, { config: base(), minClientVersion: (stable && stable.minClientVersion) || '0.0.0' }, reload) }, 'New draft') : null),
      table(['Version', 'Status', 'Note', 'Colours', 'Min app', 'Updated', ''], rows, 'No versions yet. Apps use the built-in default until you publish one.'),
      h('p', { class: 'small muted', style: { marginTop: '10px' } }, 'Flow: New draft → Submit → Approve (optionally to a percentage of users first) → Promote to 100%. Roll back publishes an earlier version again for everyone.'));
  }
  async function uiAction(path, body, msg, done) {
    try {
      await api('POST', `/v1/admin/ui-configs/${encodeURIComponent(uiApp)}${path}`, body ? { body } : {});
      toast(msg, 'ok');
      done();
    } catch (e) { toastErr(e); }
  }
  function openUiEditor(app, v, onDone) {
    const existing = v.version ? v : null;
    const editable = can('config:draft') && (!existing || existing.status === 'draft');
    const doc = JSON.parse(JSON.stringify(v.config || DEFAULT_UI));
    for (const k of ['defaults', 'display', 'shape', 'font']) doc[k] = doc[k] || {};
    doc.theme = doc.theme || JSON.parse(JSON.stringify(DEFAULT_UI.theme));
    const note = h('input', { value: (existing && existing.note) || (v.fromVersion ? `Based on v${v.fromVersion}` : ''), disabled: !editable });
    const minClient = h('input', { value: v.minClientVersion || '0.0.0', disabled: !editable });
    const report = h('div');
    const json = h('textarea', { class: 'code', disabled: !editable });
    const preview = h('div', { class: 'stack' });
    const dis = !editable;

    const paletteEditor = (mode) => h('div', null, h('h3', { text: mode === 'light' ? 'Light mode colours' : 'Dark mode colours' }),
      h('div', { class: 'palette' }, PALETTE_KEYS.map((k) => {
        const color = h('input', { type: 'color', value: (doc.theme[mode][k] || '#000000').toLowerCase(), disabled: dis });
        const text = h('input', { type: 'text', value: doc.theme[mode][k] || '', disabled: dis });
        const set = (val) => { doc.theme[mode][k] = val.toUpperCase(); drawPreview(); };
        color.addEventListener('input', () => { text.value = color.value.toUpperCase(); set(color.value); });
        text.addEventListener('change', () => { if (/^#[0-9a-f]{6}$/i.test(text.value)) { color.value = text.value.toLowerCase(); set(text.value); } });
        return h('label', null, color, text, h('span', null, k));
      })));
    const sel = (obj, key, options, label) => {
      const s = selectEl(options.map((o) => (Array.isArray(o) ? [String(o[0]), o[1]] : [String(o), String(o)])), obj[key] == null ? '' : String(obj[key]), { disabled: dis });
      s.addEventListener('change', () => { const num = options.find((o) => String(Array.isArray(o) ? o[0] : o) === s.value); obj[key] = typeof (Array.isArray(num) ? num[0] : num) === 'number' ? +s.value : s.value; drawPreview(); });
      return field(label, s);
    };
    const bool = (obj, key, label) => {
      const c = checkbox(label, obj[key], { disabled: dis });
      c.input.addEventListener('change', () => { obj[key] = c.input.checked; drawPreview(); });
      return c.el;
    };
    const multi = (obj, key, options, label, numeric) => {
      const cur = new Set(obj[key] || []);
      return h('div', null, h('div', { class: 'small muted' }, label), h('div', { class: 'row' }, options.map(([val, text]) => {
        const c = checkbox(text, cur.has(val), { disabled: dis });
        c.input.addEventListener('change', () => { if (c.input.checked) cur.add(val); else cur.delete(val); obj[key] = [...cur].sort(numeric ? (a, b) => a - b : undefined); drawPreview(); });
        return c.el;
      })));
    };
    const behaviour = () => {
      const dotMax = h('input', { type: 'number', min: 0, max: 5, value: doc.display.maxDotsPerDay ?? 3, disabled: dis });
      dotMax.addEventListener('change', () => { doc.display.maxDotsPerDay = +dotMax.value; });
      const radius = h('input', { type: 'range', min: 0, max: 24, value: doc.shape.radius ?? 12, disabled: dis });
      const radiusLabel = h('span', { class: 'muted small' }, `${doc.shape.radius ?? 12}px`);
      radius.addEventListener('input', () => { doc.shape.radius = +radius.value; radiusLabel.textContent = radius.value + 'px'; drawPreview(); });
      const header = h('input', { value: doc.display.monthHeaderFormat || 'MMMM YYYY', disabled: dis });
      header.addEventListener('change', () => { doc.display.monthHeaderFormat = header.value; });
      return h('div', { class: 'stack' },
        h('h3', { text: 'Defaults for new users' }),
        h('div', { class: 'grid3' },
          sel(doc.defaults, 'mode', [['BS', 'BS'], ['AD', 'AD']], 'Calendar'),
          sel(doc.defaults, 'locale', [['ne', 'Nepali'], ['en', 'English']], 'Language'),
          sel(doc.defaults, 'digits', [['devanagari', 'Devanagari (१२३)'], ['latin', 'Latin (123)']], 'Digits'),
          sel(doc.defaults, 'weekStart', [[0, 'Sunday'], [1, 'Monday'], [6, 'Saturday']], 'Week starts on'),
          sel(doc.defaults, 'colorScheme', [['system', 'Follow device'], ['light', 'Light'], ['dark', 'Dark']], 'Colour scheme'),
          sel(doc.defaults, 'todayTimeZone', [['device', 'Device time zone'], ['Asia/Kathmandu', 'Nepal time']], '"Today" uses')),
        multi(doc.defaults, 'weekendDays', DOW.map((d, i) => [i, d]), 'Weekend days', true),
        h('h3', { text: 'Display' }),
        h('div', { class: 'grid2' },
          bool(doc.display, 'allowModeSwitch', 'Users can switch BS / AD'),
          bool(doc.display, 'showSecondaryDate', 'Show the other calendar\'s date in each cell'),
          bool(doc.display, 'showEventDots', 'Show event dots'),
          bool(doc.display, 'highlightHolidays', 'Highlight holidays'),
          bool(doc.display, 'showProjectedWarning', 'Warn on projected (unverified) years')),
        h('div', { class: 'grid2' }, field('Max dots per day (0–5)', dotMax), field('Month header format', header)),
        multi(doc.display, 'views', [['month', 'Month'], ['agenda', 'Agenda'], ['year', 'Year']], 'Views'),
        h('h3', { text: 'Shape and font' }),
        h('div', { class: 'grid3' },
          field('Corner radius', h('div', { class: 'row' }, radius, radiusLabel)),
          sel(doc.shape, 'density', ['compact', 'comfortable', 'spacious'], 'Density'),
          sel(doc.font, 'family', ['system', 'inter', 'noto-sans', 'noto-sans-devanagari', 'mukta'], 'Font')));
    };
    function drawPreview() {
      const mk = (mode) => {
        const p = doc.theme[mode] || {};
        const r = (doc.shape && doc.shape.radius != null ? doc.shape.radius : 12) + 'px';
        const cells = [];
        const we = new Set(doc.defaults.weekendDays || [6]);
        const ws = doc.defaults.weekStart || 0;
        for (let i = 0; i < 7; i++) cells.push(h('span', { style: { color: p.muted, fontWeight: '600' } }, DOW[(i + ws) % 7].slice(0, 2)));
        for (let i = 0; i < 14; i++) {
          const dow = (i + ws) % 7;
          const today = i === 9; const holiday = i === 4; const disabled = i === 13;
          cells.push(h('span', { style: {
            background: today ? p.today : 'transparent', color: today ? p.onPrimary : disabled ? p.disabled : (holiday || we.has(dow)) ? p.holiday : p.text,
            borderRadius: r, fontWeight: today ? '700' : '400' } }, String(i + 1)));
        }
        return h('div', { class: 'preview', style: { background: p.bg, color: p.text, borderColor: p.border, borderRadius: r } },
          h('div', { class: 'pv-head' }, h('span', null, doc.defaults.mode === 'AD' ? 'October 2026' : 'Asoj 2083'), h('span', { style: { color: p.muted, fontWeight: '400' } }, mode)),
          h('div', { style: { background: p.surface, padding: '8px', borderRadius: r, border: '1px solid ' + p.border } }, h('div', { class: 'pv-grid' }, cells)),
          h('div', { class: 'pv-btn', style: { background: p.primary, color: p.onPrimary, borderRadius: r } }, 'Select date'));
      };
      preview.replaceChildren(h('div', { class: 'small muted' }, 'Preview'), mk('light'), mk('dark'));
    }
    const panes = {
      theme: () => h('div', { class: 'stack' }, paletteEditor('light'), paletteEditor('dark')),
      behaviour,
      json: () => { json.value = JSON.stringify(doc, null, 2); return h('div', null, json, h('div', { class: 'hint' }, 'Advanced: edit the full configuration. Switch tabs or save to apply.')); },
    };
    let tab = 'theme';
    const paneBox = h('div');
    const tabs = h('div', { class: 'tabs' });
    const applyJson = () => {
      if (tab !== 'json' || !editable) return;
      let parsed;
      try { parsed = JSON.parse(json.value); } catch (e) { throw new ApiError(0, { title: 'The JSON is not valid.', detail: e.message }); }
      for (const k of Object.keys(doc)) delete doc[k];
      Object.assign(doc, parsed);
      for (const k of ['defaults', 'display', 'shape', 'font']) doc[k] = doc[k] || {};
      doc.theme = doc.theme || {};
      doc.theme.light = doc.theme.light || {};
      doc.theme.dark = doc.theme.dark || {};
    };
    const showTab = (t) => {
      try { applyJson(); } catch (e) { report.replaceChildren(errorBox(e)); return; }
      tab = t;
      tabs.replaceChildren(...[['theme', 'Colours'], ['behaviour', 'Behaviour'], ['json', 'JSON']].map(([id, label]) => h('button', { class: id === tab ? 'on' : '', onclick: () => showTab(id) }, label)));
      paneBox.replaceChildren(panes[t]());
      drawPreview();
    };
    showTab('theme');
    const showValidation = (val) => {
      if (!val) return;
      report.replaceChildren(h('div', { class: 'alert ' + (val.valid ? (val.warnings.length ? 'warn' : 'ok') : 'bad') },
        val.valid ? 'The configuration is valid.' : 'The configuration has errors.',
        (val.errors.length || val.warnings.length) ? h('ul', null,
          val.errors.map((e) => h('li', null, `Error ${e.path}: ${e.message}`)),
          val.warnings.map((e) => h('li', null, `Warning ${e.path}: ${e.message}`))) : null));
    };
    if (existing && existing.validation) showValidation(existing.validation);
    const saveDraft = async () => {
      applyJson();
      const body = { config: doc, minClientVersion: minClient.value.trim() || '0.0.0' };
      if (note.value.trim()) body.note = note.value.trim();
      const r = existing
        ? await api('PATCH', `/v1/admin/ui-configs/${encodeURIComponent(app)}/${existing.version}`, { body })
        : await api('POST', `/v1/admin/ui-configs/${encodeURIComponent(app)}`, { body });
      return r.data;
    };
    const actions = [{ label: editable ? 'Cancel' : 'Close', onClick: () => true }];
    if (editable) {
      actions.push({ label: 'Check', onClick: async () => { applyJson(); const { data } = await api('POST', '/v1/admin/ui-configs/validate', { body: doc }); showValidation(data); return false; } });
      actions.push({ label: 'Save draft', onClick: async () => { const saved = await saveDraft(); toast(`Draft v${saved.version} saved.`, 'ok'); onDone(); } });
      actions.push({ label: 'Save and submit for review', class: 'primary', onClick: async () => {
        const saved = await saveDraft();
        if (saved.validation && !saved.validation.valid) { showValidation(saved.validation); toast(`Draft v${saved.version} saved, but it has errors to fix before review.`, 'bad'); onDone(); return false; }
        await api('POST', `/v1/admin/ui-configs/${encodeURIComponent(app)}/${saved.version}/submit`);
        toast(`v${saved.version} sent for review.`, 'ok');
        onDone();
      } });
    }
    modal({ title: existing ? `${app} · version ${existing.version} (${existing.status.replace('_', ' ')})` : `New ${app} draft`, wide: true, body: h('div', { class: 'stack' },
      report,
      h('div', { class: 'editor' }, h('div', null, tabs, paneBox), preview),
      h('div', { class: 'grid2' }, field('Note', note), field('Minimum app version', minClient, 'Older apps keep the previous compatible version.'))),
    actions });
  }

  // ------------------------------------------------------------------ API keys
  PAGES.keys = async (el, alive) => {
    const d = await get('/v1/admin/api-clients');
    if (!alive()) return;
    const reload = () => render();
    el.replaceChildren(
      pageHead('API keys', 'Websites and apps send a key in the X-Api-Key header. Public keys (pk_) go in browsers and mobile apps; server keys (sk_) stay on your servers.',
        h('button', { class: 'primary', onclick: () => openKey(reload) }, 'New API key')),
      table(['Name', 'Kind', 'Key starts with', 'Allowed websites', 'Limit / min', 'Last used', 'Status', ''], d.items.map((c) => h('tr', null,
        h('td', null, h('strong', null, c.name)),
        h('td', null, c.kind),
        h('td', { class: 'mono' }, c.keyPrefix + '…'),
        h('td', { class: 'small' }, c.allowedOrigins.length ? c.allowedOrigins.join(', ') : 'any'),
        h('td', null, String(c.ratePerMin)),
        h('td', { class: 'small' }, fmtDT(c.lastUsedAt)),
        h('td', null, c.revoked ? badge('revoked', 'bad') : badge('active', 'ok')),
        h('td', { class: 'actions' }, c.revoked ? null : h('button', { class: 'sm danger', onclick: async () => {
          if (!(await confirmBox(`Revoke "${c.name}"? Apps using this key stop working within a minute.`, { okLabel: 'Revoke', danger: true }))) return;
          try { await api('DELETE', `/v1/admin/api-clients/${c.id}`); toast('Key revoked.', 'ok'); reload(); } catch (e) { toastErr(e); }
        } }, 'Revoke')))), 'No API keys yet.'));
  };
  function openKey(onDone) {
    const name = h('input', { placeholder: 'Company website' });
    const kind = selectEl([['public', 'Public (website or mobile app)'], ['server', 'Server (backend only, never in a browser)']], 'public');
    const origins = h('textarea', { placeholder: 'https://oneclickinfosys.com\nhttps://www.oneclickinfosys.com', style: { minHeight: '70px' } });
    const rate = h('input', { type: 'number', min: 1, placeholder: 'default' });
    modal({ title: 'New API key', body: h('div', { class: 'stack' },
      field('Name', name), field('Kind', kind),
      field('Allowed websites (one per line, optional)', origins, 'For browser keys, list the exact site addresses. Leave empty for mobile apps.'),
      field('Requests per minute (optional)', rate, 'Default 600 for public keys (per user IP) and 6000 for server keys.')),
    actions: [
      { label: 'Cancel', onClick: () => true },
      { label: 'Create key', class: 'primary', onClick: async () => {
        const body = { name: name.value.trim(), kind: kind.value };
        const o = origins.value.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);
        if (o.length) body.allowedOrigins = o;
        if (rate.value) body.ratePerMin = +rate.value;
        const { data } = await api('POST', '/v1/admin/api-clients', { body });
        onDone();
        showSecret('Your new API key', 'Copy this key now. It is shown only once; if you lose it, revoke it and create a new one.', data.key);
      } },
    ] });
  }

  // ------------------------------------------------------------------ webhooks
  PAGES.webhooks = async (el, alive) => {
    const d = await get('/v1/admin/webhooks');
    if (!alive()) return;
    const reload = () => render();
    el.replaceChildren(
      pageHead('Webhooks', 'Your servers get a signed POST when events, categories, the UI config or the year table change.',
        h('button', { class: 'primary', onclick: () => openWebhook(reload) }, 'New webhook')),
      table(['URL', 'Topics', 'Status', 'Created', ''], d.items.map((w) => h('tr', null,
        h('td', { class: 'mono small' }, w.url),
        h('td', null, w.topics.join(', ')),
        h('td', null, w.active ? badge('active', 'ok') : badge('inactive')),
        h('td', { class: 'small' }, fmtDT(w.createdAt)),
        h('td', { class: 'actions' },
          h('button', { class: 'sm', onclick: () => openDeliveries(w) }, 'Deliveries'),
          w.active ? h('button', { class: 'sm danger', onclick: async () => {
            if (!(await confirmBox(`Stop sending to ${w.url}?`, { okLabel: 'Deactivate', danger: true }))) return;
            try { await api('DELETE', `/v1/admin/webhooks/${w.id}`); toast('Webhook deactivated.', 'ok'); reload(); } catch (e) { toastErr(e); }
          } }, 'Deactivate') : null))), 'No webhooks.'));
  };
  function openWebhook(onDone) {
    const url = h('input', { type: 'url', placeholder: 'https://example.com/hooks/calendar' });
    const topics = ['events', 'categories', 'config', 'data'].map((t) => ({ t, box: checkbox(t, t === 'events') }));
    modal({ title: 'New webhook', body: h('div', { class: 'stack' },
      field('URL (https)', url),
      h('div', null, h('div', { class: 'small muted' }, 'Send when these change'), h('div', { class: 'row' }, topics.map((x) => x.box.el)))),
    actions: [
      { label: 'Cancel', onClick: () => true },
      { label: 'Create', class: 'primary', onClick: async () => {
        const { data } = await api('POST', '/v1/admin/webhooks', { body: { url: url.value.trim(), topics: topics.filter((x) => x.box.input.checked).map((x) => x.t) } });
        onDone();
        showSecret('Webhook signing secret', 'Use this secret to verify the X-Calendar-Signature header. It is shown only once.', data.secret);
      } },
    ] });
  }
  async function openDeliveries(w) {
    try {
      const d = await get(`/v1/admin/webhooks/${w.id}/deliveries?limit=50`);
      modal({ title: 'Recent deliveries', wide: true, body: table(['Event', 'State', 'Attempts', 'Last error', 'Next try', 'Created'], d.items.map((x) => h('tr', null,
        h('td', null, x.eventType), h('td', null, statusBadge(x.state)), h('td', null, String(x.attempts)),
        h('td', { class: 'small' }, x.lastError || '—'), h('td', { class: 'small' }, x.state === 'pending' ? fmtDT(x.nextRunAt) : '—'), h('td', { class: 'small' }, fmtDT(x.createdAt)))), 'No deliveries yet.') });
    } catch (e) { toastErr(e); }
  }

  // ------------------------------------------------------------------ users
  PAGES.users = async (el, alive) => {
    const d = await get('/v1/admin/users');
    if (!alive()) return;
    const reload = () => render();
    el.replaceChildren(
      pageHead('Users', 'People who can sign in to this dashboard. Year-table changes need two calendar admins.',
        h('button', { class: 'primary', onclick: () => openUser(null, reload) }, 'New user')),
      table(['Email', 'Role', 'Status', 'Last sign-in', 'Created', ''], d.items.map((u) => h('tr', null,
        h('td', null, h('strong', null, u.email), u.id === S.me.id ? h('span', { class: 'muted' }, ' (you)') : null),
        h('td', null, u.role.replace('_', ' '), h('div', { class: 'small muted' }, ROLE_HELP[u.role] || '')),
        h('td', null, u.disabled ? badge('disabled', 'bad') : badge('active', 'ok')),
        h('td', { class: 'small' }, fmtDT(u.lastLoginAt)),
        h('td', { class: 'small' }, fmtDT(u.createdAt)),
        h('td', { class: 'actions' }, h('button', { class: 'sm', onclick: () => openUser(u, reload) }, 'Edit'))))));
  };
  function openUser(u, onDone) {
    const email = h('input', { type: 'email', value: u ? u.email : '', disabled: !!u });
    const role = selectEl(ROLES.map((r) => [r, `${r.replace('_', ' ')} — ${ROLE_HELP[r]}`]), u ? u.role : 'editor');
    const password = h('input', { type: 'password', autocomplete: 'new-password', placeholder: u ? 'leave empty to keep' : 'at least 12 characters' });
    const disabled = checkbox('Disabled (cannot sign in)', u ? u.disabled : false);
    modal({ title: u ? 'Edit user' : 'New user', body: h('div', { class: 'stack' },
      field('Email', email), field('Role', role),
      field(u ? 'New password' : 'Password', password, 'At least 12 characters. Changing it signs the user out everywhere.'),
      u ? disabled.el : null),
    actions: [
      { label: 'Cancel', onClick: () => true },
      { label: 'Save', class: 'primary', onClick: async () => {
        if (u) {
          const body = {};
          if (role.value !== u.role) body.role = role.value;
          if (disabled.input.checked !== u.disabled) body.disabled = disabled.input.checked;
          if (password.value) body.password = password.value;
          if (!Object.keys(body).length) return true;
          await api('PATCH', `/v1/admin/users/${u.id}`, { body });
          if (u.id === S.me.id && body.password) { toast('Password changed. Please sign in again.', 'ok'); clearSession(); render(); return true; }
        } else {
          await api('POST', '/v1/admin/users', { body: { email: email.value.trim(), role: role.value, password: password.value } });
        }
        toast('User saved.', 'ok');
        onDone();
      } },
    ] });
  }

  // ------------------------------------------------------------------ audit log
  PAGES.audit = async (el) => {
    const f = {
      entity: selectEl([['', 'All types'], 'event', 'category', 'year_draft', 'ui_config', 'api_client', 'webhook', 'admin_user'], ''),
      action: h('input', { placeholder: 'Action, e.g. event.publish' }),
      entityId: h('input', { placeholder: 'Item id' }),
    };
    const tbody = h('tbody');
    const more = h('button', { class: 'hidden', onclick: () => load(false) }, 'Load more');
    let cursor = null;
    const load = async (reset) => {
      if (reset) { cursor = null; tbody.replaceChildren(); }
      try {
        const d = await get('/v1/admin/audit' + qs({ entity: f.entity.value, action: f.action.value.trim(), entityId: f.entityId.value.trim(), limit: 50, cursor }));
        for (const a of d.items) {
          const detail = h('tr', { class: 'hidden' }, h('td', { colspan: 5 }, h('div', { class: 'grid2' },
            h('div', null, h('div', { class: 'small muted' }, 'Before'), h('pre', null, a.before ? JSON.stringify(a.before, null, 2) : '—')),
            h('div', null, h('div', { class: 'small muted' }, 'After'), h('pre', null, a.after ? JSON.stringify(a.after, null, 2) : '—'))),
          a.requestId ? h('div', { class: 'small muted', style: { marginTop: '6px' } }, 'Request id: ' + a.requestId) : null));
          tbody.append(h('tr', null,
            h('td', { class: 'small' }, fmtDT(a.at)),
            h('td', null, a.actorEmail || 'system', a.actorIp ? h('div', { class: 'small muted' }, a.actorIp) : null),
            h('td', { class: 'mono' }, a.action),
            h('td', { class: 'small' }, a.entity, h('div', { class: 'mono muted' }, a.entityId)),
            h('td', { class: 'actions' }, h('button', { class: 'sm', onclick: () => detail.classList.toggle('hidden') }, 'Details'))), detail);
        }
        if (!tbody.children.length) tbody.append(h('tr', null, h('td', { colspan: 5, class: 'empty' }, 'No entries.')));
        cursor = d.nextCursor;
        more.classList.toggle('hidden', !cursor);
      } catch (e) { toastErr(e); }
    };
    let t;
    Object.values(f).forEach((x) => x.addEventListener(x.tagName === 'SELECT' ? 'change' : 'input', () => { clearTimeout(t); t = setTimeout(() => load(true), 300); }));
    el.replaceChildren(
      pageHead('Audit log', 'Every change made in this dashboard or through the admin API, newest first.'),
      h('div', { class: 'filters' }, f.entity, f.action, f.entityId),
      h('div', { class: 'table-wrap' }, h('table', null, h('thead', null, h('tr', null, ['When', 'Who', 'Action', 'Item', ''].map((x) => h('th', null, x)))), tbody)),
      h('div', { class: 'row end', style: { marginTop: '10px' } }, more));
    await load(true);
  };

  // ------------------------------------------------------------------ start
  loadSession();
  render();
})();
