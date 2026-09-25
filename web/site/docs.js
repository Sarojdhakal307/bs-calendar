'use strict';
// Developer docs: turns <script type="text/plain"> samples into highlighted code blocks,
// and drives the sidebar (scroll position, mobile drawer). site.js handles tabs, copy and the menu.
(() => {
  const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

  const KEYWORDS = new Set(('import export from default function return const let var if else for of in new await async ' +
    'try catch throw type interface extends class public private readonly as typeof keyof null undefined true false ' +
    'while break continue switch case this void').split(' '));

  // A small tokenizer: comments, strings, numbers, keywords and JSX/HTML tag names.
  function highlight(src, lang) {
    if (lang === 'json') {
      return esc(src).replace(/("(?:\\.|[^"\\])*")(\s*:)?|\b(-?\d+(?:\.\d+)?)\b|\b(true|false|null)\b/g, (m, str, colon, num, lit) => {
        if (str) return colon ? `<span class="tk-p">${str}</span>${colon}` : `<span class="tk-s">${str}</span>`;
        if (num) return `<span class="tk-n">${num}</span>`;
        return `<span class="tk-k">${lit}</span>`;
      });
    }
    if (lang === 'bash') {
      return src.split('\n').map((line) => {
        const i = line.search(/(^|\s)#/);
        const code = i >= 0 ? line.slice(0, i) : line;
        const comment = i >= 0 ? line.slice(i) : '';
        const body = esc(code).replace(/("[^"]*"|'[^']*')/g, '<span class="tk-s">$1</span>')
          .replace(/^(\s*)(curl|npx|npm|cd)\b/, '$1<span class="tk-k">$2</span>')
          .replace(/(^|\s)(-[A-Za-z]+)\b/g, '$1<span class="tk-p">$2</span>')
          .replace(/^([A-Z_][A-Z0-9_]*)=/, '<span class="tk-t">$1</span>=');
        return body + (comment ? `<span class="tk-c">${esc(comment)}</span>` : '');
      }).join('\n');
    }
    if (lang === 'css') {
      return esc(src).replace(/(\/\*[\s\S]*?\*\/)|([.#][\w-]+(?:\[[^\]]*\])?)|(#[0-9a-fA-F]{3,8}\b|\b\d+(?:\.\d+)?(?:px|%|em|rem)?\b)|([\w-]+)(?=:\s)/g,
        (m, c, sel, num, prop) => {
          if (c) return `<span class="tk-c">${c}</span>`;
          if (sel) return `<span class="tk-t">${sel}</span>`;
          if (num) return `<span class="tk-n">${num}</span>`;
          return `<span class="tk-p">${prop}</span>`;
        });
    }
    const re = /(\/\/[^\n]*|\/\*[\s\S]*?\*\/|\{\/\*[\s\S]*?\*\/\})|("(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'|`(?:\\.|[^`\\])*`)|(<\/?)([A-Za-z][\w.]*)|\b(\d+(?:_\d+)*(?:\.\d+)?)\b|\b([A-Za-z_$][\w$]*)\b/g;
    let out = '';
    let last = 0;
    let m;
    while ((m = re.exec(src))) {
      out += esc(src.slice(last, m.index));
      last = re.lastIndex;
      const [all, comment, str, open, tag, num, word] = m;
      if (comment) out += `<span class="tk-c">${esc(comment)}</span>`;
      else if (str) out += `<span class="tk-s">${esc(str)}</span>`;
      else if (tag) out += esc(open) + `<span class="tk-t">${tag}</span>`;
      else if (num) out += `<span class="tk-n">${num}</span>`;
      else if (word && KEYWORDS.has(word)) out += `<span class="tk-k">${word}</span>`;
      else if (word && /^[A-Z]/.test(word)) out += `<span class="tk-t">${word}</span>`;
      else out += esc(all);
    }
    return out + esc(src.slice(last));
  }

  const LABELS = { ts: 'TypeScript', tsx: 'TSX', js: 'JavaScript', json: 'JSON', bash: 'Shell', css: 'CSS' };

  document.querySelectorAll('script[type="text/plain"][data-lang]').forEach((s) => {
    const lang = s.dataset.lang;
    const src = s.textContent.replace(/^\n+|\s+$/g, '').replaceAll('{ORIGIN}', location.origin);
    const box = document.createElement('div');
    box.className = 'code';
    const head = document.createElement('div');
    head.className = 'code-head';
    const name = document.createElement('span');
    name.textContent = s.dataset.title || '';
    const label = document.createElement('span');
    label.className = 'lang';
    label.textContent = LABELS[lang] || lang;
    const copy = document.createElement('button');
    copy.type = 'button';
    copy.className = 'copy';
    copy.textContent = 'Copy';
    head.append(name, label, copy);
    const pre = document.createElement('pre');
    const code = document.createElement('code');
    code.innerHTML = highlight(src, lang); // input is escaped by highlight()
    pre.append(code);
    box.append(head, pre);
    s.replaceWith(box);
  });

  // Sidebar: highlight the section in view, and a drawer on small screens.
  const side = document.getElementById('side');
  const toggle = side.querySelector('.side-toggle');
  const current = document.getElementById('side-current');
  const links = [...side.querySelectorAll('.side-links a[href^="#"]')];
  const byId = new Map(links.map((a) => [a.getAttribute('href').slice(1), a]));

  const setOpen = (open) => { side.classList.toggle('open', open); toggle.setAttribute('aria-expanded', String(open)); };
  toggle.addEventListener('click', () => setOpen(!side.classList.contains('open')));
  links.forEach((a) => a.addEventListener('click', () => setOpen(false)));

  function activate(id) {
    const a = byId.get(id);
    if (!a) return;
    links.forEach((x) => x.classList.toggle('active', x === a));
    current.textContent = a.textContent;
    if (window.matchMedia('(min-width: 961px)').matches) {
      const r = a.getBoundingClientRect();
      const sr = side.getBoundingClientRect();
      if (r.top < sr.top || r.bottom > sr.bottom) a.scrollIntoView({ block: 'nearest' });
    }
  }

  const sections = [...byId.keys()].map((id) => document.getElementById(id)).filter(Boolean);
  const onScroll = () => {
    const y = 140;
    let active = sections[0];
    for (const s of sections) {
      if (s.getBoundingClientRect().top <= y) active = s;
      else break;
    }
    if (window.innerHeight + window.scrollY >= document.body.scrollHeight - 4) active = sections[sections.length - 1];
    activate(active.id);
  };
  let ticking = false;
  window.addEventListener('scroll', () => {
    if (ticking) return;
    ticking = true;
    requestAnimationFrame(() => { ticking = false; onScroll(); });
  }, { passive: true });
  onScroll();
})();
