// Self-test harness for dom_prelude.js. Plain JS, run with:
//   node internal/engine/testdata/prelude_harness.js
// Builds a MOCK `__sw` host over an in-memory tree, loads the prelude into a
// fresh global (vm.runInNewContext) and exercises the browser API surface.
'use strict';
var fs = require('fs');
var path = require('path');
var vm = require('vm');

// ---------------------------------------------------------------- mock tree
function makeHost() {
  var nodes = new Map(); // id -> node
  var seq = 0;
  var logs = [], mutations = [], httpCalls = [], navigations = [], dialogs = [];
  var storage = { local: new Map(), session: new Map() };
  var cookie = '';
  var url = 'https://example.test/path/page?q=1#frag';
  var refs = new Map();

  function mk(type, props) { var n = Object.assign({ id: ++seq, type: type, parent: null, children: [], attrs: [], tag: '', data: '', value: undefined, checked: false, selectedIndex: -1 }, props); nodes.set(n.id, n); return n; }
  var doc = mk(9, {});
  var VOID = { area: 1, base: 1, br: 1, col: 1, embed: 1, hr: 1, img: 1, input: 1, link: 1, meta: 1, source: 1, track: 1, wbr: 1 };
  var RAW = { script: 1, style: 1 };

  function getAttr(n, name) { for (var i = 0; i < n.attrs.length; i++) if (n.attrs[i][0] === name) return n.attrs[i][1]; return null; }
  function setAttr(n, name, v) { for (var i = 0; i < n.attrs.length; i++) if (n.attrs[i][0] === name) { n.attrs[i][1] = v; return; } n.attrs.push([name, v]); }
  function removeAttr(n, name) { n.attrs = n.attrs.filter(function (a) { return a[0] !== name; }); }
  function detach(n) { if (n.parent) { var p = n.parent; p.children.splice(p.children.indexOf(n), 1); n.parent = null; } }
  function append(p, c) { detach(c); c.parent = p; p.children.push(c); }
  function insertBefore(p, c, ref) { detach(c); c.parent = p; var i = ref ? p.children.indexOf(ref) : -1; if (i < 0) p.children.push(c); else p.children.splice(i, 0, c); }
  function attached(n) { while (n) { if (n === doc) return true; n = n.parent; } return false; }
  function textContent(n) { if (n.type === 3) return n.data; if (n.type === 8) return ''; return n.children.map(textContent).join(''); }
  function esc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); }
  function escAttr(s) { return String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;'); }
  function serialize(n) {
    if (n.type === 3) return n.parent && RAW[n.parent.tag] ? n.data : esc(n.data);
    if (n.type === 8) return '<!--' + n.data + '-->';
    if (n.type === 10) return '<!DOCTYPE ' + n.tag + '>';
    if (n.type === 9) return n.children.map(serialize).join('');
    var s = '<' + n.tag + n.attrs.map(function (a) { return ' ' + a[0] + '="' + escAttr(a[1]) + '"'; }).join('') + '>';
    if (VOID[n.tag]) return s;
    return s + n.children.map(serialize).join('') + '</' + n.tag + '>';
  }
  function unesc(s) { return s.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&nbsp;/g, ' ').replace(/&amp;/g, '&'); }
  // tiny HTML fragment parser: tags, attributes, text, comments, void elements, raw text for script/style
  function parseFragment(html) {
    var root = { children: [] };
    var stack = [root];
    var i = 0, re = /<!--([\s\S]*?)--!?>|<!(doctype)[^>]*>|<\/([a-zA-Z][\w:-]*)\s*>|<([a-zA-Z][\w:-]*)((?:\s+[^\s"'>\/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'>]+))?)*)\s*(\/?)>/gi, m;
    function text(t) { if (!t) return; var n = mk(3, { data: unesc(t) }); var p = stack[stack.length - 1]; n.parent = p === root ? null : p; p.children.push(n); }
    while ((m = re.exec(html))) {
      text(html.slice(i, m.index)); i = re.lastIndex;
      var cur = stack[stack.length - 1];
      if (m[1] !== undefined) { var c = mk(8, { data: m[1] }); c.parent = cur === root ? null : cur; cur.children.push(c); }
      else if (m[2]) { var dt = mk(10, { tag: 'html' }); dt.parent = cur === root ? null : cur; cur.children.push(dt); }
      else if (m[3]) { var tag = m[3].toLowerCase(); for (var k = stack.length - 1; k > 0; k--) if (stack[k].tag === tag) { stack.length = k; break; } }
      else if (m[4]) {
        var el = mk(1, { tag: m[4].toLowerCase() });
        var attrRe = /([^\s"'>\/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?/g, am;
        while ((am = attrRe.exec(m[5] || ''))) el.attrs.push([am[1].toLowerCase(), unesc(am[2] !== undefined ? am[2] : am[3] !== undefined ? am[3] : am[4] !== undefined ? am[4] : '')]);
        el.parent = cur === root ? null : cur; cur.children.push(el);
        if (RAW[el.tag]) { var close = html.indexOf('</' + el.tag, i); if (close < 0) close = html.length; var raw = mk(3, { data: html.slice(i, close) }); raw.parent = el; el.children.push(raw); re.lastIndex = i = close; }
        else if (!VOID[el.tag] && !m[6]) stack.push(el);
      }
    }
    text(html.slice(i));
    return root.children;
  }
  // selector engine: tag, #id, .class, [attr], [attr=v], [attr~=v], [attr^=v], [attr$=v], [attr*=v], :not(), :first-child, :last-child, :nth-child(n), descendant, child, adjacent, sibling, comma groups, *
  function parseSelector(sel) {
    sel = sel.trim();
    if (!sel) throw new Error('empty selector');
    var groups = [], buf = '', depth = 0, inStr = null;
    for (var i = 0; i < sel.length; i++) { var ch = sel[i]; if (inStr) { if (ch === inStr) inStr = null; buf += ch; continue; } if (ch === '"' || ch === "'") inStr = ch; if (ch === '(' || ch === '[') depth++; if (ch === ')' || ch === ']') depth--; if (ch === ',' && depth === 0) { groups.push(buf); buf = ''; continue; } buf += ch; }
    groups.push(buf);
    return groups.map(parseComplex);
  }
  function parseComplex(s) {
    s = s.trim();
    var parts = [], comb = ' ', cur = '', depth = 0, inStr = null;
    for (var i = 0; i < s.length; i++) {
      var ch = s[i];
      if (inStr) { cur += ch; if (ch === inStr) inStr = null; continue; }
      if (ch === '"' || ch === "'") { inStr = ch; cur += ch; continue; }
      if (ch === '(' || ch === '[') depth++; if (ch === ')' || ch === ']') depth--;
      if (depth === 0 && /[\s>+~]/.test(ch)) {
        if (cur) { parts.push({ comb: comb, compound: parseCompound(cur) }); cur = ''; comb = ' '; }
        if (ch !== ' ' && ch !== '\t' && ch !== '\n') comb = ch; else if (comb === ' ') comb = ' ';
        continue;
      }
      cur += ch;
    }
    if (cur) parts.push({ comb: comb, compound: parseCompound(cur) });
    if (!parts.length) throw new Error('bad selector');
    return parts;
  }
  function parseCompound(s) {
    var tests = [], re = /^(\*|[a-zA-Z][\w-]*)|^#([\w-]+)|^\.([\w-]+)|^\[\s*([\w-]+)\s*(?:([~^$*|]?=)\s*(?:"([^"]*)"|'([^']*)'|([^\]\s]+)))?\s*\]|^:not\((.*?)\)(?=[^)]*$|$)|^:([\w-]+)(?:\(([^)]*)\))?/;
    var m;
    while (s.length) {
      m = re.exec(s);
      if (!m) throw new Error('bad selector token: ' + s);
      s = s.slice(m[0].length);
      if (m[1] !== undefined) { if (m[1] !== '*') tests.push({ t: 'tag', v: m[1].toLowerCase() }); }
      else if (m[2]) tests.push({ t: 'attr', n: 'id', op: '=', v: m[2] });
      else if (m[3]) tests.push({ t: 'attr', n: 'class', op: '~=', v: m[3] });
      else if (m[4]) tests.push({ t: 'attr', n: m[4].toLowerCase(), op: m[5] || null, v: m[6] !== undefined ? m[6] : m[7] !== undefined ? m[7] : m[8] });
      else if (m[9] !== undefined) tests.push({ t: 'not', v: parseSelector(m[9]) });
      else if (m[10]) tests.push({ t: 'pseudo', n: m[10], a: m[11] });
    }
    return tests;
  }
  function matchCompound(n, tests) {
    for (var i = 0; i < tests.length; i++) {
      var t = tests[i];
      if (t.t === 'tag') { if (n.tag !== t.v) return false; }
      else if (t.t === 'attr') {
        var v = getAttr(n, t.n);
        if (v === null) return false;
        if (t.op === '=') { if (v !== t.v) return false; }
        else if (t.op === '~=') { if (v.split(/\s+/).indexOf(t.v) < 0) return false; }
        else if (t.op === '^=') { if (v.indexOf(t.v) !== 0) return false; }
        else if (t.op === '$=') { if (v.slice(-t.v.length) !== t.v) return false; }
        else if (t.op === '*=') { if (v.indexOf(t.v) < 0) return false; }
        else if (t.op === '|=') { if (v !== t.v && v.indexOf(t.v + '-') !== 0) return false; }
      }
      else if (t.t === 'not') { if (matchesGroups(n, t.v)) return false; }
      else if (t.t === 'pseudo') {
        var sibs = n.parent ? n.parent.children.filter(function (c) { return c.type === 1; }) : [n];
        switch (t.n) {
          case 'first-child': if (sibs[0] !== n) return false; break;
          case 'last-child': if (sibs[sibs.length - 1] !== n) return false; break;
          case 'only-child': if (sibs.length !== 1) return false; break;
          case 'nth-child': { var idx = sibs.indexOf(n) + 1; var a = t.a.trim(); if (a === 'odd') { if (idx % 2 !== 1) return false; } else if (a === 'even') { if (idx % 2 !== 0) return false; } else if (/^\d+$/.test(a)) { if (idx !== +a) return false; } else { var mm = /^(-?\d*)n(?:\s*([+-])\s*(\d+))?$/.exec(a.replace(/\s/g, '')); if (!mm) return false; var A = mm[1] === '' || mm[1] === '+' ? 1 : mm[1] === '-' ? -1 : +mm[1], B = mm[2] ? (mm[2] === '-' ? -1 : 1) * +mm[3] : 0; if (A === 0) { if (idx !== B) return false; } else if ((idx - B) / A < 0 || (idx - B) % A !== 0) return false; } break; }
          case 'checked': if (!n.checked && getAttr(n, 'selected') === null) return false; break;
          case 'disabled': if (getAttr(n, 'disabled') === null) return false; break;
          case 'enabled': if (getAttr(n, 'disabled') !== null) return false; break;
          case 'empty': if (n.children.length) return false; break;
          case 'root': if (n.parent !== doc) return false; break;
          case 'is': case 'where': if (!matchesGroups(n, parseSelector(t.a))) return false; break;
          case 'has': if (!descendants(n).some(function (d) { return matchesGroups(d, parseSelector(t.a)); })) return false; break;
          default: return false;
        }
      }
    }
    return true;
  }
  function matchComplex(n, parts, idx) {
    if (idx === undefined) idx = parts.length - 1;
    if (!matchCompound(n, parts[idx].compound)) return false;
    if (idx === 0) return true;
    var comb = parts[idx].comb;
    if (comb === ' ') { for (var p = n.parent; p && p.type === 1; p = p.parent) if (matchComplex(p, parts, idx - 1)) return true; return false; }
    if (comb === '>') return !!(n.parent && n.parent.type === 1 && matchComplex(n.parent, parts, idx - 1));
    var sibs = n.parent ? n.parent.children.filter(function (c) { return c.type === 1; }) : [];
    var i = sibs.indexOf(n);
    if (comb === '+') return i > 0 && matchComplex(sibs[i - 1], parts, idx - 1);
    if (comb === '~') { for (var k = i - 1; k >= 0; k--) if (matchComplex(sibs[k], parts, idx - 1)) return true; return false; }
    return false;
  }
  function matchesGroups(n, groups) { if (n.type !== 1) return false; return groups.some(function (g) { return matchComplex(n, g); }); }
  function descendants(n) { var out = []; (function walk(x) { x.children.forEach(function (c) { if (c.type === 1) out.push(c); walk(c); }); })(n); return out; }
  function compile(sel) { try { return parseSelector(String(sel)); } catch (e) { var err = new Error("'" + sel + "' is not a valid selector."); err.name = 'SyntaxError'; throw err; } }
  function node(id) { var n = nodes.get(id); if (!n) throw new Error('unknown node id ' + id); return n; }
  function root(id) { return id ? node(id) : doc; }

  var host = {
    _nodes: nodes, _doc: doc, _logs: logs, _mutations: mutations, _http: httpCalls, _navigations: navigations, _dialogs: dialogs, _storage: storage, _now: 0, _viewport: { width: 1024, height: 768, dsf: 2, mobile: false, dark: true },
    _httpHandler: null, _dialogHandler: null, _pointTarget: 0,
    docId: function () { return doc.id; },
    nodeType: function (id) { var n = nodes.get(id); return n ? n.type : 0; },
    tagName: function (id) { var n = nodes.get(id); return n && (n.type === 1 || n.type === 10) ? n.tag : ''; },
    parent: function (id) { var n = nodes.get(id); return n && n.parent ? n.parent.id : 0; },
    children: function (id) { return node(id).children.map(function (c) { return c.id; }); },
    firstChild: function (id) { var c = node(id).children; return c.length ? c[0].id : 0; },
    lastChild: function (id) { var c = node(id).children; return c.length ? c[c.length - 1].id : 0; },
    nextSibling: function (id) { var n = node(id); if (!n.parent) return 0; var s = n.parent.children; var i = s.indexOf(n); return s[i + 1] ? s[i + 1].id : 0; },
    prevSibling: function (id) { var n = node(id); if (!n.parent) return 0; var s = n.parent.children; var i = s.indexOf(n); return s[i - 1] ? s[i - 1].id : 0; },
    createElement: function (tag) { return mk(1, { tag: String(tag).toLowerCase() }).id; },
    createText: function (d) { return mk(3, { data: String(d) }).id; },
    createComment: function (d) { return mk(8, { data: String(d) }).id; },
    appendChild: function (p, c) { append(node(p), node(c)); return c; },
    insertBefore: function (p, c, r) { insertBefore(node(p), node(c), r ? node(r) : null); return c; },
    removeChild: function (p, c) { var cn = node(c); if (cn.parent !== node(p)) throw new Error('not a child'); detach(cn); return c; },
    cloneNode: function (id, deep) { var src = node(id); function cl(n) { var c = mk(n.type, { tag: n.tag, data: n.data, attrs: n.attrs.map(function (a) { return a.slice(); }), value: n.value, checked: n.checked, selectedIndex: n.selectedIndex }); if (deep) n.children.forEach(function (k) { append(c, cl(k)); }); return c; } return cl(src).id; },
    attached: function (id) { var n = nodes.get(id); return !!n && attached(n); },
    nodeValue: function (id) { return node(id).data; },
    setNodeValue: function (id, s) { node(id).data = String(s); },
    textContent: function (id) { return textContent(node(id)); },
    setTextContent: function (id, s) { var n = node(id); n.children.forEach(function (c) { c.parent = null; }); n.children = []; if (s !== '') append(n, mk(3, { data: String(s) })); },
    innerHTML: function (id) { return node(id).children.map(serialize).join(''); },
    setInnerHTML: function (id, html) { var n = node(id); n.children.forEach(function (c) { c.parent = null; }); n.children = []; parseFragment(String(html)).forEach(function (c) { append(n, c); }); },
    outerHTML: function (id) { return serialize(node(id)); },
    insertAdjacentHTML: function (id, pos, html) { var n = node(id); var kids = parseFragment(String(html)); if (pos === 'beforebegin') kids.forEach(function (k) { insertBefore(n.parent, k, n); }); else if (pos === 'afterbegin') kids.reverse().forEach(function (k) { insertBefore(n, k, n.children[0] || null); }); else if (pos === 'beforeend') kids.forEach(function (k) { append(n, k); }); else { var next = n.parent.children[n.parent.children.indexOf(n) + 1] || null; kids.forEach(function (k) { insertBefore(n.parent, k, next); }); } },
    getAttr: function (id, name) { return getAttr(node(id), name); },
    setAttr: function (id, name, v) { setAttr(node(id), name, String(v)); },
    removeAttr: function (id, name) { removeAttr(node(id), name); },
    hasAttr: function (id, name) { return getAttr(node(id), name) !== null; },
    attrNames: function (id) { return node(id).attrs.map(function (a) { return a[0]; }); },
    querySelector: function (rootId, sel) { var g = compile(sel); var r = root(rootId); var d = descendants(r); for (var i = 0; i < d.length; i++) if (matchesGroups(d[i], g)) return d[i].id; return 0; },
    querySelectorAll: function (rootId, sel) { var g = compile(sel); return descendants(root(rootId)).filter(function (n) { return matchesGroups(n, g); }).map(function (n) { return n.id; }); },
    matches: function (id, sel) { return matchesGroups(node(id), compile(sel)); },
    getElementById: function (id) { var d = descendants(doc); for (var i = 0; i < d.length; i++) if (getAttr(d[i], 'id') === id) return d[i].id; return 0; },
    elementsByTag: function (rootId, tag) { return descendants(root(rootId)).filter(function (n) { return tag === '*' || n.tag === tag; }).map(function (n) { return n.id; }); },
    elementsByClass: function (rootId, cls) { var want = cls.trim().split(/\s+/); return descendants(root(rootId)).filter(function (n) { var have = (getAttr(n, 'class') || '').split(/\s+/); return want.every(function (w) { return have.indexOf(w) >= 0; }); }).map(function (n) { return n.id; }); },
    elementsByName: function (name) { return descendants(doc).filter(function (n) { return getAttr(n, 'name') === name; }).map(function (n) { return n.id; }); },
    getValue: function (id) { var n = node(id); if (n.tag === 'select') { var opts = descendants(n).filter(function (o) { return o.tag === 'option'; }); var i = host.selectedIndex(id); return i >= 0 && opts[i] ? (getAttr(opts[i], 'value') !== null ? getAttr(opts[i], 'value') : textContent(opts[i])) : ''; } if (n.value !== undefined) return n.value; return n.tag === 'textarea' ? textContent(n) : (getAttr(n, 'value') || ''); },
    setValue: function (id, v) { node(id).value = String(v); },
    getChecked: function (id) { var n = node(id); return n.checked !== null && n.checkedDirty ? n.checked : getAttr(n, 'checked') !== null; },
    setChecked: function (id, b) { var n = node(id); n.checked = !!b; n.checkedDirty = true; },
    selectedIndex: function (id) { var n = node(id); if (n.selectedIndex >= 0 || n.selectedDirty) return n.selectedIndex; var opts = descendants(n).filter(function (o) { return o.tag === 'option'; }); for (var i = 0; i < opts.length; i++) if (getAttr(opts[i], 'selected') !== null) return i; return opts.length && getAttr(n, 'multiple') === null ? 0 : -1; },
    setSelectedIndex: function (id, i) { var n = node(id); n.selectedIndex = i; n.selectedDirty = true; },
    submitForm: function (id) { navigations.push({ submit: id }); },
    log: function (level, text) { logs.push([level, text]); },
    url: function () { return url; },
    setUrl: function (u) { url = u; },
    navigate: function (u) { navigations.push(u); },
    userAgent: function () { return 'Mozilla/5.0 (X11; Linux x86_64) Sweef/1.0'; },
    viewport: function () { return host._viewport; },
    now: function () { return host._now; },
    http: function (method, u, headers, body) { httpCalls.push({ method: method, url: u, headers: headers, body: body }); if (host._httpHandler) return host._httpHandler(method, u, headers, body); return { status: 404, statusText: 'Not Found', headers: {}, body: '', url: u }; },
    storageGet: function (k, key) { return storage[k].has(key) ? storage[k].get(key) : null; },
    storageSet: function (k, key, v) { storage[k].set(key, String(v)); },
    storageRemove: function (k, key) { storage[k].delete(key); },
    storageClear: function (k) { storage[k].clear(); },
    storageKeys: function (k) { return Array.from(storage[k].keys()); },
    cookie: function () { return cookie; },
    setCookie: function (s) { var kv = s.split(';')[0].trim(); if (!kv) return; var name = kv.split('=')[0]; var parts = cookie ? cookie.split('; ').filter(function (c) { return c.split('=')[0] !== name; }) : []; if (!/max-age=0|expires=Thu, 01 Jan 1970/i.test(s)) parts.push(kv); cookie = parts.join('; '); },
    dialog: function (kind, message, dflt) { dialogs.push([kind, message, dflt]); return host._dialogHandler ? host._dialogHandler(kind, message, dflt) : { accepted: true, value: dflt || '' }; },
    mutation: function (op, id) { mutations.push([op, id]); },
    box: function (id) { var n = node(id); return { x: 10, y: 20, width: n.tag === 'div' ? 100 : 50, height: 30 }; },
    elementFromPoint: function (x, y) { return host._pointTarget || 0; },
    refOf: function (id) { if (!refs.has(id)) refs.set(id, refs.size + 1); return refs.get(id); },
    _load: function (html) { doc.children.forEach(function (c) { c.parent = null; }); doc.children = []; parseFragment(html).forEach(function (c) { append(doc, c); }); }
  };
  return host;
}

// ---------------------------------------------------------------- runner
var preludePath = path.join(__dirname, '..', 'dom_prelude.js');
var preludeSrc = fs.readFileSync(preludePath, 'utf8');
var failures = 0, passes = 0, section = '';
function check(name, cond, extra) { if (cond) { passes++; } else { failures++; console.log('FAIL [' + section + '] ' + name + (extra !== undefined ? ' -- ' + extra : '')); } }
function eq(name, a, b) { check(name, a === b, JSON.stringify(a) + ' !== ' + JSON.stringify(b)); }
function throws(name, fn, nameOrRe) { try { fn(); check(name, false, 'did not throw'); } catch (e) { check(name, !nameOrRe || (typeof nameOrRe === 'string' ? e.name === nameOrRe : nameOrRe.test(String(e))), String(e)); } }

function boot(html) {
  var host = makeHost();
  host._load(html || '<!DOCTYPE html><html><head><title>T</title></head><body></body></html>');
  var ctx = vm.createContext({ __sw: host });
  vm.runInContext('var globalThis = this;', ctx);
  vm.runInContext(preludeSrc, ctx, { filename: 'dom_prelude.js' });
  // evaluate page code in the fresh global
  ctx.__run = function (src) { return vm.runInContext(src, ctx); };
  return { host: host, g: ctx, run: function (src) { return vm.runInContext(src, ctx, { filename: 'page.js' }); } };
}
// drain microtasks of the inner context: they share node's loop, so await a macrotask tick
function tick() { return new Promise(function (r) { setImmediate(r); }); }

async function main() {
  var w, g, host;
  // ---------------------------------------------------------------- basics
  section = 'globals';
  w = boot('<!DOCTYPE html><html><head><title>Hello</title></head><body><div id="app" class="a b"><p>hi <b>there</b></p><input id="in" type="text" value="x"><input id="cb" type="checkbox"><select id="sel"><option value="1">One</option><option value="2" selected>Two</option></select></div></body></html>');
  g = w.g; host = w.host;
  eq('window === globalThis', w.run('window === globalThis'), true);
  eq('self/top/parent', w.run('self === window && top === window && parent === window && frames === window'), true);
  eq('document.nodeType', w.run('document.nodeType'), 9);
  eq('document instanceof Document', w.run('document instanceof Document && document instanceof Node && document instanceof EventTarget'), true);
  eq('documentElement', w.run('document.documentElement.tagName'), 'HTML');
  eq('head/body', w.run('document.head.tagName + document.body.tagName'), 'HEADBODY');
  eq('title', w.run('document.title'), 'Hello');
  w.run('document.title = "New"'); eq('title set', w.run('document.querySelector("title").textContent'), 'New');
  eq('readyState loading', w.run('document.readyState'), 'loading');
  eq('doctype', w.run('document.doctype && document.doctype.nodeType'), 10);
  eq('location.href', w.run('location.href'), 'https://example.test/path/page?q=1#frag');
  eq('location parts', w.run('[location.protocol, location.host, location.pathname, location.search, location.hash, location.origin].join("|")'), 'https:|example.test|/path/page|?q=1|#frag|https://example.test');
  eq('navigator.userAgent', w.run('navigator.userAgent.indexOf("Sweef") > 0'), true);
  eq('navigator.userAgentData undefined', w.run('navigator.userAgentData'), undefined);
  eq('innerWidth', w.run('innerWidth + "x" + innerHeight + "@" + devicePixelRatio'), '1024x768@2');
  eq('screen', w.run('screen.width'), 1024);
  eq('matchMedia dark', w.run('matchMedia("(prefers-color-scheme: dark)").matches'), true);
  eq('matchMedia other', w.run('matchMedia("(min-width: 2000px)").matches'), false);
  eq('matchMedia API', w.run('typeof matchMedia("x").addListener === "function" && typeof matchMedia("x").addEventListener === "function"'), true);
  var globalsList = ['Node', 'Element', 'HTMLElement', 'HTMLInputElement', 'HTMLSelectElement', 'HTMLTextAreaElement', 'HTMLFormElement', 'HTMLAnchorElement', 'HTMLButtonElement', 'HTMLImageElement', 'HTMLScriptElement', 'HTMLStyleElement', 'HTMLDivElement', 'HTMLSpanElement', 'HTMLTemplateElement', 'HTMLCanvasElement', 'SVGElement', 'Text', 'Comment', 'DocumentFragment', 'Document', 'EventTarget', 'Event', 'CustomEvent', 'MouseEvent', 'KeyboardEvent', 'InputEvent', 'FocusEvent', 'UIEvent', 'PointerEvent', 'TouchEvent', 'ProgressEvent', 'ErrorEvent', 'PromiseRejectionEvent', 'NodeList', 'HTMLCollection', 'DOMTokenList', 'NamedNodeMap', 'CSSStyleDeclaration', 'Storage', 'Location', 'History', 'Navigator', 'Screen', 'MutationObserver', 'IntersectionObserver', 'ResizeObserver', 'requestAnimationFrame', 'cancelAnimationFrame', 'requestIdleCallback', 'setTimeout', 'setInterval', 'clearTimeout', 'clearInterval', 'queueMicrotask', 'fetch', 'Headers', 'Request', 'Response', 'AbortController', 'AbortSignal', 'XMLHttpRequest', 'FormData', 'URL', 'URLSearchParams', 'TextEncoder', 'TextDecoder', 'atob', 'btoa', 'structuredClone', 'matchMedia', 'getComputedStyle', 'scrollTo', 'scrollBy', 'scroll', 'performance', 'crypto', 'console', 'alert', 'confirm', 'prompt', 'customElements', 'DOMParser', 'Image', 'Option', 'Audio', 'getSelection', 'Intl', 'WebSocket', 'Worker', 'Notification', 'MessageChannel', 'localStorage', 'sessionStorage', 'location', 'history', 'navigator', 'screen', 'CustomElementRegistry', 'DOMException', 'Blob', 'File', 'FileReader', 'DataTransfer'];
  globalsList.forEach(function (n) { check('global ' + n, w.run('typeof ' + n + ' !== "undefined"')); });
  ['__swDispatch', '__swDispatchKey', '__swMouse', '__swTouch', '__swDrag', '__swWheel', '__swSetValueFromGo', '__swRunTimers', '__swFlushMutationObservers', '__swFireLifecycle', '__swSetDocument', '__swDefaultAction', '__swSerialize', '__swSetCurrentScript', '__swIsThenable'].forEach(function (n) { check('export ' + n, w.run('typeof ' + n + ' === "function"')); });

  // ---------------------------------------------------------------- wrappers & instanceof
  section = 'wrappers';
  eq('same wrapper', w.run('document.getElementById("app") === document.querySelector("#app")'), true);
  w.run('document.getElementById("app").__expando = 42');
  eq('expando persists', w.run('document.body.firstChild.__expando'), 42);
  eq('instanceof chain', w.run('var a = document.getElementById("app"); a instanceof HTMLDivElement && a instanceof HTMLElement && a instanceof Element && a instanceof Node && a instanceof EventTarget'), true);
  eq('input class', w.run('document.getElementById("in") instanceof HTMLInputElement'), true);
  eq('select class', w.run('document.getElementById("sel") instanceof HTMLSelectElement'), true);
  eq('text node class', w.run('document.querySelector("p").firstChild instanceof Text && document.querySelector("p").firstChild.nodeType === 3'), true);
  eq('nodeName upper', w.run('document.querySelector("p").nodeName + document.querySelector("p").tagName + document.querySelector("p").localName'), 'PPp');
  eq('text nodeName', w.run('document.querySelector("p").firstChild.nodeName'), '#text');
  eq('ownerDocument', w.run('document.querySelector("p").ownerDocument === document && document.ownerDocument === null'), true);
  eq('constructor.name', w.run('document.getElementById("in").constructor.name'), 'HTMLInputElement');
  eq('toString', w.run('String(document.getElementById("in"))'), '[object HTMLInputElement]');
  eq('illegal ctor', w.run('try { new HTMLElement(); "no" } catch (e) { e.name }'), 'TypeError');
  eq('Text ctor', w.run('new Text("abc").data + new Comment("c").nodeType'), 'abc8');

  // ---------------------------------------------------------------- tree ops
  section = 'tree';
  w.run('var d = document.createElement("DIV"); d.id = "made"; d.textContent = "t"; document.body.appendChild(d);');
  eq('createElement lowercases, tagName upper', w.run('d.tagName + "/" + d.localName'), 'DIV/div');
  eq('appended', w.run('document.body.lastChild === d && d.parentNode === document.body && d.isConnected'), true);
  eq('childNodes array-like', w.run('var cn = document.getElementById("app").childNodes; cn.length === 4 && cn.item(0).tagName === "P" && typeof cn.forEach === "function" && Array.from(cn).length === 4 && [...cn].length === 4 && cn instanceof NodeList'), true);
  eq('childNodes live-ish', w.run('var app = document.getElementById("app"); var cn2 = app.childNodes; var before = cn2.length; app.appendChild(document.createElement("i")); cn2.length === before + 1'), true);
  eq('children', w.run('app.children.length === 5 && app.children instanceof HTMLCollection && app.firstElementChild.tagName === "P" && app.lastElementChild.tagName === "I" && app.children[1].id === "in"'), true);
  eq('siblings', w.run('app.children[0].nextElementSibling.id === "in" && app.children[1].previousElementSibling.tagName === "P" && document.querySelector("b").previousSibling.nodeType === 3'), true);
  eq('insertBefore', w.run('var s = document.createElement("span"); app.insertBefore(s, app.children[1]); app.children[1] === s && s.nextSibling.id === "in"'), true);
  eq('removeChild', w.run('app.removeChild(s); s.parentNode === null && app.children.length === 5'), true);
  throws('removeChild not child', function () { w.run('app.removeChild(s)'); }, 'NotFoundError');
  throws('appendChild non-node', function () { w.run('app.appendChild("str")'); }, 'TypeError');
  throws('hierarchy', function () { w.run('document.querySelector("p").appendChild(app)'); }, 'HierarchyRequestError');
  eq('replaceChild', w.run('var r = document.createElement("em"); var old = app.children[0]; app.replaceChild(r, old); app.children[0] === r && old.parentNode === null'), true);
  eq('cloneNode deep', w.run('var c = app.cloneNode(true); c !== app && c.id === "app" && c.children.length === app.children.length && !c.isConnected'), true);
  eq('cloneNode shallow', w.run('app.cloneNode(false).childNodes.length'), 0);
  eq('contains', w.run('document.body.contains(app) && app.contains(app) && !app.contains(document.body) && document.contains(app)'), true);
  eq('compareDocumentPosition', w.run('document.body.compareDocumentPosition(app) & 16 ? "contains" : "no"'), 'contains');
  eq('textContent', w.run('document.getElementById("app").children[0].textContent = "<x>"; document.getElementById("app").children[0].innerHTML'), '&lt;x&gt;');
  w.run('var box = document.createElement("div"); box.id = "box"; document.body.appendChild(box);');
  eq('innerHTML set', w.run('box.innerHTML = "<ul><li>1</li><li class=\\"z\\">2</li></ul>"; box.children.length === 1 && box.querySelectorAll("li").length === 2 && box.querySelector("li.z").textContent === "2"'), true);
  eq('innerHTML roundtrip', w.run('box.innerHTML'), '<ul><li>1</li><li class="z">2</li></ul>');
  eq('outerHTML', w.run('box.querySelector("li.z").outerHTML'), '<li class="z">2</li>');
  eq('insertAdjacentHTML', w.run('box.querySelector("li").insertAdjacentHTML("afterend", "<li>1.5</li>"); box.querySelectorAll("li")[1].textContent'), '1.5');
  eq('insertAdjacentElement', w.run('var li0 = document.createElement("li"); li0.textContent = "0"; box.querySelector("li").insertAdjacentElement("beforebegin", li0); box.querySelector("li").textContent'), '0');
  eq('append/prepend/remove/before/after', w.run('var ul = box.querySelector("ul"); ul.append("txt", document.createElement("li")); ul.prepend(document.createElement("li")); ul.lastElementChild.previousSibling.nodeType === 3 && ul.firstChild.tagName === "LI" && (ul.firstChild.remove(), ul.children.length === 5) && (ul.children[0].before(document.createElement("hr")), ul.children[0].tagName === "HR") && (ul.children[0].after("x"), ul.childNodes[1].data === "x")'), true);
  eq('replaceChildren', w.run('ul.replaceChildren(document.createElement("li")); ul.childNodes.length'), 1);
  eq('replaceWith', w.run('var nn = document.createElement("ol"); ul.replaceWith(nn); box.querySelector("ol") === nn && !ul.parentNode'), true);
  eq('fragment', w.run('var f = document.createDocumentFragment(); var fa = document.createElement("a"); var fb = document.createElement("b"); f.appendChild(fa); f.appendChild(fb); f.childNodes.length === 2 && fa.parentNode === f && f.nodeType === 11 && f.firstChild === fa && f.querySelector("b") === fb && (nn.appendChild(f), f.childNodes.length === 0 && nn.children.length === 2 && fa.parentNode === nn)'), true);
  eq('template content', w.run('var t = document.createElement("template"); t.innerHTML = "<p class=tp>x</p><span></span>"; var ct = t.content; ct instanceof DocumentFragment && ct.childNodes.length === 2 && ct.firstChild.className === "tp" && t.content === ct && ct.cloneNode(true).childNodes.length === 2 && ct.childNodes.length === 2'), true);
  eq('createElementNS svg', w.run('var sv = document.createElementNS("http://www.w3.org/2000/svg", "svg"); var pth = document.createElementNS("http://www.w3.org/2000/svg", "linearGradient"); sv.appendChild(pth); sv instanceof SVGElement && pth instanceof SVGElement && !(pth instanceof HTMLElement) && pth.namespaceURI === "http://www.w3.org/2000/svg" && pth.tagName === "linearGradient" && sv.tagName === "svg" && document.createElement("div").namespaceURI === "http://www.w3.org/1999/xhtml"'), true);
  eq('svg setAttribute preserves case', w.run('pth.setAttribute("viewBox", "0 0 1 1"); pth.getAttribute("viewBox")'), '0 0 1 1');
  eq('parsed svg class', w.run('document.body.insertAdjacentHTML("beforeend", "<svg id=psvg><path d=M0/></svg>"); document.getElementById("psvg") instanceof SVGElement && document.querySelector("#psvg path") instanceof SVGElement'), true);
  eq('normalize', w.run('var nd = document.createElement("div"); nd.appendChild(document.createTextNode("a")); nd.appendChild(document.createTextNode("b")); nd.appendChild(document.createTextNode("")); nd.normalize(); nd.childNodes.length === 1 && nd.firstChild.data === "ab"'), true);
  eq('splitText', w.run('var tn = nd.firstChild; var t2 = tn.splitText(1); tn.data === "a" && t2.data === "b" && t2.previousSibling === tn'), true);
  eq('getRootNode', w.run('app.getRootNode() === document && document.createElement("x").getRootNode().tagName === "X"'), true);
  eq('TreeWalker', w.run('var tw = document.createTreeWalker(app, NodeFilter.SHOW_ELEMENT); var names = []; while (tw.nextNode()) names.push(tw.currentNode.tagName); names.join(",")'), 'EM,INPUT,INPUT,SELECT,OPTION,OPTION,I');
  eq('NodeIterator', w.run('var ni = document.createNodeIterator(app, NodeFilter.SHOW_ELEMENT); var c0 = 0; while (ni.nextNode()) c0++; c0'), 8);

  // ---------------------------------------------------------------- selectors
  section = 'query';
  eq('qs tag', w.run('document.querySelector("ol").tagName'), 'OL');
  eq('qs id', w.run('document.querySelector("#in").id'), 'in');
  eq('qs class', w.run('document.querySelector(".a.b").id'), 'app');
  eq('qs attr', w.run('document.querySelector("[type=checkbox]").id'), 'cb');
  eq('qs combinators', w.run('document.querySelectorAll("#box > ol > a, #box > ol > b").length'), 2);
  eq('qsa NodeList', w.run('var ql = document.querySelectorAll("input"); ql instanceof NodeList && ql.length === 2 && ql[1].id === "cb" && Array.prototype.map.call(ql, function (x) { return x.id; }).join() === "in,cb"'), true);
  eq('qs scoped', w.run('app.querySelector("input").id'), 'in');
  eq('qs none', w.run('document.querySelector(".nope")'), null);
  throws('qs bad', function () { w.run('document.querySelector("!!!")'); }, 'SyntaxError');
  eq('matches/closest', w.run('var cb = document.getElementById("cb"); cb.matches("input[type=checkbox]") && !cb.matches("div") && cb.closest("div") === app && cb.closest("#cb") === cb && cb.closest(".zzz") === null'), true);
  eq('getElementsByTagName live', w.run('var ems = document.getElementsByTagName("em"); var n0 = ems.length; document.body.appendChild(document.createElement("em")); ems.length === n0 + 1 && ems instanceof HTMLCollection'), true);
  eq('getElementsByClassName', w.run('document.getElementsByClassName("a b")[0].id'), 'app');
  eq('getElementsByName', w.run('document.body.insertAdjacentHTML("beforeend", "<input name=nm>"); document.getElementsByName("nm").length'), 1);
  eq('namedItem', w.run('document.body.insertAdjacentHTML("beforeend", "<form id=f1 name=fname></form>"); document.forms.namedItem("fname").id === "f1" && document.forms.fname.id === "f1" && document.forms.length >= 1'), true);

  // ---------------------------------------------------------------- attributes
  section = 'attrs';
  eq('get/set/has/remove', w.run('var e = document.createElement("div"); e.setAttribute("Data-X", "1"); e.getAttribute("data-x") === "1" && e.hasAttribute("DATA-X") && e.getAttributeNames().join() === "data-x" && (e.removeAttribute("data-x"), !e.hasAttribute("data-x") && e.getAttribute("data-x") === null)'), true);
  eq('setAttribute coerces', w.run('e.setAttribute("n", 5); e.setAttribute("u", undefined); e.getAttribute("n") + e.getAttribute("u")'), '5undefined');
  throws('setAttribute invalid', function () { w.run('e.setAttribute("a b", 1)'); }, 'InvalidCharacterError');
  eq('toggleAttribute', w.run('e.toggleAttribute("hidden") && e.hidden && !e.toggleAttribute("hidden") && !e.hidden && e.toggleAttribute("x", true) && !e.toggleAttribute("y", false)'), true);
  eq('id/className reflect', w.run('e.id = "idd"; e.className = "c1 c2"; e.getAttribute("id") === "idd" && e.getAttribute("class") === "c1 c2" && e.className === "c1 c2"'), true);
  eq('classList', w.run('e.classList.add("c3", "c4"); e.classList.remove("c1"); e.classList.toggle("c2"); e.classList.toggle("c5", true); e.classList.replace("c3", "c6"); e.className === "c6 c4 c5" && e.classList.contains("c4") && e.classList.length === 3 && e.classList[0] === "c6" && e.classList.item(1) === "c4" && Array.from(e.classList).join() === "c6,c4,c5" && String(e.classList) === "c6 c4 c5" && e.classList.value === "c6 c4 c5" && e.classList instanceof DOMTokenList'), true);
  throws('classList empty token', function () { w.run('e.classList.add("")'); }, 'SyntaxError');
  throws('classList space token', function () { w.run('e.classList.add("a b")'); }, 'InvalidCharacterError');
  eq('classList.value set', w.run('e.classList.value = "q"; e.className'), 'q');
  eq('dataset', w.run('e.dataset.fooBar = "v"; e.getAttribute("data-foo-bar") === "v" && e.dataset.fooBar === "v" && "fooBar" in e.dataset && Object.keys(e.dataset).join() === "fooBar" && (delete e.dataset.fooBar, !e.hasAttribute("data-foo-bar")) && e.dataset.nope === undefined && JSON.stringify(e.dataset) === "{}"'), true);
  eq('style', w.run('e.style.color = "red"; e.style.backgroundColor = "blue"; e.style.setProperty("--x", "1"); e.getAttribute("style") === "color: red; background-color: blue; --x: 1;" && e.style.color === "red" && e.style.getPropertyValue("background-color") === "blue" && e.style.length === 3 && e.style[0] === "color" && e.style.getPropertyValue("--x") === "1" && e.style instanceof CSSStyleDeclaration && e.style === e.style'), true);
  eq('style.cssText', w.run('e.style.cssText = "display:none !important; margin : 0"; e.getAttribute("style") === "display: none !important; margin: 0;" && e.style.display === "none" && e.style.getPropertyPriority("display") === "important" && e.style.cssText === "display: none !important; margin: 0;"'), true);
  eq('style remove', w.run('e.style.removeProperty("display"); e.style.margin = ""; e.hasAttribute("style")'), false);
  eq('style attr read-through', w.run('e.setAttribute("style", "width: 10px"); e.style.width'), '10px');
  eq('style vendor / float', w.run('e.style.webkitTransform = "x"; e.style.cssFloat = "left"; e.getAttribute("style")'), 'width: 10px; -webkit-transform: x; float: left;');
  eq('attributes NamedNodeMap', w.run('e.attributes.length === 6 && e.attributes[0].name === "n" && e.attributes.getNamedItem("style").value.indexOf("width") === 0 && e.attributes.style.value === e.getAttribute("style") && Array.from(e.attributes).length === 6 && e.attributes instanceof NamedNodeMap'), true);
  eq('reflected bool', w.run('var inp = document.getElementById("in"); inp.disabled = true; inp.required = 1; inp.readOnly = false; inp.hasAttribute("disabled") && inp.getAttribute("disabled") === "" && inp.required && !inp.readOnly && (inp.disabled = false, !inp.hasAttribute("disabled"))'), true);
  eq('reflected int', w.run('inp.tabIndex === -1 && (inp.tabIndex = 3, inp.getAttribute("tabindex") === "3") && inp.maxLength === -1 && (inp.maxLength = 5, inp.maxLength === 5)'), true);
  eq('reflected str', w.run('inp.placeholder = "ph"; inp.name = "nn"; inp.title = "tt"; inp.getAttribute("placeholder") + inp.name + inp.title + inp.lang'), 'phnntt');
  eq('htmlFor', w.run('var lb = document.createElement("label"); lb.htmlFor = "in"; lb.getAttribute("for") === "in" && lb.control === inp'), true);
  eq('href resolved', w.run('var an = document.createElement("a"); an.href = "../x?y#z"; an.href === "https://example.test/x?y#z" && an.getAttribute("href") === "../x?y#z" && an.pathname === "/x" && an.hash === "#z" && an.host === "example.test" && String(an) === an.href'), true);
  eq('href unset', w.run('document.createElement("a").href'), '');
  eq('src resolved', w.run('var im = new Image(10, 20); im.src = "/i.png"; im.src === "https://example.test/i.png" && im.width === 10 && im instanceof HTMLImageElement'), true);
  eq('type default', w.run('document.createElement("input").type + document.createElement("button").type + document.createElement("textarea").type'), 'textsubmittextarea');
  eq('contentEditable', w.run('var ce = document.createElement("div"); ce.contentEditable === "inherit" && !ce.isContentEditable && (ce.contentEditable = "true", ce.isContentEditable)'), true);
  eq('draggable/spellcheck', w.run('ce.draggable = true; ce.getAttribute("draggable") === "true" && ce.draggable && !ce.spellcheck'), true);
  eq('aria reflect', w.run('ce.ariaLabel = "L"; ce.getAttribute("aria-label") === "L" && ce.ariaLabel === "L" && ce.ariaHidden === null'), true);
  eq('innerText', w.run('ce.innerText = "a<b"; ce.innerText === "a<b" && ce.innerHTML === "a&lt;b"'), true);
  eq('hidden', w.run('ce.hidden = true; ce.hasAttribute("hidden")'), true);

  // ---------------------------------------------------------------- form state
  section = 'forms';
  eq('value live', w.run('inp.value === "x" && inp.defaultValue === "x" && (inp.value = "y", inp.value === "y" && inp.getAttribute("value") === "x")'), true);
  eq('value accessor on prototype', w.run('var dsc = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value"); !!(dsc && typeof dsc.get === "function" && typeof dsc.set === "function" && dsc.configurable)'), true);
  eq('checked accessor on prototype', w.run('var dsc2 = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "checked"); !!(dsc2 && dsc2.get && dsc2.set)'), true);
  eq('textarea/select value accessors', w.run('!!Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set && !!Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set && !inp.hasOwnProperty("value")'), true);
  eq('React inputValueTracking wrap', w.run('(function () { var node = inp; var d = Object.getOwnPropertyDescriptor(node.constructor.prototype, "value"); var current = "" + node.value; var seen = []; Object.defineProperty(node, "value", { configurable: true, get: function () { return d.get.call(this); }, set: function (v) { current = "" + v; seen.push(v); d.set.call(this, v); } }); node.value = "tracked"; var ok = node.value === "tracked" && current === "tracked" && seen.length === 1; delete node.value; return ok && node.value === "tracked"; })()'), true);
  eq('checked live', w.run('var cbx = document.getElementById("cb"); !cbx.checked && (cbx.checked = true, cbx.checked && !cbx.hasAttribute("checked") && !cbx.defaultChecked)'), true);
  eq('select', w.run('var sel = document.getElementById("sel"); sel.selectedIndex === 1 && sel.value === "2" && sel.options.length === 2 && sel.options[1].selected && !sel.options[0].selected && sel.options[0].text === "One" && sel.options[0].index === 0 && sel.selectedOptions[0].value === "2"'), true);
  eq('select set', w.run('sel.selectedIndex = 0; sel.value === "1" && sel.options[0].selected && (sel.options[1].selected = true, sel.selectedIndex === 1)'), true);
  eq('Option ctor', w.run('var op = new Option("Three", "3"); sel.add(op); sel.length === 3 && op.value === "3" && op instanceof HTMLOptionElement'), true);
  eq('form elements', w.run('var fm = document.createElement("form"); fm.innerHTML = "<input name=a value=1><input type=checkbox name=c checked><input type=checkbox name=d><select name=s><option>o1<option selected>o2</select><textarea name=t>tt</textarea><button name=b>go</button>"; document.body.appendChild(fm); fm.elements.length === 6 && fm.length === 6 && fm.elements[0].form === fm && fm.elements.namedItem("t").value === "tt" && fm.a === undefined'), true);
  eq('FormData from form', w.run('var fdd = new FormData(fm); Array.from(fdd.entries()).map(function (p) { return p.join("="); }).join("&")'), 'a=1&c=on&s=o2&t=tt');
  eq('form reset', w.run('fm.elements[0].value = "changed"; fm.elements[2].checked = true; fm.reset(); fm.elements[0].value === "1" && !fm.elements[2].checked'), true);
  eq('form submit event + host', w.run('var subs = []; fm.addEventListener("submit", function (ev) { subs.push(ev.submitter === fm.elements[5]); ev.preventDefault(); }); fm.requestSubmit(fm.elements[5]); subs.length === 1 && subs[0]'), true);
  eq('prevented submit no nav', host._navigations.length, 0);
  w.run('fm.submit()'); eq('form.submit host', host._navigations.length === 1 && host._navigations[0].submit !== undefined, true);
  eq('fieldset elements', w.run('var fs = document.createElement("fieldset"); fs.innerHTML = "<input><input>"; fs.elements.length'), 2);

  // ---------------------------------------------------------------- events
  section = 'events';
  eq('Event ctor', w.run('var ev = new Event("x", { bubbles: true, cancelable: true }); ev.type === "x" && ev.bubbles && ev.cancelable && !ev.defaultPrevented && ev.target === null && ev.currentTarget === null && ev.eventPhase === 0 && typeof ev.timeStamp === "number" && ev.isTrusted === false && ev instanceof Event'), true);
  throws('Event no new', function () { w.run('Event("x")'); }, 'TypeError');
  throws('Event no args', function () { w.run('new Event()'); }, 'TypeError');
  eq('CustomEvent', w.run('var cev = new CustomEvent("c", { detail: { a: 1 } }); cev.detail.a === 1 && cev instanceof Event && cev instanceof CustomEvent && new CustomEvent("d").detail === null'), true);
  eq('MouseEvent', w.run('var me = new MouseEvent("click", { clientX: 5, clientY: 6, button: 1, bubbles: true }); me.clientX === 5 && me.x === 5 && me.pageY === 6 && me.button === 1 && me instanceof UIEvent && me instanceof MouseEvent && me.relatedTarget === null'), true);
  eq('KeyboardEvent', w.run('var ke = new KeyboardEvent("keydown", { key: "a" }); ke.key === "a" && ke.keyCode === 65 && ke.which === 65 && !ke.shiftKey && new KeyboardEvent("k", { key: "Enter" }).keyCode === 13'), true);
  eq('PointerEvent chain', w.run('var pe = new PointerEvent("pointerdown", { pointerId: 2 }); pe instanceof MouseEvent && pe.pointerId === 2 && pe.pointerType === "mouse"'), true);
  eq('createEvent/initEvent', w.run('var ie = document.createEvent("Event"); ie.initEvent("foo", true, true); ie.type === "foo" && ie.bubbles && ie.cancelable && document.createEvent("MouseEvents") instanceof MouseEvent && document.createEvent("HTMLEvents") instanceof Event'), true);
  throws('createEvent bad', function () { w.run('document.createEvent("Nope")'); }, 'NotSupportedError');
  var r = w.run([
    'var log = []; var tgt = document.createElement("button"); var mid = document.createElement("div"); mid.appendChild(tgt); document.body.appendChild(mid);',
    'function L(n) { return function (ev) { log.push(n + ":" + ev.eventPhase + ":" + (ev.currentTarget === window ? "W" : ev.currentTarget === document ? "D" : ev.currentTarget.tagName)); }; }',
    'window.addEventListener("click", L("wc"), true); window.addEventListener("click", L("wb")); document.addEventListener("click", L("dc"), { capture: true }); document.addEventListener("click", L("db"));',
    'mid.addEventListener("click", L("mc"), true); mid.addEventListener("click", L("mb")); tgt.addEventListener("click", L("tc"), true); tgt.addEventListener("click", L("tb"));',
    'var same = function () { log.push("same"); }; tgt.addEventListener("click", same); tgt.addEventListener("click", same); tgt.addEventListener("click", same, false); tgt.addEventListener("click", same, { capture: false });',
    'tgt.onclick = function (ev) { log.push("on:" + (this === tgt)); };',
    'var res = tgt.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));',
    'log.join(" ") + " => " + res'].join('\n'));
  eq('propagation order', r, 'wc:1:W dc:1:D mc:1:DIV tc:2:BUTTON tb:2:BUTTON same on:true mb:3:DIV db:3:D wb:3:W => true');
  eq('target/currentTarget after dispatch', w.run('var ev2 = new Event("q"); tgt.dispatchEvent(ev2); ev2.target === tgt && ev2.currentTarget === null && ev2.eventPhase === 0 && ev2.composedPath().length === 6 && ev2.composedPath()[0] === tgt && ev2.composedPath()[5] === window && ev2.composedPath()[4] === document'), true);
  eq('non-bubbling stays at target', w.run('log = []; tgt.dispatchEvent(new Event("click")); log.join(" ")'), 'wc:1:W dc:1:D mc:1:DIV tc:2:BUTTON tb:2:BUTTON same on:true');
  eq('stopPropagation', w.run('log = []; var sp = function (ev) { ev.stopPropagation(); }; mid.addEventListener("click", sp, true); tgt.dispatchEvent(new Event("click", { bubbles: true })); mid.removeEventListener("click", sp, true); log.join(" ")'), 'wc:1:W dc:1:D mc:1:DIV');
  eq('stopImmediatePropagation', w.run('log = []; var sip = function (ev) { ev.stopImmediatePropagation(); log.push("sip"); }; tgt.addEventListener("click", sip, true); tgt.dispatchEvent(new Event("click", { bubbles: true })); tgt.removeEventListener("click", sip, true); log.join(" ")'), 'wc:1:W dc:1:D mc:1:DIV tc:2:BUTTON tb:2:BUTTON same sip');
  eq('preventDefault', w.run('var pd = function (ev) { ev.preventDefault(); }; tgt.addEventListener("click", pd); var ok1 = tgt.dispatchEvent(new Event("click", { cancelable: true })); var ok2 = tgt.dispatchEvent(new Event("click")); tgt.removeEventListener("click", pd); ok1 === false && ok2 === true'), true);
  eq('once', w.run('var cnt = 0; tgt.addEventListener("o", function () { cnt++; }, { once: true }); tgt.dispatchEvent(new Event("o")); tgt.dispatchEvent(new Event("o")); cnt'), 1);
  eq('passive', w.run('var pev = new Event("pv", { cancelable: true }); tgt.addEventListener("pv", function (ev) { ev.preventDefault(); }, { passive: true }); tgt.dispatchEvent(pev); !pev.defaultPrevented'), true);
  eq('signal', w.run('var ac = new AbortController(); var sc = 0; tgt.addEventListener("sg", function () { sc++; }, { signal: ac.signal }); tgt.dispatchEvent(new Event("sg")); ac.abort(); tgt.dispatchEvent(new Event("sg")); sc'), 1);
  eq('handleEvent object', w.run('var hc = 0; var ho = { handleEvent: function (ev) { hc++; this.seen = ev.type; } }; tgt.addEventListener("he", ho); tgt.dispatchEvent(new Event("he")); hc === 1 && ho.seen === "he"'), true);
  eq('onX returning false prevents', w.run('tgt.onclick = function () { return false; }; var rr = tgt.dispatchEvent(new Event("click", { cancelable: true })); tgt.onclick = null; rr === false && tgt.onclick === null'), true);
  eq('on* props exist', w.run('"onclick" in tgt && "oninput" in tgt && "onload" in window && "onerror" in window && "onkeydown" in document && "onDOMContentLoaded" in document === false'), true);
  eq('removed listener during dispatch not called', w.run('var rc = []; var l1 = function () { rc.push(1); tgt.removeEventListener("rm", l2); }; var l2 = function () { rc.push(2); }; tgt.addEventListener("rm", l1); tgt.addEventListener("rm", l2); tgt.dispatchEvent(new Event("rm")); rc.join()'), '1');
  eq('listener on detached element', w.run('var det = document.createElement("div"); var dc = 0; det.addEventListener("z", function () { dc++; }); window.addEventListener("z", function () { dc += 10; }); det.dispatchEvent(new Event("z", { bubbles: true })); dc'), 1);
  throws('dispatch non-event', function () { w.run('tgt.dispatchEvent({})'); }, 'TypeError');
  host._logs.length = 0; w.run('var rd = new Event("rd"); tgt.addEventListener("rd", function (ev) { tgt.dispatchEvent(ev); }); tgt.dispatchEvent(rd)');
  check('re-dispatch during dispatch reported', host._logs.some(function (l) { return l[0] === 'error' && /already being dispatched/.test(l[1]); }), JSON.stringify(host._logs));
  // errors in listeners
  host._logs.length = 0;
  eq('listener error isolated', w.run('var after = false; var winErr = []; window.addEventListener("error", function (ev) { winErr.push(ev.message + "|" + (ev.error instanceof Error) + "|" + (ev instanceof ErrorEvent)); }); window.onerror = function (msg, src, line, col, err) { winErr.push("onerror:" + msg + ":" + (err instanceof Error)); }; tgt.addEventListener("boom", function () { throw new Error("kaboom"); }); tgt.addEventListener("boom", function () { after = true; }); var okb = tgt.dispatchEvent(new Event("boom")); window.onerror = null; after && okb && winErr.join(";")'), 'kaboom|true|true;onerror:kaboom:true');
  check('listener error logged', host._logs.some(function (l) { return l[0] === 'error' && /kaboom/.test(l[1]); }), JSON.stringify(host._logs));

  // ---------------------------------------------------------------- __swDispatch & friends
  section = 'go-dispatch';
  var app = w.run('document.getElementById("app")');
  eq('__swDispatch returns true', w.run('__swDispatch(' + host.getElementById('in') + ', "focus", {})'), true);
  eq('activeElement after focus', w.run('document.activeElement.id'), 'in');
  eq('__swDispatch prevented false', w.run('var kin = document.getElementById("in"); kin.addEventListener("keydown", function (ev) { if (ev.key === "Escape") ev.preventDefault(); }); __swDispatch(kin.__swid || ' + host.getElementById('in') + ', "keydown", { key: "Escape", bubbles: true, cancelable: true })'), false);
  eq('__swDispatch keydown ok', w.run('__swDispatch(' + host.getElementById('in') + ', "keydown", { key: "a" })'), true);
  eq('__swDispatch chooses class', w.run('var cls = []; document.addEventListener("pointerdown", function (ev) { cls.push(ev.constructor.name + ":" + ev.isTrusted + ":" + ev.bubbles + ":" + ev.clientX); }); __swDispatch(' + host.getElementById('in') + ', "pointerdown", { clientX: 3 }); cls[0]'), 'PointerEvent:true:true:3');
  eq('__swDispatch click checkbox toggles before handlers', w.run('var cb2 = document.getElementById("cb"); cb2.checked = false; var seenChecked = null; cb2.addEventListener("click", function () { seenChecked = cb2.checked; }); var okc = __swDispatch(' + host.getElementById('cb') + ', "click", {}); okc && seenChecked === true && cb2.checked === true'), true);
  eq('__swDefaultAction checkbox fires change', w.run('var chg = 0; cb2.addEventListener("change", function () { chg++; }); cb2.addEventListener("input", function () { chg += 10; }); __swDefaultAction(' + host.getElementById('cb') + '); chg'), 11);
  eq('__swDispatch click prevented reverts checkbox', w.run('cb2.addEventListener("click", function (ev) { ev.preventDefault(); }, { once: true }); var okp = __swDispatch(' + host.getElementById('cb') + ', "click", {}); okp === false && cb2.checked === true'), true);
  eq('element.click() full flow', w.run('cb2.click(); cb2.checked === false'), true);
  eq('click on link navigates', w.run('var lnk = document.createElement("a"); lnk.href = "/go"; lnk.textContent = "L"; document.body.appendChild(lnk); lnk.click(); __sw._navigations[__sw._navigations.length - 1]'), 'https://example.test/go');
  eq('click prevented no nav', w.run('var nb = __sw._navigations.length; lnk.addEventListener("click", function (ev) { ev.preventDefault(); }); lnk.click(); __sw._navigations.length === nb'), true);
  eq('click disabled button ignored', w.run('var dbt = document.createElement("button"); dbt.disabled = true; var dcl = 0; dbt.addEventListener("click", function () { dcl++; }); dbt.click(); dcl'), 0);
  eq('__swDispatchKey types into input', w.run('kin.value = ""; kin.focus(); var kseq = []; kin.addEventListener("beforeinput", function (ev) { kseq.push("bi:" + ev.data); }); kin.addEventListener("input", function (ev) { kseq.push("in:" + kin.value + ":" + ev.inputType + ":" + (ev instanceof InputEvent)); }); kin.addEventListener("keypress", function (ev) { kseq.push("kp:" + ev.charCode); }); __swDispatchKey(' + host.getElementById('in') + ', "h"); __swDispatchKey(' + host.getElementById('in') + ', "i"); __swDispatchKey(' + host.getElementById('in') + ', "Backspace"); kin.value + "|" + kseq.join(",")'), 'h|kp:104,bi:h,in:h:insertText:true,kp:105,bi:i,in:hi:insertText:true,bi:null,in:h:deleteContentBackward:true');
  eq('__swDispatchKey prevented keydown', w.run('kin.addEventListener("keydown", function (ev) { if (ev.key === "z") ev.preventDefault(); }); var okz = __swDispatchKey(' + host.getElementById('in') + ', "z"); okz === false && kin.value === "h"'), true);
  eq('__swDispatchKey code', w.run('var codes = []; kin.addEventListener("keydown", function (ev) { codes.push(ev.code + "/" + ev.keyCode); }); __swDispatchKey(' + host.getElementById('in') + ', "Enter"); __swDispatchKey(' + host.getElementById('in') + ', "A"); codes.join()'), 'Enter/13,KeyA/65');
  eq('__swSetValueFromGo', w.run('var vf = []; kin.addEventListener("input", function () { vf.push("i:" + kin.value); }); kin.addEventListener("change", function () { vf.push("c"); }); __swSetValueFromGo(' + host.getElementById('in') + ', "filled"); vf.join()'), 'i:filled,c');
  eq('__swSetValueFromGo select', w.run('__swSetValueFromGo(' + host.getElementById('sel') + ', "Three"); sel.value'), '3');
  eq('__swMouse', w.run('__sw._pointTarget = ' + host.getElementById('app') + '; var mm = []; app.addEventListener("mousemove", function (ev) { mm.push(ev.clientX + "," + ev.clientY + "," + ev.target.id); }); __swMouse("mousemove", 11, 22); mm[0]'), '11,22,app');
  eq('__swMouse click', w.run('var mc = 0; app.addEventListener("click", function () { mc++; }); __swMouse("click", 1, 1) && mc === 1'), true);
  eq('__swTouch', w.run('var tt = []; app.addEventListener("touchstart", function (ev) { tt.push(ev.touches.length + ":" + ev.changedTouches[0].target.id + ":" + (ev instanceof TouchEvent)); }); __swTouch(' + host.getElementById('app') + ', "touchstart"); tt[0]'), '1:app:true');
  eq('__swDrag', w.run('var dsrc = document.createElement("div"); var ddst = document.createElement("div"); document.body.appendChild(dsrc); document.body.appendChild(ddst); var dlog = []; dsrc.addEventListener("dragstart", function (ev) { ev.dataTransfer.setData("text/plain", "payload"); dlog.push("start"); }); ddst.addEventListener("dragover", function (ev) { ev.preventDefault(); dlog.push("over"); }); ddst.addEventListener("drop", function (ev) { ev.preventDefault(); dlog.push("drop:" + ev.dataTransfer.getData("text")); }); dsrc.addEventListener("dragend", function () { dlog.push("end"); }); __swDrag(' + '__swIdOf(dsrc), __swIdOf(ddst)) + "|" + dlog.join()'), 'true|start,over,drop:payload,end');
  eq('__swWheel', w.run('var wl = []; document.addEventListener("wheel", function (ev) { wl.push(ev.deltaY + ":" + (ev instanceof WheelEvent)); }); __swWheel(0, 120); wl[0]'), '120:true');
  eq('focus/blur events', w.run('var fl = []; var i2 = document.createElement("input"); document.body.appendChild(i2); ["focus", "blur", "focusin", "focusout"].forEach(function (t) { i2.addEventListener(t, function (ev) { fl.push(t + (ev.bubbles ? "+" : "-")); }); }); document.body.addEventListener("focusin", function () { fl.push("body-focusin"); }); i2.focus(); i2.focus(); i2.blur(); document.activeElement === document.body && fl.join()'), 'focus-,focusin+,body-focusin,blur-,focusout+');
  eq('focus moves', w.run('i2.focus(); kin.focus(); document.activeElement === kin && (kin.remove(), document.activeElement === document.body)'), true);

  // ---------------------------------------------------------------- timers
  section = 'timers';
  eq('ids increasing ints', w.run('var ta = setTimeout(function () {}, 0); var tb = setTimeout(function () {}, 0); clearTimeout(ta); clearTimeout(tb); Number.isInteger(ta) && tb > ta && ta > 0'), true);
  eq('runTimers none', w.run('__swRunTimers(0)'), -1);
  host._now = 100;
  var tres = w.run([
    'var tl = []; setTimeout(function (a, b) { tl.push("t10:" + a + b); }, 10, "x", "y"); setTimeout(function () { tl.push("t0"); }); setTimeout(function () { tl.push("t5"); }, 5);',
    'var ivc = 0; var iv = setInterval(function () { tl.push("iv"); if (++ivc === 3) clearInterval(iv); }, 7);',
    'var canc = setTimeout(function () { tl.push("cancelled"); }, 1); clearTimeout(canc);',
    'setTimeout("tl.push(\\"str\\")", 0);',
    'var next1 = __swRunTimers(100); var l1 = tl.join(); tl = [];',
    'var next2 = __swRunTimers(105); var l2 = tl.join(); tl = [];',
    'var next3 = __swRunTimers(130); var l3 = tl.join(); tl = [];',
    'var next4 = __swRunTimers(200);',
    '[next1, l1, next2, l2, next3, l3, next4].join("|")'].join('\n'));
  eq('timer ordering/intervals', tres, '5|t0,str|2|t5|7|iv,t10:xy|7');
  eq('nested setTimeout during run', w.run('var nl = []; setTimeout(function () { nl.push("a"); setTimeout(function () { nl.push("b"); }, 0); }, 0); __swRunTimers(300); var n1 = nl.join(); __swRunTimers(300); n1 + "|" + nl.join()'), 'a|a,b');
  eq('timer error isolated', w.run('setTimeout(function () { throw new Error("timer boom"); }, 0); var tx = 0; setTimeout(function () { tx = 1; }, 0); __swRunTimers(400); tx'), 1);
  check('timer error logged', host._logs.some(function (l) { return /timer boom/.test(l[1]); }));
  eq('rAF', w.run('var rafT = null; requestAnimationFrame(function (ts) { rafT = ts; }); var wait = __swRunTimers(400); __swRunTimers(416); wait === 16 && typeof rafT === "number"'), true);
  eq('cancelAnimationFrame', w.run('var rf = 0; var rid = requestAnimationFrame(function () { rf = 1; }); cancelAnimationFrame(rid); __swRunTimers(1000); rf'), 0);
  eq('requestIdleCallback', w.run('var idl = null; requestIdleCallback(function (d) { idl = typeof d.timeRemaining() === "number" && d.didTimeout === false; }); __swRunTimers(2000); idl'), true);
  eq('performance.now', w.run('typeof performance.now() === "number" && performance.now() >= 0 && performance.getEntriesByType("resource").length === 0 && (performance.mark("m1"), performance.measure("ms", "m1"), performance.getEntriesByName("ms")[0].entryType === "measure")'), true);
  // microtasks / MessageChannel
  var mcr = await (async function () { w.run('var mcl = []; var ch = new MessageChannel(); ch.port1.onmessage = function (ev) { mcl.push("msg:" + ev.data); }; ch.port2.postMessage("hello"); queueMicrotask(function () { mcl.push("micro"); }); Promise.resolve().then(function () { mcl.push("promise"); }); mcl.push("sync");'); await tick(); w.run('__swRunTimers(5000)'); await tick(); return w.run('mcl.join()'); })();
  eq('MessageChannel + microtasks', mcr, 'sync,micro,promise,msg:hello');
  eq('queueMicrotask error isolated', await (async function () { w.run('queueMicrotask(function () { throw new Error("micro boom"); });'); await tick(); return host._logs.some(function (l) { return /micro boom/.test(l[1]); }); })(), true);

  // ---------------------------------------------------------------- fetch / XHR
  section = 'net';
  host._httpHandler = function (method, u, headers, body) {
    if (/\/json/.test(u)) return { status: 200, statusText: 'OK', headers: { 'content-type': 'application/json', 'x-echo': headers['x-req'] || '' }, body: JSON.stringify({ ok: true, method: method, body: body, url: u }), url: u };
    if (/\/redirect/.test(u)) return { status: 200, statusText: 'OK', headers: {}, body: 'moved', url: 'https://example.test/final' };
    if (/\/fail/.test(u)) return { status: 0, statusText: '', headers: {}, body: '', url: u };
    return { status: 500, statusText: 'Boom', headers: { 'content-type': 'text/plain' }, body: 'err', url: u };
  };
  var fr = await (async function () {
    w.run('var fres = {}; fetch("/json?a=1", { method: "POST", headers: { "X-Req": "hv" }, body: JSON.stringify({ x: 1 }) }).then(function (r) { fres.ok = r.ok; fres.status = r.status; fres.ct = r.headers.get("Content-Type"); fres.echo = r.headers.get("x-echo"); fres.url = r.url; fres.isResp = r instanceof Response; fres.hdrs = r.headers instanceof Headers; return r.json(); }).then(function (j) { fres.json = j; }).catch(function (e) { fres.err = String(e); });');
    await tick(); await tick();
    return w.run('JSON.stringify(fres)');
  })();
  var fj = JSON.parse(fr);
  eq('fetch ok/status/headers', fj.ok === true && fj.status === 200 && fj.ct === 'application/json' && fj.echo === 'hv' && fj.isResp && fj.hdrs, true, fr);
  eq('fetch url resolved and json', fj.url === 'https://example.test/json?a=1' && fj.json && fj.json.ok === true && fj.json.method === 'POST' && fj.json.body === '{"x":1}', true, fr);
  check('fetch recorded in host', host._http.some(function (c) { return c.method === 'POST' && c.headers['x-req'] === 'hv' && c.headers['content-type'] === 'text/plain;charset=UTF-8'; }), JSON.stringify(host._http));
  eq('fetch 500 not ok + text', await (async function () { w.run('var f5; fetch("/x").then(function (r) { return r.text().then(function (t) { f5 = r.status + ":" + r.ok + ":" + t + ":" + r.statusText; }); });'); await tick(); await tick(); return w.run('f5'); })(), '500:false:err:Boom');
  eq('fetch network failure rejects', await (async function () { w.run('var ff; fetch("/fail").then(function () { ff = "resolved"; }, function (e) { ff = e.name + ":" + (e instanceof TypeError); });'); await tick(); await tick(); return w.run('ff'); })(), 'TypeError:true');
  eq('fetch redirected', await (async function () { w.run('var frd; fetch("/redirect").then(function (r) { frd = r.redirected + ":" + r.url; });'); await tick(); await tick(); return w.run('frd'); })(), 'true:https://example.test/final');
  eq('fetch abort', await (async function () { w.run('var fab; var abc = new AbortController(); fetch("/json", { signal: abc.signal }).catch(function (e) { fab = e.name; }); abc.abort();'); await tick(); await tick(); return w.run('fab'); })(), 'AbortError');
  eq('body used once', await (async function () { w.run('var bu; fetch("/json").then(function (r) { return r.text().then(function () { return r.text(); }); }).catch(function (e) { bu = e instanceof TypeError; });'); await tick(); await tick(); await tick(); return w.run('bu'); })(), true);
  eq('Response ctor', await (async function () { w.run('var rc; new Response("hi", { status: 201, headers: { a: "b" } }).text().then(function (t) { rc = t; }); var rj = Response.json({ q: 1 }); var rjh = rj.headers.get("content-type");'); await tick(); return w.run('rc + "|" + rjh + "|" + new Response(null).status + "|" + Response.error().type'); })(), 'hi|application/json|200|error');
  eq('Request', w.run('var rq = new Request("/p", { method: "post", body: "b", headers: [["A", "1"]] }); rq.url === "https://example.test/p" && rq.method === "POST" && rq.headers.get("a") === "1" && rq.headers.get("content-type") === "text/plain;charset=UTF-8" && new Request(rq).url === rq.url && rq.signal instanceof AbortSignal'), true);
  throws('Request GET body', function () { w.run('new Request("/", { body: "x" })'); }, 'TypeError');
  eq('Headers', w.run('var hh = new Headers({ "Content-Type": "a" }); hh.append("x", "1"); hh.append("X", "2"); hh.get("x") === "1, 2" && hh.has("content-type") && Array.from(hh.keys()).join() === "content-type,x" && (hh.delete("x"), !hh.has("x")) && Array.from(new Headers([["k", "v"]]))[0].join("=") === "k=v"'), true);
  var xr = await (async function () {
    w.run('var xl = []; var xhr = new XMLHttpRequest(); xhr.onreadystatechange = function () { xl.push("rs" + xhr.readyState); }; xhr.addEventListener("load", function () { xl.push("load:" + xhr.status + ":" + xhr.responseText + ":" + xhr.getResponseHeader("Content-Type") + ":" + (xhr.getAllResponseHeaders().indexOf("content-type:") >= 0)); }); xhr.onloadend = function () { xl.push("end"); }; xhr.open("GET", "/json"); xhr.setRequestHeader("X-Req", "xx"); xhr.send(); xl.push("sent:" + xhr.readyState);');
    w.run('__swRunTimers(6000)');
    return w.run('xl.join(" ")');
  })();
  eq('XHR async', xr, 'rs1 sent:1 rs2 rs3 rs4 load:200:{"ok":true,"method":"GET","body":null,"url":"https://example.test/json"}:application/json:true end');
  eq('XHR sync', w.run('var sx = new XMLHttpRequest(); sx.open("GET", "/json", false); sx.send(); sx.status === 200 && sx.readyState === 4 && JSON.parse(sx.responseText).ok === true'), true);
  eq('XHR responseType json', w.run('var jx = new XMLHttpRequest(); jx.responseType = "json"; jx.open("POST", "/json", false); jx.send("payload"); jx.response.body'), 'payload');
  eq('XHR error', w.run('var ex = new XMLHttpRequest(); var exl = []; ex.onerror = function () { exl.push("error:" + ex.status); }; ex.onload = function () { exl.push("load"); }; ex.open("GET", "/fail"); ex.send(); __swRunTimers(7000); exl.join()'), 'error:0');
  eq('XHR abort', w.run('var ax = new XMLHttpRequest(); var axl = []; ax.onabort = function () { axl.push("abort"); }; ax.onload = function () { axl.push("load"); }; ax.open("GET", "/json"); ax.send(); ax.abort(); __swRunTimers(8000); axl.join() + ":" + ax.readyState'), 'abort:0');
  throws('XHR send before open', function () { w.run('new XMLHttpRequest().send()'); }, 'InvalidStateError');
  eq('sendBeacon', w.run('navigator.sendBeacon("/b", "data") && __sw._http[__sw._http.length - 1].method === "POST"'), true);

  // ---------------------------------------------------------------- storage/cookie/url/history
  section = 'storage';
  eq('localStorage', w.run('localStorage.setItem("k", 1); localStorage.getItem("k") === "1" && localStorage.length === 1 && localStorage.key(0) === "k" && localStorage.k === "1" && (localStorage.other = "v", localStorage.getItem("other") === "v") && "k" in localStorage && Object.keys(localStorage).join() === "k,other" && localStorage.getItem("missing") === null && (localStorage.removeItem("k"), localStorage.getItem("k") === null) && (delete localStorage.other, localStorage.length === 0) && localStorage instanceof Storage'), true);
  eq('sessionStorage separate', w.run('sessionStorage.setItem("s", "1"); localStorage.getItem("s") === null && sessionStorage.getItem("s") === "1" && (sessionStorage.clear(), sessionStorage.length === 0)'), true);
  check('storage in host', host._storage.local.size === 0 && host._storage.session.size === 0);
  eq('cookie', w.run('document.cookie = "a=1; path=/"; document.cookie = "b=2"; document.cookie'), 'a=1; b=2');
  eq('URL', w.run('var u = new URL("../q?x=1&y=%20z#h", "https://u:p@Example.COM:8080/a/b/c"); [u.href, u.origin, u.protocol, u.username, u.host, u.hostname, u.port, u.pathname, u.search, u.hash, u.searchParams.get("y")].join("|")'), 'https://u:p@example.com:8080/a/q?x=1&y=%20z#h|https://example.com:8080|https:|u|example.com:8080|example.com|8080|/a/q|?x=1&y=%20z|#h| z');
  eq('URL default port/normalise', w.run('new URL("HTTP://a.com:80/./x/../y").href + "|" + new URL("//cdn.x/y", "https://a.com/").href + "|" + new URL("?q", "https://a.com/p#h").href + "|" + new URL("#f", "https://a.com/p?q").href + "|" + new URL("mailto:a@b.c").pathname + "|" + new URL("data:text/plain,hi").protocol + "|" + new URL("about:blank").href'), 'http://a.com/y|https://cdn.x/y|https://a.com/p?q|https://a.com/p?q#f|a@b.c|data:|about:blank');
  eq('URL setters', w.run('var u2 = new URL("https://a.com/p"); u2.pathname = "x y"; u2.search = "a=b c"; u2.hash = "h"; u2.port = "443"; u2.hostname = "B.com"; u2.href'), 'https://b.com/x%20y?a=b%20c#h');
  throws('URL invalid', function () { w.run('new URL("nope")'); }, 'TypeError');
  throws('URL empty host', function () { w.run('new URL("https://")'); }, 'TypeError');
  eq('URL.canParse / toJSON', w.run('URL.canParse("x") === false && URL.canParse("x", "https://a/") && JSON.stringify({ u: new URL("https://a/") }) === "{\\"u\\":\\"https://a/\\"}"'), true);
  eq('URLSearchParams', w.run('var sp = new URLSearchParams("a=1&b=2&a=3&c=%20x+y"); sp.getAll("a").join() === "1,3" && sp.get("c") === " x y" && sp.has("b") && (sp.append("d", "é&"), sp.set("a", "9"), sp.delete("b"), sp.toString() === "a=9&c=+x+y&d=%C3%A9%26") && Array.from(sp.keys()).join() === "a,c,d" && new URLSearchParams({ k: "v" }).toString() === "k=v" && new URLSearchParams([["x", "1"]]).get("x") === "1" && sp.size === 3'), true);
  eq('searchParams linked', w.run('var u3 = new URL("https://a/?x=1"); u3.searchParams.append("y", "2"); u3.searchParams.sort(); u3.href === "https://a/?x=1&y=2" && (u3.search = "?z=3", u3.searchParams.get("z") === "3" && u3.searchParams.get("x") === null)'), true);
  eq('history.pushState', w.run('history.pushState({ s: 1 }, "", "/new?x#y"); location.pathname === "/new" && location.search === "?x" && location.hash === "#y" && history.state.s === 1 && history.length === 2 && document.URL === "https://example.test/new?x#y" && __sw._navigations.filter(function (n) { return typeof n === "string"; }).length === 1'), true);
  eq('history.replaceState', w.run('history.replaceState(null, "", "rel"); location.pathname === "/rel" && history.length === 2 && history.state === null'), true);
  throws('pushState cross-origin', function () { w.run('history.pushState(null, "", "https://other.test/")'); }, 'SecurityError');
  eq('location.hash set fires hashchange', w.run('var hc2 = []; window.addEventListener("hashchange", function (ev) { hc2.push(ev.newURL); }); window.addEventListener("popstate", function () { hc2.push("pop"); }); location.hash = "#sec"; hc2.join() + "|" + location.hash'), 'https://example.test/rel#sec,pop|#sec');
  eq('location.assign navigates', w.run('location.assign("/else"); __sw._navigations[__sw._navigations.length - 1]'), 'https://example.test/else');
  eq('location.href set navigates', w.run('location.href = "https://x.test/"; __sw._navigations[__sw._navigations.length - 1]'), 'https://x.test/');
  eq('window.location = str', w.run('window.location = "/z"; __sw._navigations[__sw._navigations.length - 1]'), 'https://example.test/z');
  eq('String(location)', w.run('String(location) === location.href && location instanceof Location'), true);

  // ---------------------------------------------------------------- MutationObserver
  section = 'mutation';
  var mo = await (async function () {
    w.run([
      'var mrecs = []; var moTarget = document.createElement("div"); document.body.appendChild(moTarget);',
      'var mo = new MutationObserver(function (recs, obs) { recs.forEach(function (r) { mrecs.push(r.type + ":" + (r.target === moTarget ? "T" : r.target.nodeType === 1 ? r.target.tagName : r.target.nodeName) + ":" + r.addedNodes.length + ":" + r.removedNodes.length + ":" + r.attributeName + ":" + r.oldValue + ":" + (obs === mo)); }); });',
      'mo.observe(moTarget, { childList: true, attributes: true, attributeOldValue: true, subtree: true, characterData: true, characterDataOldValue: true });',
      'var k1 = document.createElement("span"); moTarget.appendChild(k1); moTarget.setAttribute("a", "1"); moTarget.setAttribute("a", "2"); k1.setAttribute("b", "x"); k1.textContent = "t"; k1.firstChild.data = "u"; moTarget.innerHTML = "<i></i>"; moTarget.removeChild(moTarget.firstChild);',
      'var sync = mrecs.length;'].join('\n'));
    await tick();
    return w.run('sync + "|" + mrecs.join(" ")');
  })();
  eq('MutationObserver records', mo, '0|childList:T:1:0:null:null:true attributes:T:0:0:a:null:true attributes:T:0:0:a:1:true attributes:SPAN:0:0:b:null:true childList:SPAN:1:0:null:null:true characterData:#text:0:0:null:t:true childList:T:1:1:null:null:true childList:T:0:1:null:null:true');
  eq('MutationObserver microtask batching', await (async function () { w.run('var batches = 0; var mo2 = new MutationObserver(function (r) { batches++; }); mo2.observe(moTarget, { attributes: true }); moTarget.setAttribute("p", "1"); moTarget.setAttribute("p", "2"); moTarget.setAttribute("p", "3");'); await tick(); return w.run('batches'); })(), 1);
  eq('attributeFilter + takeRecords + disconnect', w.run('var mo3 = new MutationObserver(function () {}); mo3.observe(moTarget, { attributes: true, attributeFilter: ["x"] }); moTarget.setAttribute("y", "1"); moTarget.setAttribute("x", "1"); var tr = mo3.takeRecords(); mo3.disconnect(); moTarget.setAttribute("x", "2"); tr.length === 1 && tr[0].attributeName === "x" && mo3.takeRecords().length === 0'), true);
  eq('__swFlushMutationObservers', w.run('var flushed = 0; var mo4 = new MutationObserver(function () { flushed++; }); mo4.observe(moTarget, { childList: true }); moTarget.appendChild(document.createElement("b")); __swFlushMutationObservers(); flushed'), 1);
  eq('no records without subtree', await (async function () { w.run('var nos = 0; var mo5 = new MutationObserver(function () { nos++; }); mo5.observe(moTarget, { attributes: true }); moTarget.firstElementChild.setAttribute("q", "1");'); await tick(); return w.run('nos'); })(), 0);
  throws('observe without options', function () { w.run('new MutationObserver(function () {}).observe(moTarget, {})'); }, 'TypeError');
  eq('IntersectionObserver callback', w.run('var io = null; new IntersectionObserver(function (entries) { io = entries[0].isIntersecting && entries[0].target === moTarget && entries[0].boundingClientRect.width === 100; }).observe(moTarget); __swRunTimers(9000); io'), true);
  eq('ResizeObserver callback', w.run('var ro = null; new ResizeObserver(function (entries) { ro = entries[0].contentRect.height === 30 && entries[0].target === moTarget; }).observe(moTarget); __swRunTimers(9001); ro'), true);

  // ---------------------------------------------------------------- lifecycle
  section = 'lifecycle';
  eq('lifecycle order', w.run('var lc = []; document.addEventListener("DOMContentLoaded", function (ev) { lc.push("dcl:" + document.readyState + ":" + (ev.target === document) + ":" + ev.bubbles); }); window.addEventListener("DOMContentLoaded", function () { lc.push("dcl-window"); }); document.addEventListener("readystatechange", function () { lc.push("rs:" + document.readyState); }); window.addEventListener("load", function (ev) { lc.push("load:" + document.readyState + ":" + (ev.target === window)); }); window.onload = function () { lc.push("onload"); }; window.addEventListener("pageshow", function (ev) { lc.push("pageshow:" + ev.persisted); }); __swFireLifecycle("DOMContentLoaded"); __swFireLifecycle("load"); lc.join(" ")'), 'rs:interactive dcl:interactive:true:true dcl-window rs:complete load:complete:true onload pageshow:false');
  eq('beforeunload', w.run('window.addEventListener("beforeunload", function (ev) { ev.preventDefault(); }); __swFireLifecycle("beforeunload") === false'), true);
  eq('unload', w.run('var ul2 = []; window.addEventListener("pagehide", function () { ul2.push("hide"); }); window.addEventListener("unload", function () { ul2.push("unload"); }); __swFireLifecycle("unload"); ul2.join()'), 'hide,unload');
  eq('currentScript', w.run('var scr = document.createElement("script"); document.head.appendChild(scr); __swSetCurrentScript(__swIdOf(scr)); var cs = document.currentScript === scr; __swSetCurrentScript(0); cs && document.currentScript === null'), true);
  eq('__swIsThenable', w.run('__swIsThenable(Promise.resolve()) && __swIsThenable({ then: function () {} }) && !__swIsThenable({}) && !__swIsThenable(null) && !__swIsThenable(1) && __swIsThenable(Object.assign(function () {}, { then: function () {} }))'), true);
  eq('getComputedStyle', w.run('var gcs = document.createElement("div"); gcs.style.color = "red"; document.body.appendChild(gcs); var cs2 = getComputedStyle(gcs); cs2.color === "red" && cs2.getPropertyValue("color") === "red" && cs2.display === "block" && cs2.getPropertyValue("margin-top") === "0px" && getComputedStyle(document.createElement("span")).display === "inline"'), true);
  eq('getBoundingClientRect/offsets', w.run('var bb = app.getBoundingClientRect(); bb.width === 100 && bb.x === 10 && bb.left === 10 && bb.right === 110 && bb.bottom === 50 && app.offsetWidth === 100 && app.clientHeight === 30 && app.offsetTop === 20 && typeof bb.toJSON === "function" && app.scrollTop === 0'), true);
  eq('dialogs', w.run('alert("a"); var cf = confirm("c"); __sw._dialogHandler = function (k, m, d) { return { accepted: k === "prompt", value: d + "!" }; }; var pr = prompt("p", "dv"); var pc = confirm("x"); __sw._dialogHandler = null; cf === true && pr === "dv!" && pc === false && __sw._dialogs.length === 4 && __sw._dialogs[0][0] === "alert"'), true);

  // ---------------------------------------------------------------- console
  section = 'console';
  host._logs.length = 0;
  w.run('var cyc = { a: 1, s: "str", f: function named() {}, n: null, u: undefined, arr: [1, "two", [3]], el: document.getElementById("app"), d: new Date(0), m: new Map([["k", 1]]) }; cyc.self = cyc; console.log("hello %s and %d and %o", "world", 42.5, { x: 1 }, "extra", cyc); console.warn("w"); console.error(new Error("E1")); console.info(1, true, null, undefined); console.debug([document.body]); console.table([1]); console.group("g"); console.log("inner"); console.groupEnd(); console.log("%c styled", "color: red"); console.assert(false, "assertion"); console.count(); console.count(); console.dir(document.body);');
  var L = host._logs;
  eq('console.log format', L[0][0] === 'log' && L[0][1], 'hello world and 42 and {x: 1} extra {a: 1, s: "str", f: [Function: named], n: null, u: undefined, arr: [1, "two", [3]], el: <div id="app" class="a b">, d: 1970-01-01T00:00:00.000Z, m: Map(1) [["k", 1]], self: [Circular]}');
  eq('console.warn level', L[1][0] + ':' + L[1][1], 'warn:w');
  eq('console.error Error', L[2][0] === 'error' && /Error: E1/.test(L[2][1]), true, L[2][1]);
  eq('console.info prims', L[3][1], '1 true null undefined');
  eq('console.debug node array', L[4][0] + ':' + L[4][1], 'debug:[<body>]');
  eq('console.group indent', L[6][1] + '|' + L[7][1], 'g|  inner');
  eq('console %c stripped', L[8][1], ' styled');
  eq('console.assert', L[9][0] + ':' + L[9][1], 'error:Assertion failed: assertion');
  eq('console.count', L[10][1] + '|' + L[11][1], 'default: 1|default: 2');
  eq('console.dir node', L[12][1], '<body>');

  // ---------------------------------------------------------------- custom elements
  section = 'custom-elements';
  eq('customElements.define + upgrade', w.run([
    'var celog = [];',
    'class MyEl extends HTMLElement { constructor() { super(); this._ctor = true; celog.push("ctor"); } connectedCallback() { celog.push("connected:" + this.isConnected); } disconnectedCallback() { celog.push("disconnected"); } attributeChangedCallback(n, o, v) { celog.push("attr:" + n + ":" + o + ":" + v); } static get observedAttributes() { return ["x"]; } }',
    'document.body.insertAdjacentHTML("beforeend", "<my-el id=pre x=0></my-el>");',
    'customElements.define("my-el", MyEl);',
    'var pre = document.getElementById("pre"); var ok1 = pre instanceof MyEl && pre._ctor === true;',
    'var created = document.createElement("my-el"); var ok2 = created instanceof MyEl && created._ctor === true && !created.isConnected;',
    'created.setAttribute("x", "1"); created.setAttribute("y", "1"); document.body.appendChild(created); created.remove();',
    'var fresh = new MyEl(); var ok3 = fresh instanceof HTMLElement && fresh.tagName === "MY-EL" && fresh._ctor;',
    'var viaHTML = document.createElement("div"); viaHTML.innerHTML = "<my-el></my-el>"; var ok4 = viaHTML.firstChild instanceof MyEl; document.body.appendChild(viaHTML);',
    'var ok5 = customElements.get("my-el") === MyEl && customElements.get("no-pe") === undefined;',
    '[ok1, ok2, ok3, ok4, ok5].join() + "|" + celog.join(" ")'].join('\n')), 'true,true,true,true,true|ctor connected:true ctor attr:x:null:1 connected:true disconnected ctor ctor connected:true');
  throws('define bad name', function () { w.run('customElements.define("bad", class extends HTMLElement {})'); }, 'SyntaxError');
  throws('define twice', function () { w.run('customElements.define("my-el", class extends HTMLElement {})'); }, 'NotSupportedError');
  eq('whenDefined', await (async function () { w.run('var wd = null; customElements.whenDefined("my-el").then(function (c) { wd = c === MyEl; }); var wd2 = null; customElements.whenDefined("late-el").then(function () { wd2 = true; }); customElements.define("late-el", class extends HTMLElement {});'); await tick(); return w.run('wd && wd2'); })(), true);

  // ---------------------------------------------------------------- misc platform
  section = 'misc';
  eq('DOMParser', w.run('var pd = new DOMParser().parseFromString("<p id=pp>hi</p><b>x</b>", "text/html"); pd.body.children.length === 2 && pd.getElementById("pp").textContent === "hi" && pd.querySelector("b").tagName === "B" && pd.documentElement.tagName === "HTML" && !pd.body.isConnected && pd.nodeType === 9 && pd.body.ownerDocument === document'), true);
  eq('DOMParser full doc', w.run('var pd2 = new DOMParser().parseFromString("<!DOCTYPE html><html><head><title>TT</title></head><body class=bb><div>d</div></body></html>", "text/html"); pd2.title === "TT" && pd2.body.className === "bb" && pd2.body.firstChild.tagName === "DIV"'), true);
  eq('implementation.createHTMLDocument', w.run('var hd = document.implementation.createHTMLDocument("x"); hd.title === "x" && hd.body.tagName === "BODY"'), true);
  eq('atob/btoa', w.run('btoa("hello") === "aGVsbG8=" && atob("aGVsbG8=") === "hello" && btoa("") === "" && atob("aGk") === "hi"'), true);
  throws('btoa non-latin1', function () { w.run('btoa("é€")'); }, 'InvalidCharacterError');
  eq('TextEncoder/Decoder', w.run('var te = new TextEncoder().encode("héllo €"); te instanceof Uint8Array && te.length === 10 && new TextDecoder().decode(te) === "héllo €" && new TextDecoder().decode(new Uint8Array([0xef, 0xbb, 0xbf, 65])) === "A" && new TextDecoder("utf-8").encoding === "utf-8"'), true);
  eq('structuredClone', w.run('var src = { a: [1, { b: 2 }], d: new Date(5), m: new Map([[1, 2]]), s: new Set([1]) }; src.self = src; var cl = structuredClone(src); cl !== src && cl.a[1].b === 2 && cl.a !== src.a && cl.d.getTime() === 5 && cl.m.get(1) === 2 && cl.s.has(1) && cl.self === cl'), true);
  throws('structuredClone function', function () { w.run('structuredClone(function () {})'); }, 'DataCloneError');
  eq('crypto', w.run('var rv = crypto.getRandomValues(new Uint8Array(8)); rv.length === 8 && /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(crypto.randomUUID())'), true);
  eq('Blob/File/FileReader', await (async function () { w.run('var bl = new Blob(["ab", new Uint8Array([99])], { type: "text/plain" }); var bt; bl.text().then(function (t) { bt = t; }); var fl2 = new File(["x"], "n.txt", { type: "text/plain" }); var frr = new FileReader(); var frres; frr.onload = function () { frres = frr.result; }; frr.readAsDataURL(fl2); var blInfo = bl.size + ":" + bl.type + ":" + (fl2 instanceof Blob) + ":" + fl2.name;'); await tick(); w.run('__swRunTimers(9999)'); return w.run('blInfo + ":" + bt + ":" + frres'); })(), '3:text/plain:true:n.txt:abc:data:text/plain;base64,eA==');
  eq('URL.createObjectURL', w.run('var ou = URL.createObjectURL(new Blob(["x"])); ou.indexOf("blob:https://example.test/") === 0 && (URL.revokeObjectURL(ou), true)'), true);
  eq('Intl', w.run('typeof Intl.NumberFormat === "function" && new Intl.NumberFormat().format(1234.5) === "1,234.5" && new Intl.NumberFormat("en", { style: "currency", currency: "USD" }).format(3) === "$3.00" && typeof new Intl.DateTimeFormat().format(new Date()) === "string" && (1234567).toLocaleString() === "1,234,567" && new Intl.PluralRules().select(1) === "one" && ["b", "a"].sort(new Intl.Collator().compare).join() === "a,b" && new Intl.RelativeTimeFormat().format(-2, "day") === "2 days ago"'), true);
  eq('WebSocket stub', w.run('var wsl = []; var ws = new WebSocket("wss://x"); ws.onerror = function () { wsl.push("error:" + ws.readyState); }; ws.addEventListener("close", function (ev) { wsl.push("close:" + ev.code); }); __swRunTimers(10000); wsl.join() + ":" + WebSocket.CLOSED'), 'error:3,close:1006:3');
  throws('Worker throws', function () { w.run('new Worker("x.js")'); }, 'NotSupportedError');
  eq('Notification', w.run('Notification.permission === "denied" && typeof Notification.requestPermission === "function" && new Notification("t").title === "t"'), true);
  eq('canvas', w.run('var cv = document.createElement("canvas"); cv.getContext("2d") === null && cv.width === 300 && (cv.width = 10, cv.getAttribute("width") === "10") && cv instanceof HTMLCanvasElement'), true);
  eq('Audio/media', w.run('var au = new Audio("a.mp3"); au instanceof HTMLAudioElement && au.paused && au.src === "https://example.test/a.mp3" && au.play() instanceof Promise && !au.paused && (au.pause(), au.paused)'), true);
  eq('getSelection', w.run('getSelection().rangeCount === 0 && typeof getSelection().removeAllRanges === "function" && document.getSelection() === getSelection()'), true);
  eq('Range', w.run('var rg = document.createRange(); rg.selectNodeContents(app); rg.collapsed === false && typeof rg.getBoundingClientRect === "function" && rg.createContextualFragment("<i>f</i>").firstChild.tagName === "I"'), true);
  eq('scroll no-ops', w.run('scrollTo(0, 1); scrollBy(1, 1); scroll({ top: 0 }); app.scrollIntoView(); app.scrollTo(0, 0); scrollX === 0 && pageYOffset === 0'), true);
  eq('requestFullscreen', w.run('app.requestFullscreen() instanceof Promise && document.fullscreenElement === null'), true);
  eq('navigator misc', w.run('navigator.language === "en-US" && navigator.languages.length === 2 && navigator.onLine && navigator.cookieEnabled && navigator.hardwareConcurrency === 1 && typeof navigator.platform === "string" && typeof navigator.clipboard.writeText === "function" && navigator instanceof Navigator && window.clientInformation === navigator'), true);
  eq('DOMException', w.run('var dx = new DOMException("m", "AbortError"); dx instanceof Error && dx.name === "AbortError" && dx.code === 20 && dx.message === "m"'), true);
  eq('CSS.escape', w.run('CSS.escape("a b#c")'), 'a\\ b\\#c');
  eq('postMessage', w.run('var pm = null; window.addEventListener("message", function (ev) { pm = ev.data.x; }); window.postMessage({ x: 7 }, "*"); __swRunTimers(20000); pm'), 7);
  eq('document.write appends', w.run('document.write("<p id=dw>w</p>"); document.getElementById("dw").parentNode === document.body'), true);
  eq('document.all/forms/images/links/scripts', w.run('document.all.length > 5 && document.images.length >= 0 && document.links.length >= 1 && document.scripts.length === 1 && document.forms.length >= 1'), true);
  eq('img load event', w.run('var ldi = 0; var im2 = new Image(); im2.onload = function () { ldi++; }; im2.src = "x.png"; document.body.appendChild(im2); __swRunTimers(30000); ldi'), 1);
  eq('details toggle', w.run('var dt = document.createElement("details"); dt.innerHTML = "<summary>s</summary>"; document.body.appendChild(dt); var tg = 0; dt.addEventListener("toggle", function () { tg++; }); dt.querySelector("summary").click(); dt.open && tg === 1'), true);
  eq('label click forwards', w.run('var lb2 = document.createElement("label"); var lcb = document.createElement("input"); lcb.type = "checkbox"; lb2.appendChild(lcb); document.body.appendChild(lb2); lb2.click(); lcb.checked'), true);
  eq('radio group', w.run('document.body.insertAdjacentHTML("beforeend", "<input type=radio name=rg id=r1 checked><input type=radio name=rg id=r2>"); document.getElementById("r2").click(); document.getElementById("r2").checked && !document.getElementById("r1").checked'), true);
  eq('table API', w.run('var tb = document.createElement("table"); var row = tb.insertRow(); row.insertCell().textContent = "c"; tb.rows.length === 1 && tb.tBodies.length === 1 && row.cells.length === 1 && tb.rows[0] === row && row.rowIndex === 0'), true);
  eq('select.add/remove', w.run('var s3 = document.createElement("select"); s3.add(new Option("a")); s3.add(new Option("b")); s3.remove(0); s3.length === 1 && s3.options[0].text === "b"'), true);
  eq('input number/maxlength typing', w.run('var ni2 = document.createElement("input"); ni2.type = "number"; document.body.appendChild(ni2); __swDispatchKey(__swIdOf(ni2), "1"); __swDispatchKey(__swIdOf(ni2), "x"); __swDispatchKey(__swIdOf(ni2), "2"); ni2.value === "12" && ni2.valueAsNumber === 12'), true);
  eq('textarea typing + Enter newline', w.run('var ta2 = document.createElement("textarea"); document.body.appendChild(ta2); __swDispatchKey(__swIdOf(ta2), "a"); __swDispatchKey(__swIdOf(ta2), "Enter"); __swDispatchKey(__swIdOf(ta2), "b"); JSON.stringify(ta2.value)'), '"a\\nb"');
  eq('Enter in input submits form', w.run('var ef = document.createElement("form"); ef.innerHTML = "<input name=q>"; document.body.appendChild(ef); var subd = 0; ef.addEventListener("submit", function (ev) { subd++; ev.preventDefault(); }); __swDispatchKey(__swIdOf(ef.firstChild), "Enter"); subd'), 1);
  eq('contenteditable typing', w.run('var ced = document.createElement("div"); ced.contentEditable = "true"; document.body.appendChild(ced); __swDispatchKey(__swIdOf(ced), "q"); ced.textContent'), 'q');
  eq('selectionStart', w.run('var si = document.createElement("input"); si.value = "hello"; si.setSelectionRange(1, 3); si.selectionStart === 1 && si.selectionEnd === 3 && (si.select(), si.selectionEnd === 5)'), true);
  eq('ChildNode on text', w.run('var tx = document.createTextNode("t"); app.appendChild(tx); tx.remove(); tx.parentNode === null && typeof tx.before === "function"'), true);
  eq('CharacterData ops', w.run('var cd = document.createTextNode("abc"); cd.appendData("d"); cd.insertData(0, "0"); cd.deleteData(1, 1); cd.replaceData(0, 1, "Z"); cd.data === "Zbcd" && cd.length === 4 && cd.substringData(1, 2) === "bc" && cd.nodeValue === "Zbcd" && (cd.nodeValue = "n", cd.textContent === "n") && cd.wholeText === "n"'), true);
  eq('Comment', w.run('var cm = document.createComment("c"); app.appendChild(cm); cm.nodeName === "#comment" && cm.data === "c" && app.innerHTML.indexOf("<!--c-->") > 0 && cm instanceof Comment && app.textContent.indexOf("c") < 0'), true);
  eq('shadow stub', w.run('var sh = document.createElement("div"); var sr = sh.attachShadow({ mode: "open" }); sr === sh && sh.shadowRoot === sh'), true);
  eq('attributes of select mirror', w.run('document.createElement("select").type === "select-one" && (function () { var ms = document.createElement("select"); ms.multiple = true; return ms.type === "select-multiple"; })()'), true);

  // ---------------------------------------------------------------- serialize
  section = 'serialize';
  eq('__swSerialize', w.run('var ser = __swSerialize({ n: 1, s: "s", f: function () {}, el: app, arr: [app.firstChild], e: new Error("x"), m: new Map([["k", 1]]), st: new Set([2]), d: new Date(0), nul: null, u: undefined, inf: Infinity, nested: { a: { b: { c: { d: { e: { f: { g: { h: { i: 1 } } } } } } } } } }); var hasStack = typeof ser.e.stack === "string"; delete ser.e.stack; JSON.stringify(ser) + "|" + hasStack'), '{"n":1,"s":"s","f":"function","el":{"nodeType":1,"tagName":"DIV","id":"app","ref":"e1","sweefRef":"e1"},"arr":[{"nodeType":1,"tagName":"EM","id":"","ref":"e2","sweefRef":"e2"}],"e":{"name":"Error","message":"x"},"m":[["k",1]],"st":[2],"d":"1970-01-01T00:00:00.000Z","nul":null,"inf":"Infinity","nested":{"a":{"b":{"c":{"d":{"e":{"f":{"g":"[Object]"}}}}}}}}|true');
  eq('__swSerialize cycle', w.run('var cy = { a: 1 }; cy.me = cy; JSON.stringify(__swSerialize(cy))'), '{"a":1,"me":"[Circular]"}');
  eq('__swSerialize text node/NodeList', w.run('var sn = __swSerialize(document.querySelectorAll("em")); Array.isArray(sn) && sn[0].tagName === "EM" && __swSerialize(document.createTextNode("tx")).data === "tx" && __swSerialize(document).nodeType === 9 && __swSerialize(window) === "[Window]"'), true);
  eq('refOf stable', w.run('__swSerialize(app).ref === __swSerialize(app).ref'), true);

  // ---------------------------------------------------------------- __swSetDocument reset
  section = 'reset';
  var oldApp = w.run('app');
  host._load('<html><head></head><body><div id="fresh"></div></body></html>');
  w.run('setTimeout(function () {}, 1000); window.addEventListener("resize", function () { throw new Error("stale listener"); }); var oldDoc = document;');
  eq('__swSetDocument', w.run('__swSetDocument("https://example.test/next", "loading"); document !== oldDoc && document.getElementById("fresh") !== null && document.readyState === "loading" && __swRunTimers(99999) === -1 && document.body.firstChild.id === "fresh" && document.activeElement === document.body && history.state === null'), true);
  eq('old wrappers detached', w.run('app.isConnected === false && app !== document.getElementById("app")'), true);
  host._logs.length = 0;
  w.run('__swFireLifecycle("resize")');
  check('window listeners cleared', !host._logs.some(function (l) { return /stale listener/.test(l[1]); }));
  eq('new document works', w.run('document.body.innerHTML = "<input id=ni2 value=v>"; document.getElementById("ni2").value === "v" && document.getElementById("ni2") instanceof HTMLInputElement'), true);

  // ---------------------------------------------------------------- React-ish / jQuery-ish smoke
  section = 'framework-smoke';
  eq('React-like feature detection', w.run([
    '(function () {',
    '  var canUseDOM = !!(typeof window !== "undefined" && window.document && window.document.createElement);',
    '  var supportsPassive = false; try { var o = Object.defineProperty({}, "passive", { get: function () { supportsPassive = true; } }); window.addEventListener("test", null, o); window.removeEventListener("test", null, o); } catch (e) {}',
    '  var isInputEventSupported = "oninput" in document.createElement("div");',
    '  var hasMC = typeof MessageChannel === "function" && typeof setImmediate === "function";',
    '  var ns = document.createElementNS("http://www.w3.org/2000/svg", "svg"); ns.innerHTML = "<circle r=1></circle>"; var svgInner = ns.firstChild && ns.firstChild.namespaceURI === "http://www.w3.org/2000/svg";',
    '  var root = document.createElement("div"); document.body.appendChild(root);',
    '  var btn = document.createElement("button"); btn.setAttribute("type", "button"); root.appendChild(btn); var textNode = document.createTextNode("Count: 0"); btn.appendChild(textNode);',
    '  var count = 0; root.addEventListener("click", function (ev) { if (ev.target === btn) { count++; textNode.nodeValue = "Count: " + count; } });',
    '  btn.click(); btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));',
    '  var inp = document.createElement("input"); root.appendChild(inp); var tracked = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value"); var got = []; Object.defineProperty(inp, "value", { configurable: true, get: function () { return tracked.get.call(this); }, set: function (v) { got.push(v); tracked.set.call(this, v); } });',
    '  inp.value = "a"; __swDispatchKey(__swIdOf(inp), "b");',
    '  return [canUseDOM, supportsPassive, isInputEventSupported, hasMC, svgInner, btn.textContent === "Count: 2", inp.value === "ab", got.join("") === "a"].join();',
    '})()'].join('\n')), 'true,true,true,true,true,true,true,true');
  eq('jQuery-like feature checks', w.run([
    '(function () {',
    '  var div = document.createElement("div"); div.innerHTML = "  <link/><table></table><a href=\'/a\'>a</a><input type=\'checkbox\'/>";',
    '  var ok1 = div.getElementsByTagName("a")[0].getAttribute("href") === "/a";',
    '  var ok2 = typeof div.querySelectorAll === "function" && typeof div.matches === "function" && typeof div.getElementsByClassName === "function";',
    '  var ok3 = document.documentElement.contains(document.body) && !document.documentElement.contains(div);',
    '  var ok4 = typeof window.getComputedStyle === "function" && typeof document.createEvent === "function";',
    '  var ok5 = (function () { var e = document.createEvent("HTMLEvents"); e.initEvent("custom", true, true); var hit = 0; document.addEventListener("custom", function () { hit++; }); document.body.dispatchEvent(e); return hit === 1; })();',
    '  var ok6 = div.children.length === 4 && div.childNodes.length === 5 && "classList" in div && div.compareDocumentPosition(document.body) & 1;',
    '  var ok7 = "ownerDocument" in div && div.ownerDocument.defaultView === window;',
    '  var ok8 = (function () { try { return document.querySelector(":checked") !== undefined; } catch (e) { return false; } })();',
    '  var ok9 = typeof div.style.cssText === "string" && typeof div.dataset === "object" && div.style.opacity === "" && (div.style.opacity = 0.5, div.style.opacity === "0.5");',
    '  return [ok1, ok2, ok3, ok4, ok5, !!ok6, ok7, ok8, ok9].join();',
    '})()'].join('\n')), 'true,true,true,true,true,true,true,true,true');

  console.log('prelude harness: ' + passes + ' passed, ' + failures + ' failed');
  process.exit(failures ? 1 : 0);
}
main().catch(function (e) { console.log('HARNESS CRASH in [' + section + ']: ' + (e && e.stack || e)); process.exit(2); });
