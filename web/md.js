// Small, safe Markdown renderer for agent output (untrusted text).
// It never uses innerHTML: every node is made with createElement and text
// goes in through createTextNode, so raw HTML in the input only shows as text.
// API: Md.render(text) -> <div class="md"> element, Md.code(text) -> raw text in a code block, Md.plain(text) -> string,
//      Md.oneLine(text, max) -> string, Md.safeUrl(url) -> string ('' if unsafe).
(function () {
  'use strict';

  const SAFE_SCHEMES = ['http', 'https', 'mailto'];

  function safeUrl(u) {
    const clean = String(u == null ? '' : u).replace(/[\u0000-\u0020\u007f-\u009f\u200b-\u200f\u2028\u2029\ufeff]/g, '');
    const m = /^([a-z][a-z0-9+.-]*):/i.exec(clean);
    if (!m || !SAFE_SCHEMES.includes(m[1].toLowerCase())) return '';
    return clean;
  }

  function mk(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function mkLink(href, parent) {
    const a = mk('a');
    a.href = href;
    a.target = '_blank';
    a.rel = 'noopener noreferrer';
    parent.appendChild(a);
    return a;
  }

  // ---------- inline ----------
  const LINK_RE = /^\[((?:[^\[\]\\]|\\.)*)\]\(\s*<?([^\s)>]*)>?(?:\s+(?:"[^"]*"|'[^']*'))?\s*\)/;
  const AUTO_RE = /^https?:\/\/[^\s<>()]*[^\s<>().,;:!?'"*_`]/i;
  const ESCAPABLE = '\\`*_{}[]()#+-.!|>~<"\'&';

  function inline(s, parent) {
    let buf = '';
    const flush = () => { if (buf) { parent.appendChild(document.createTextNode(buf)); buf = ''; } };
    let i = 0;
    while (i < s.length) {
      const c = s[i];
      if (c === '\\' && i + 1 < s.length && ESCAPABLE.includes(s[i + 1])) { buf += s[i + 1]; i += 2; continue; }
      if (c === '\u0001') {
        const j = s.indexOf('\u0002', i + 1);
        if (j > 0) { flush(); parent.appendChild(mk('code', '', s.slice(i + 1, j))); i = j + 1; continue; }
      }
      if (c === '`') {
        let n = 1; while (s[i + n] === '`') n++;
        const fence = '`'.repeat(n);
        let j = i + n, found = -1;
        while ((j = s.indexOf(fence, j)) >= 0) {
          if (s[j + n] !== '`' && s[j - 1] !== '`') { found = j; break; }
          j++;
        }
        if (found > i + n) {
          flush();
          let code = s.slice(i + n, found).replace(/\n/g, ' ');
          if (code.length > 2 && code.startsWith(' ') && code.endsWith(' ')) code = code.slice(1, -1);
          parent.appendChild(mk('code', '', code));
          i = found + n; continue;
        }
        buf += fence; i += n; continue;
      }
      if (c === '[') {
        const m = LINK_RE.exec(s.slice(i));
        if (m) {
          flush();
          const href = safeUrl(m[2]);
          const host = href ? mkLink(href, parent) : parent;
          inline(m[1], host);
          i += m[0].length; continue;
        }
      }
      if ((c === 'h' || c === 'H') && !/[A-Za-z0-9]/.test(s[i - 1] || ' ')) {
        const m = AUTO_RE.exec(s.slice(i));
        if (m) {
          flush();
          const href = safeUrl(m[0]);
          if (href) mkLink(href, parent).textContent = m[0]; else parent.appendChild(document.createTextNode(m[0]));
          i += m[0].length; continue;
        }
      }
      if (c === '*' || c === '_') {
        const prev = s[i - 1] || ' ';
        // '_' only opens emphasis after a space or an opening bracket, so paths like foo/__init__.py stay as written
        const canOpen = c === '*' || /[\s(\[{"']/.test(prev) || i === 0;
        if (canOpen) {
          if (s[i + 1] === c) {
            const j = s.indexOf(c + c, i + 2);
            if (j > i + 2 && !/\s/.test(s[i + 2]) && (c === '*' || !/[A-Za-z0-9]/.test(s[j + 2] || ' '))) {
              flush(); inline(s.slice(i + 2, j), parent.appendChild(mk('strong'))); i = j + 2; continue;
            }
          } else if (s[i + 1] && !/\s/.test(s[i + 1])) {
            let j = i + 1, found = -1;
            while ((j = s.indexOf(c, j)) >= 0) {
              if (s[j + 1] !== c && s[j - 1] !== c && !/\s/.test(s[j - 1]) && (c === '*' || !/[A-Za-z0-9]/.test(s[j + 1] || ' '))) { found = j; break; }
              j++;
            }
            if (found > i + 1) { flush(); inline(s.slice(i + 1, found), parent.appendChild(mk('em'))); i = found + 1; continue; }
          }
        }
      }
      if (c === '~' && s[i + 1] === '~') {
        const j = s.indexOf('~~', i + 2);
        if (j > i + 2) { flush(); inline(s.slice(i + 2, j), parent.appendChild(mk('del'))); i = j + 2; continue; }
      }
      if (c === '\n') { flush(); parent.appendChild(mk('br')); i++; continue; }
      buf += c; i++;
    }
    flush();
  }

  // ---------- blocks ----------
  const FENCE_RE = /^\s{0,3}(`{3,}|~{3,})\s*([\w+#.-]*)[^`]*$/;
  const HEAD_RE = /^\s{0,3}(#{1,6})\s+(.*?)(?:\s+#+)?\s*$/;
  const HR_RE = /^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/;
  const LIST_RE = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;
  const QUOTE_RE = /^\s{0,3}>\s?(.*)$/;
  const SEP_RE = /^\s*\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*)*\|?\s*$/;

  const indentOf = l => /^\s*/.exec(l.replace(/\t/g, '    '))[0].length;
  const blank = l => !l.trim();

  function splitRow(line) {
    let t = line.trim();
    if (t.startsWith('|')) t = t.slice(1);
    if (t.endsWith('|') && !t.endsWith('\\|')) t = t.slice(0, -1);
    const cells = []; let cur = '';
    for (let i = 0; i < t.length; i++) {
      if (t[i] === '\\' && t[i + 1] === '|') { cur += '|'; i++; }
      else if (t[i] === '|') { cells.push(cur.trim()); cur = ''; }
      else cur += t[i];
    }
    cells.push(cur.trim());
    return cells;
  }

  function startsBlock(lines, i) {
    const l = lines[i];
    return FENCE_RE.test(l) || HEAD_RE.test(l) || HR_RE.test(l) || QUOTE_RE.test(l) || LIST_RE.test(l) || isTable(lines, i);
  }

  function isTable(lines, i) {
    return i + 1 < lines.length && lines[i].includes('|') && lines[i + 1].includes('-') && SEP_RE.test(lines[i + 1]) &&
      (lines[i + 1].includes('|') || lines[i].trim().startsWith('|'));
  }

  function codeBlock(code, lang, out) {
    const box = mk('div', 'code');
    if (lang) box.dataset.lang = lang;
    const btn = mk('button', 'copy', 'Copy');
    btn.type = 'button';
    btn.setAttribute('aria-label', 'Copy code');
    btn.addEventListener('click', e => {
      e.stopPropagation();
      copyText(code).then(() => { btn.textContent = 'Copied'; }, () => { btn.textContent = 'Copy failed'; });
      clearTimeout(btn._t);
      btn._t = setTimeout(() => { btn.textContent = 'Copy'; }, 1500);
    });
    // stop clicks on code from toggling a parent (speech bubbles, chronicle)
    box.addEventListener('click', e => e.stopPropagation());
    const pre = mk('pre');
    pre.tabIndex = 0;
    pre.appendChild(mk('code', '', code));
    if (lang) box.appendChild(mk('span', 'lang', lang));
    box.append(btn, pre);
    out.appendChild(box);
  }

  function copyText(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) return navigator.clipboard.writeText(text);
    return new Promise((ok, no) => {
      const ta = document.createElement('textarea');
      ta.value = text; ta.setAttribute('readonly', '');
      ta.style.cssText = 'position:fixed;left:-9999px;top:0';
      document.body.appendChild(ta); ta.select();
      let done = false;
      try { done = document.execCommand('copy'); } catch (e) { done = false; }
      ta.remove();
      done ? ok() : no(new Error('copy failed'));
    });
  }

  function blocks(lines, out) {
    let i = 0;
    while (i < lines.length) {
      const l = lines[i];
      if (blank(l)) { i++; continue; }

      let m = FENCE_RE.exec(l);
      if (m) {
        const mark = m[1], lang = m[2];
        const body = []; i++;
        while (i < lines.length) {
          const t = lines[i].trim();
          if (t.length >= mark.length && t[0] === mark[0] && t === mark[0].repeat(t.length)) { i++; break; }
          body.push(lines[i]); i++;
        }
        codeBlock(body.join('\n'), lang, out);
        continue;
      }

      m = HEAD_RE.exec(l);
      if (m) {
        const h = mk('h' + Math.min(m[1].length + 2, 5));
        inline(m[2], h); out.appendChild(h); i++; continue;
      }

      if (HR_RE.test(l)) { out.appendChild(mk('hr')); i++; continue; }

      if (QUOTE_RE.test(l)) {
        const q = []; 
        while (i < lines.length && (m = QUOTE_RE.exec(lines[i]))) { q.push(m[1]); i++; }
        const bq = mk('blockquote'); blocks(q, bq); out.appendChild(bq); continue;
      }

      if (isTable(lines, i)) {
        const head = splitRow(lines[i]);
        const aligns = splitRow(lines[i + 1]).map(c => c.startsWith(':') && c.endsWith(':') ? 'center' : c.endsWith(':') ? 'right' : c.startsWith(':') ? 'left' : '');
        i += 2;
        const wrap = mk('div', 'tablewrap'), table = mk('table'), thead = mk('thead'), tbody = mk('tbody');
        const addRow = (cells, tag, parent) => {
          const tr = mk('tr');
          for (let k = 0; k < head.length; k++) {
            const cell = mk(tag);
            if (aligns[k]) cell.style.textAlign = aligns[k];
            inline(cells[k] || '', cell); tr.appendChild(cell);
          }
          parent.appendChild(tr);
        };
        addRow(head, 'th', thead);
        while (i < lines.length && !blank(lines[i]) && lines[i].includes('|')) { addRow(splitRow(lines[i]), 'td', tbody); i++; }
        table.append(thead, tbody); wrap.appendChild(table); out.appendChild(wrap); continue;
      }

      m = LIST_RE.exec(l);
      if (m) {
        const base = indentOf(l), ordered = /\d/.test(m[2][0]);
        const list = mk(ordered ? 'ol' : 'ul');
        if (ordered) { const n = parseInt(m[2], 10); if (n !== 1) list.start = n; }
        while (i < lines.length) {
          const im = LIST_RE.exec(lines[i]);
          if (!im || indentOf(lines[i]) !== base || /\d/.test(im[2][0]) !== ordered) break;
          const item = [im[3]]; i++;
          while (i < lines.length) {
            const nl = lines[i];
            if (blank(nl)) {
              let k = i; while (k < lines.length && blank(lines[k])) k++;
              if (k < lines.length && indentOf(lines[k]) > base) { item.push(''); i++; continue; }
              break;
            }
            if (indentOf(nl) > base) { item.push(nl); i++; continue; }
            break;
          }
          const li = mk('li');
          blocks(item.map((s, k) => k === 0 ? s : s.replace(/^\s{1,4}/, '')), li);
          // unwrap a leading paragraph so tight items have no extra margin
          const lead = li.firstElementChild;
          if (lead && lead.tagName === 'P' && li.children.length === 1) {
            while (lead.firstChild) li.insertBefore(lead.firstChild, lead);
            lead.remove();
          }
          list.appendChild(li);
          while (i < lines.length && blank(lines[i]) && i + 1 < lines.length && LIST_RE.test(lines[i + 1]) && indentOf(lines[i + 1]) === base) i++;
        }
        out.appendChild(list); continue;
      }

      // paragraph
      const para = [l]; i++;
      while (i < lines.length && !blank(lines[i]) && !startsBlock(lines, i)) { para.push(lines[i]); i++; }
      const p = mk('p');
      inline(para.map(s => s.trim()).join('\n'), p);
      out.appendChild(p);
    }
  }

  function render(text) {
    const root = mk('div', 'md');
    const src = String(text == null ? '' : text).replace(/\r\n?/g, '\n').replace(/\t/g, '    ');
    blocks(src.split('\n'), root);
    return root;
  }

  // ---------- plain text ----------
  function plain(text) {
    let s = String(text == null ? '' : text).replace(/\r\n?/g, '\n');
    s = s.replace(/\u0001([^\u0002]*)\u0002/g, '$1');
    s = s.split('\n').filter(l => !FENCE_RE.test(l) && !HR_RE.test(l) && !(l.includes('-') && SEP_RE.test(l) && /[|]/.test(l))).map(l =>
      l.replace(HEAD_RE, '$2').replace(/^\s{0,3}>\s?/, '').replace(/^\s*(?:[-*+]|\d{1,9}[.)])\s+/, '').replace(/\s*\|\s*/g, ' ')
    ).join(' ');
    s = s.replace(/!?\[((?:[^\[\]\\]|\\.)*)\]\([^)]*\)/g, '$1')
      .replace(/`+([^`]*)`+/g, '$1')
      .replace(/(\*\*|__|~~)(.+?)\1/g, '$2')
      .replace(/(^|[^\w*])([*_])(?=\S)(.+?)\2(?![\w])/g, '$1$3')
      .replace(/\\([\\`*_{}\[\]()#+\-.!|>~])/g, '$1');
    return s.replace(/\s+/g, ' ').trim();
  }

  function oneLine(text, max) {
    const s = plain(text);
    max = max || 90;
    return s.length > max ? s.slice(0, max - 1).trimEnd() + '…' : s;
  }

  // raw text (tool output, stack traces) shown as it is, in a code block with a Copy button
  function code(text) {
    const root = mk('div', 'md');
    codeBlock(String(text == null ? '' : text), '', root);
    return root;
  }

  window.Md = { render, code, plain, oneLine, safeUrl };
})();
